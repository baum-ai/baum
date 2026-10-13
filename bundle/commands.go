package bundle

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// CommandOutcome maps a tool result's status to a command event's closed
// outcome vocabulary (succeeded, failed, interrupted, unknown). An empty
// status, a tool use with no recorded result, is "unknown".
func CommandOutcome(status string) string {
	switch status {
	case "ok":
		return "succeeded"
	case "error":
		return "failed"
	case "interrupted":
		return "interrupted"
	default:
		return "unknown"
	}
}

// CommandText reads the shell command line from a tool use's raw input JSON,
// trying the "command" and "cmd" keys different vendors use
// (sessions/codexsession/tool_results.go tries the same pair). A command value
// may be a string or a string array (argv-style vendors); both are normalised
// to one shell-syntax string. The text is for ParseCommandParaphrase only and
// must never enter a bundle.
func CommandText(input []byte) string {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(input, &probe); err != nil {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		raw, ok := probe[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		var parts []string
		if err := json.Unmarshal(raw, &parts); err == nil {
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// commandFirstArgCategories and commandCategories are the closed category
// table: test, build, lint, migrate, git, package, other. Keyed by program
// base name, with a sub-table for CLIs whose first argument decides the
// category.
var commandFirstArgCategories = map[string]map[string]string{
	"go":     {"test": "test", "vet": "lint", "build": "build", "install": "build", "get": "package", "mod": "package"},
	"npm":    {"test": "test", "build": "build", "install": "package", "add": "package", "ci": "package", "lint": "lint", "run": "other"},
	"yarn":   {"test": "test", "build": "build", "install": "package", "add": "package", "lint": "lint"},
	"pnpm":   {"test": "test", "build": "build", "install": "package", "add": "package", "lint": "lint"},
	"cargo":  {"test": "test", "build": "build", "add": "package", "install": "package"},
	"docker": {"build": "build"},
	"pip":    {"install": "package"},
	"pip3":   {"install": "package"},
}

var commandCategories = map[string]string{
	"git": "git", "gh": "git",
	"pytest": "test", "jest": "test", "vitest": "test", "mocha": "test", "rspec": "test",
	"golangci-lint": "lint", "eslint": "lint", "oxlint": "lint", "biome": "lint", "ruff": "lint", "flake8": "lint", "gofmt": "lint",
	"make": "build",
	"just": "build",
}

// categorize reads the category of a program, by its base name, from its
// arguments.
func categorize(base string, args []string) string {
	if table, ok := commandFirstArgCategories[base]; ok {
		if firstArg := firstNonFlagArg(args); firstArg != "" {
			if category, ok := table[firstArg]; ok {
				return category
			}
		}
	}
	if category, ok := commandCategories[base]; ok {
		return category
	}
	if strings.Contains(base, "migrate") {
		return "migrate"
	}
	return "other"
}

func firstNonFlagArg(args []string) string {
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// firstRepositoryPathArg returns the first argument that looks like a
// repository-relative path, skipping flags, non-path words such as a
// subcommand keyword ("test", "build"), and the value of a flag that carries
// code, a message or a credential (textValueFlag).
func firstRepositoryPathArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if textValueFlag(args[i]) {
			i++
			continue
		}
		if looksLikeRepositoryPath(args[i]) {
			return args[i]
		}
	}
	return ""
}

// codeOrMessageFlags take code to run or a message as their value
// (sh -c, python -c, node -e, perl -e, git commit -m, ...), which is never a
// path to keep.
var codeOrMessageFlags = map[string]bool{
	"-c": true, "--command": true, "-e": true, "--eval": true, "--exec": true, "--execute": true,
	"-m": true, "--message": true, "-p": true, "--print": true, "-r": true, "--run": true,
}

// textValueFlag reports whether arg is a flag whose value is code, a message
// or a credential: one of codeOrMessageFlags, or a flag naming a password,
// token, secret, key or credential.
func textValueFlag(arg string) bool {
	if codeOrMessageFlags[arg] {
		return true
	}
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	name := strings.ToLower(arg)
	for _, word := range []string{"pass", "token", "secret", "key", "auth", "cred"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

// looksLikeRepositoryPath reports whether arg is strictly a
// repository-relative path, the only kind of target a command event keeps. It
// must be local (not absolute, not escaping through ".."), contain a "/" or a
// file extension, and hold only path characters: whitespace, quotes, "=",
// "@", ":", "\" and shell metacharacters mark a word that is (or may hide)
// code, a message, an assignment, a remote or a credential, so such a word is
// dropped rather than guessed at, even where it could be a legitimate file
// name.
func looksLikeRepositoryPath(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "/") || !safeWord(arg) {
		return false
	}
	if !filepath.IsLocal(arg) {
		return false
	}
	base := path.Base(arg)
	return strings.Contains(arg, "/") || (strings.Contains(base, ".") && strings.Trim(base, ".") != "")
}

// unsafeWordRunes are the characters a kept program name or target never
// holds: quoting, assignment, remote and drive syntax, Windows separators and
// shell metacharacters.
const unsafeWordRunes = "\"'`=@:\\$;&|<>(){}[]*?!#~%^,"

// safeWord reports whether word holds only printable characters that are
// neither whitespace nor unsafeWordRunes.
func safeWord(word string) bool {
	for _, r := range word {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) || strings.ContainsRune(unsafeWordRunes, r) {
			return false
		}
	}
	return true
}

// programName is a command word's base name across "/" and "\\" separators,
// or "" when the word names a directory (ends in a separator), is dynamic
// (wordLiteral gave ""), or is not a plain name, so no path fragment or other
// raw text becomes the program.
func programName(word string) string {
	if i := strings.LastIndexAny(word, "/\\"); i >= 0 {
		word = word[i+1:]
	}
	if word == "" || strings.Trim(word, ".") == "" || !safeWord(word) {
		return ""
	}
	return word
}

// ParseCommandParaphrase extracts one entry per simple command from a possibly
// compound or piped command line. Raw command lines never survive past this
// function's return: it returns only program base name, category and an
// optional repository-relative target.
func ParseCommandParaphrase(command string) []CommandEntry {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	var entries []CommandEntry
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		args := make([]string, len(call.Args))
		for i, word := range call.Args {
			args[i] = wordLiteral(word)
		}
		// A command whose program is dynamic or not a plain name is left out:
		// nothing about it can be told without its raw text.
		program := programName(args[0])
		if program == "" {
			return true
		}
		entry := CommandEntry{Program: program, Category: categorize(program, args[1:])}
		entry.Target = firstRepositoryPathArg(args[1:])
		entries = append(entries, entry)
		return true
	})
	return entries
}

// wordLiteral reconstructs a shell word's literal text when every part is
// static (a literal or a quoted literal); it returns "" for a word carrying
// a substitution, parameter expansion or other dynamic content, the same
// conservative approach linkage/gitevidence's staticWord takes.
func wordLiteral(word *syntax.Word) string {
	var text strings.Builder
	var appendParts func([]syntax.WordPart) bool
	appendParts = func(parts []syntax.WordPart) bool {
		for _, part := range parts {
			switch part := part.(type) {
			case *syntax.Lit:
				text.WriteString(part.Value)
			case *syntax.SglQuoted:
				text.WriteString(part.Value)
			case *syntax.DblQuoted:
				if !appendParts(part.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !appendParts(word.Parts) {
		return ""
	}
	return text.String()
}
