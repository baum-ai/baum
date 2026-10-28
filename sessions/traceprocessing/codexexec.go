package traceprocessing

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// CodexExecName is the tool name of Codex's code-mode wrapper. Its input is a
// JavaScript program, not a command: the shell command lines it runs sit inside
// the program as `tools.exec_command({cmd: "..."})` calls.
const CodexExecName = "exec"

// NormalizeCodexExecInput gives a Codex `exec` tool input the documented
// `command` field the execute-command readers look for. The input arrives as a
// JavaScript source string (a native rollout's custom tool call) or as
// {"input": "<source>"} (Entire's compact transcript). Either way the result is
// the object {"input": "<source>", "command": "<command lines>"}, so the source
// is kept and every reader finds the text where Claude Code puts it. Several
// commands in one program are joined with newlines. Input that holds no
// command, or is not a Codex exec input, is returned unchanged.
func NormalizeCodexExecInput(name string, input json.RawMessage) json.RawMessage {
	if name != CodexExecName {
		return input
	}
	var source string
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &source); err == nil {
		object = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(input, &object); err == nil {
		if _, has := object["command"]; has {
			return input
		}
		if _, has := object["cmd"]; has {
			return input
		}
		if err := json.Unmarshal(object["input"], &source); err != nil {
			return input
		}
	} else {
		return input
	}
	command := CodexExecCommand(source)
	if command == "" {
		return input
	}
	commandJSON, _ := json.Marshal(command)
	object["command"] = commandJSON
	if _, has := object["input"]; !has {
		object["input"], _ = json.Marshal(source)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return input
	}
	return normalized
}

// CodexExecCommand returns the shell command lines a Codex code-mode program
// passes as `cmd` or `command` object properties, joined with newlines. It
// scans the source as JavaScript tokens, so a "cmd:" inside a string literal,
// a comment or a regular expression (a file body the program writes, say) is
// not a command, and only an object key counts: `cond ? cmd : "x"` and
// `case "cmd":` are not. The value is a string literal, or an argv array of
// them (`command: ["bash", "-lc", script]`, whose script is the command).
// Values built at run time (template literals with substitutions, variables,
// concatenations) have no static text and are skipped.
//
// A command that does not parse as bash on its own is dropped too. Readers
// parse the joined text as one script, and one unparsable command would make
// the whole text unparsable and hide its siblings (a `git commit` among them).
func CodexExecCommand(source string) string {
	tokens := tokenize(source)
	var commands []string
	for i := 1; i+2 < len(tokens); i++ {
		key, colon := tokens[i], tokens[i+1]
		if !key.isKey() || (key.value != "cmd" && key.value != "command") || !colon.isPunct(":") {
			continue
		}
		if before := tokens[i-1]; !before.isPunct("{") && !before.isPunct(",") {
			continue
		}
		if command, ok := commandValue(tokens, i+2); ok && parsesAsBash(command) {
			commands = append(commands, command)
		}
	}
	return strings.Join(commands, "\n")
}

// parsesAsBash reports whether command is a whole bash script, as the
// command readers (gitevidence, prreview) parse it.
func parsesAsBash(command string) bool {
	_, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	return err == nil
}

// commandValue reads the command a property value at tokens[i] holds.
func commandValue(tokens []token, i int) (string, bool) {
	first := tokens[i]
	switch {
	case first.kind == tokString:
		if !first.static || first.value == "" {
			return "", false
		}
		// "a" + b, "a" + "b": built at run time, so no static command.
		if i+1 < len(tokens) && tokens[i+1].isOperator("+") {
			return "", false
		}
		return first.value, true
	case first.isPunct("["):
		var argv []string
		for j := i + 1; j < len(tokens); j += 2 {
			if tokens[j].kind != tokString || !tokens[j].static || j+1 >= len(tokens) {
				return "", false
			}
			argv = append(argv, tokens[j].value)
			switch {
			case tokens[j+1].isPunct("]"):
				return argvCommand(argv)
			case !tokens[j+1].isPunct(","):
				return "", false
			}
		}
	}
	return "", false
}

// argvCommand turns an argv into the command line it runs: the script of a
// `bash -lc script` call, else the words joined.
func argvCommand(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	if len(argv) >= 3 && isShellCommand(argv[0], argv[1]) {
		return argv[2], argv[2] != ""
	}
	return strings.Join(argv, " "), true
}

func isShellCommand(executable, flags string) bool {
	switch path.Base(executable) {
	case "bash", "sh", "zsh":
		return strings.HasPrefix(flags, "-") && strings.Contains(strings.TrimLeft(flags, "-"), "c")
	}
	return false
}

type tokenKind int

const (
	tokIdent tokenKind = iota
	tokString
	tokRegex
	tokPunct
	tokOperator
)

type token struct {
	kind   tokenKind
	text   string // punctuation and operators
	value  string // identifier name or decoded string
	static bool
}

func (t token) isKey() bool {
	return (t.kind == tokIdent || t.kind == tokString) && t.static
}
func (t token) isPunct(text string) bool    { return t.kind == tokPunct && t.text == text }
func (t token) isOperator(text string) bool { return t.kind == tokOperator && t.text == text }

// regexKeywords are the identifiers after which a slash starts a regular
// expression rather than dividing.
var regexKeywords = map[string]bool{
	"return": true, "typeof": true, "case": true, "in": true, "of": true, "delete": true,
	"void": true, "throw": true, "new": true, "await": true, "yield": true, "else": true, "do": true,
}

// tokenize splits JavaScript source into identifiers, string literals,
// regular expressions, punctuation and operators. Whitespace and comments are
// dropped. A leading sentinel punctuation token lets a reader look one token
// back without a bounds check. It is approximate (no automatic semicolons,
// no template nesting beyond ${} skipping) and made to keep quotes inside
// strings, comments and regular expressions from desynchronising the scan.
func tokenize(source string) []token {
	tokens := []token{{kind: tokPunct, text: ";"}}
	for pos := 0; pos < len(source); {
		c := source[pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			pos++
		case c == '/' && pos+1 < len(source) && source[pos+1] == '/':
			for pos < len(source) && source[pos] != '\n' {
				pos++
			}
		case c == '/' && pos+1 < len(source) && source[pos+1] == '*':
			end := strings.Index(source[pos+2:], "*/")
			if end < 0 {
				return tokens
			}
			pos += 2 + end + 2
		case c == '"' || c == '\'' || c == '`':
			value, static, next := scanString(source, pos)
			tokens = append(tokens, token{kind: tokString, value: value, static: static})
			pos = next
		case c == '/' && regexAllowed(tokens[len(tokens)-1]):
			if next, ok := scanRegex(source, pos); ok {
				tokens = append(tokens, token{kind: tokRegex})
				pos = next
				break
			}
			tokens = append(tokens, token{kind: tokOperator, text: "/"})
			pos++
		case isIdentByte(c):
			start := pos
			for pos < len(source) && isIdentByte(source[pos]) {
				pos++
			}
			word := source[start:pos]
			tokens = append(tokens, token{kind: tokIdent, text: word, value: word, static: true})
		case strings.IndexByte("{}[]():,;.", c) >= 0:
			tokens = append(tokens, token{kind: tokPunct, text: string(c)})
			pos++
		default:
			_, size := utf8.DecodeRuneInString(source[pos:])
			tokens = append(tokens, token{kind: tokOperator, text: source[pos : pos+size]})
			pos += size
		}
	}
	return tokens
}

// regexAllowed reports whether a slash after prev starts a regular expression.
func regexAllowed(prev token) bool {
	switch prev.kind {
	case tokIdent:
		return regexKeywords[prev.value]
	case tokString, tokRegex:
		return false
	case tokPunct:
		return prev.text != ")" && prev.text != "]"
	}
	return true
}

// maxRegexLength bounds the scan for a regular expression's close, so a long
// line of slashes is not rescanned from each one.
const maxRegexLength = 1000

// scanRegex reads the regular expression literal opening at source[pos] and
// returns the position after its flags. A literal that does not close on its
// line is not one.
func scanRegex(source string, pos int) (int, bool) {
	inClass := false
	for i := pos + 1; i < len(source) && i < pos+maxRegexLength; i++ {
		switch source[i] {
		case '\n':
			return 0, false
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			i++
			for i < len(source) && isIdentByte(source[i]) {
				i++
			}
			return i, true
		}
	}
	return 0, false
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// scanString reads the string literal opening at source[pos] and returns its
// decoded value, whether the value is fully static (a template literal with a
// ${...} substitution is not), and the position after the closing quote.
func scanString(source string, pos int) (string, bool, int) {
	quote := source[pos]
	var out strings.Builder
	static := true
	i := pos + 1
	for i < len(source) {
		c := source[i]
		switch {
		case c == quote:
			return out.String(), static, i + 1
		case c == '\\':
			i = decodeEscape(source, i, &out)
		case quote == '`' && c == '$' && i+1 < len(source) && source[i+1] == '{':
			static = false
			i = skipSubstitution(source, i+2)
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), false, len(source)
}

// skipSubstitution moves past a template literal's ${...} body, which may
// itself hold braces and strings.
func skipSubstitution(source string, i int) int {
	depth := 1
	for i < len(source) && depth > 0 {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '"', '\'', '`':
			_, _, next := scanString(source, i)
			i = next
			continue
		}
		i++
	}
	return i
}

// decodeEscape appends the character the JavaScript escape at source[i] (a
// backslash) stands for and returns the position after it.
func decodeEscape(source string, i int, out *strings.Builder) int {
	if i+1 >= len(source) {
		return i + 1
	}
	c := source[i+1]
	i += 2
	switch c {
	case 'n':
		out.WriteByte('\n')
	case 't':
		out.WriteByte('\t')
	case 'r':
		out.WriteByte('\r')
	case 'b':
		out.WriteByte('\b')
	case 'f':
		out.WriteByte('\f')
	case 'v':
		out.WriteByte('\v')
	case '0':
		out.WriteByte(0)
	case '\n':
	case '\r':
		if i < len(source) && source[i] == '\n' {
			i++
		}
	case 'x':
		if i+2 <= len(source) {
			if n, err := strconv.ParseUint(source[i:i+2], 16, 8); err == nil {
				out.WriteRune(rune(n))
				return i + 2
			}
		}
		out.WriteByte('x')
	case 'u':
		return decodeUnicodeEscape(source, i, out)
	default:
		out.WriteByte(c)
	}
	return i
}

func decodeUnicodeEscape(source string, i int, out *strings.Builder) int {
	if i < len(source) && source[i] == '{' {
		// At most six hex digits and the brace: a missing close must not
		// rescan the rest of the program.
		if end := strings.IndexByte(source[i:min(len(source), i+9)], '}'); end > 0 {
			if n, err := strconv.ParseUint(source[i+1:i+end], 16, 32); err == nil {
				out.WriteRune(rune(n))
				return i + end + 1
			}
		}
		out.WriteByte('u')
		return i
	}
	r, next, ok := hex4(source, i)
	if !ok {
		out.WriteByte('u')
		return i
	}
	if r >= 0xD800 && r < 0xDC00 && strings.HasPrefix(source[next:], `\u`) {
		if low, after, ok := hex4(source, next+2); ok && low >= 0xDC00 && low < 0xE000 {
			out.WriteRune(0x10000 + (r-0xD800)<<10 + (low - 0xDC00))
			return after
		}
	}
	out.WriteRune(r)
	return next
}

func hex4(source string, i int) (rune, int, bool) {
	if i+4 > len(source) {
		return 0, i, false
	}
	n, err := strconv.ParseUint(source[i:i+4], 16, 32)
	if err != nil {
		return 0, i, false
	}
	return rune(n), i + 4, true
}
