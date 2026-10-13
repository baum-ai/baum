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

// firstRepositoryPathArg returns the first argument that is a known
// repository path: path-shaped (looksLikeRepositoryPath) and, normalised by
// normalizeTarget, accepted by knownPath. Flags, non-path words such as a
// subcommand keyword ("test", "build"), and the value of a flag that carries
// code, a message or a credential (textValueFlag) are skipped. A nil
// knownPath accepts nothing.
func firstRepositoryPathArg(args []string, knownPath func(string) bool) string {
	if knownPath == nil {
		return ""
	}
	for i := 0; i < len(args); i++ {
		if textValueFlag(args[i]) {
			i++
			continue
		}
		if !looksLikeRepositoryPath(args[i]) {
			continue
		}
		if target, ok := normalizeTarget(args[i]); ok && knownPath(TargetPath(target)) {
			return target
		}
	}
	return ""
}

// normalizeTarget spells a path-shaped argument as a repository-relative
// path: cleaned, without a leading "./", and with a trailing Go package
// pattern "/..." kept. The repository root itself and a path that escapes it
// are not targets.
func normalizeTarget(arg string) (string, bool) {
	pattern := strings.HasSuffix(arg, "/...")
	cleaned := path.Clean(strings.TrimSuffix(arg, "/..."))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	if pattern {
		return cleaned + "/...", true
	}
	return cleaned, true
}

// TargetPath is the repository path a command target names: the target
// without a trailing Go package pattern "/...".
func TargetPath(target string) string {
	return strings.TrimSuffix(target, "/...")
}

// KnownPaths returns a knownPath predicate for ParseCommandParaphrase that
// accepts each of paths and every directory above it, but not the repository
// root. Paths are cleaned and a leading "./" is ignored.
func KnownPaths(paths ...string) func(string) bool {
	known := map[string]bool{}
	for _, p := range paths {
		for p = path.Clean(p); p != "." && p != "/" && p != "" && !strings.HasPrefix(p, "../") && p != ".."; p = path.Dir(p) {
			known[p] = true
		}
	}
	return func(candidate string) bool { return known[path.Clean(candidate)] }
}

// KeepKnownCommandTargets clears every command target of sessions that
// knownPath does not accept and that is not one of the session's own touched
// paths or a directory above one, for a host that paraphrases commands before
// the paths it knows are all read.
func KeepKnownCommandTargets(sessions []Session, knownPath func(string) bool) {
	for i := range sessions {
		var touched []string
		for _, prompt := range sessions[i].Prompts {
			touched = append(touched, prompt.TouchedPaths...)
		}
		touchedPath := KnownPaths(touched...)
		for e := range sessions[i].CommandEvents {
			for c := range sessions[i].CommandEvents[e].Commands {
				entry := &sessions[i].CommandEvents[e].Commands[c]
				if entry.Target == "" {
					continue
				}
				if p := TargetPath(entry.Target); (knownPath == nil || !knownPath(p)) && !touchedPath(p) {
					entry.Target = ""
				}
			}
		}
	}
}

// codeOrMessageFlags take code to run or a message as their value
// (sh -c, python -c, node -e, perl -e, git commit -m, ...), which is never a
// path to keep.
var codeOrMessageFlags = map[string]bool{
	"-c": true, "--command": true, "-e": true, "--eval": true, "--exec": true, "--execute": true,
	"-m": true, "--message": true, "-p": true, "--print": true, "-r": true, "--run": true,
}

// textValueFlag reports whether arg is a flag whose value is code, a message
// or a credential: one of codeOrMessageFlags, a cluster of two or three short
// flags ending in one of them ("-am", "-cm"; a longer single-dash word is a
// long flag such as go's -race), or a flag naming a password, token, secret,
// key or credential.
func textValueFlag(arg string) bool {
	if codeOrMessageFlags[arg] {
		return true
	}
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	if cluster := arg[1:]; len(cluster) >= 2 && len(cluster) <= 3 && strings.Trim(cluster, "abcdefghijklmnopqrstuvwxyz") == "" &&
		strings.ContainsRune("cem", rune(cluster[len(cluster)-1])) {
		return true
	}
	name := strings.ToLower(arg)
	for _, word := range []string{"pass", "token", "secret", "key", "auth", "cred"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

// looksLikeRepositoryPath reports whether arg is path-shaped, the first test
// a target passes before knownPath is asked. It must be local (not absolute,
// not escaping through ".."), contain a "/" or a file extension, and hold
// only path characters: whitespace, quotes, "=", "@", ":", "\" and shell
// metacharacters mark a word that is (or may hide) code, a message, an
// assignment, a remote or a credential, so such a word is dropped rather than
// guessed at, even where it could be a legitimate file name.
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
// optional target. The target is the first argument that is path-shaped and,
// normalised, a path knownPath accepts: positive evidence it is a path of the
// repository, such as a changed file, a touched path or a file in the
// checkout. With a nil knownPath no entry has a target.
func ParseCommandParaphrase(command string, knownPath func(string) bool) []CommandEntry {
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
		entry.Target = firstRepositoryPathArg(args[1:], knownPath)
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
