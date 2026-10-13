package bundle

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

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

func categorize(program string, args []string) string {
	base := path.Base(program)
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
// repository-relative path, skipping flags and non-path words such as a
// subcommand keyword ("test", "build").
func firstRepositoryPathArg(args []string) string {
	for _, a := range args {
		if looksLikeRepositoryPath(a) {
			return a
		}
	}
	return ""
}

// looksLikeRepositoryPath reports whether arg is plausibly a
// repository-relative path, the only kind of target a command event keeps:
// local (not absolute, not a flag, not escaping through ".." with either
// separator), not a URL, scp/rsync remote or Windows drive path, and
// containing a "/" or a file extension. "@", ":" and "\\" are valid in
// repository filenames, so only their structural use is rejected.
func looksLikeRepositoryPath(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") {
		return false
	}
	if hasRemotePrefix(arg) || !filepath.IsLocal(arg) || !filepath.IsLocal(strings.ReplaceAll(arg, "\\", "/")) {
		return false
	}
	return strings.Contains(arg, "/") || strings.Contains(path.Base(arg), ".")
}

// hasRemotePrefix reports whether arg starts with URL, scp/rsync or Windows
// drive syntax: a ":" in its first path component ("https://host/x",
// "user:token@host/x", "host:dir/file", "C:\\Users"). Git applies the same
// rule to tell scp-style remotes from local paths.
func hasRemotePrefix(arg string) bool {
	first, _, _ := strings.Cut(strings.ReplaceAll(arg, "\\", "/"), "/")
	return strings.Contains(first, ":")
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
		program := path.Base(args[0])
		if program == "" {
			return true
		}
		entry := CommandEntry{Program: program, Category: categorize(args[0], args[1:])}
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
