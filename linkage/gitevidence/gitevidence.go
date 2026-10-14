// Package gitevidence recognises git activity in text an agent captured — a
// command it ran, the output it got back. Pure functions over strings, no I/O;
// review it as data, like a price table.
//
// It lives on its own because two parse paths need the same answer and must not
// drift: the legacy trace adapters and the session ingest both decide from this
// whether a session committed anything.
package gitevidence

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

var (
	commitSHARe  = regexp.MustCompile(`\[[^\s\]]+ ([0-9a-f]{7,40})\]`)
	onelineSHARe = regexp.MustCompile(`(?m)^([0-9a-f]{7,40})[ \t]+\S.*$`)
)

// IsCommitCommand reports whether a shell command appears to run git commit.
func IsCommitCommand(command string) bool {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	commits := false
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || commits {
			return !commits
		}
		commits = isGitCommit(call) || isShellCommit(call)
		return !commits
	})
	return commits
}

func isShellCommit(call *syntax.CallExpr) bool {
	if !isShellScript(call) {
		return false
	}
	script, ok := staticWord(call.Args[2])
	return ok && IsCommitCommand(script)
}

// isShellScript reports whether call is `bash|sh|zsh -c <script>`.
func isShellScript(call *syntax.CallExpr) bool {
	if len(call.Args) < 3 {
		return false
	}
	executable := call.Args[0].Lit()
	switch path.Base(executable) {
	case "bash", "sh", "zsh":
	default:
		return false
	}
	flags := call.Args[1].Lit()
	if !strings.HasPrefix(flags, "-") || !strings.Contains(strings.TrimLeft(flags, "-"), "c") {
		return false
	}
	return true
}

func staticWord(word *syntax.Word) (string, bool) {
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
		return "", false
	}
	return text.String(), true
}

func isGitCommit(call *syntax.CallExpr) bool {
	if len(call.Args) < 2 || call.Args[0].Lit() != "git" {
		return false
	}
	args := call.Args[1:]
	for len(args) > 0 {
		option := args[0].Lit()
		if option == "" || !strings.HasPrefix(option, "-") {
			break
		}
		args = args[1:]
		if gitGlobalOptionTakesArgument(option) && len(args) > 0 {
			args = args[1:]
		}
	}
	if len(args) == 0 || args[0].Lit() != "commit" {
		return false
	}
	for _, arg := range args[1:] {
		if arg.Lit() == "--dry-run" {
			return false
		}
	}
	return true
}

func gitGlobalOptionTakesArgument(option string) bool {
	switch option {
	case "-c", "-C", "--config-env", "--exec-path", "--git-dir", "--namespace", "--work-tree":
		return true
	default:
		return false
	}
}

// TrustsOnelineLog reports whether a trailing `git log --oneline` in command
// only runs after a successful commit: `git log` follows `git commit` through
// `&&` alone. Newline, `;` and `||` continue when the commit fails (a
// rejecting hook, nothing to commit), so the SHA `git log` prints is then the
// previous HEAD's.
func TrustsOnelineLog(command string) bool {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	trusted := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if trusted {
			return false
		}
		switch node := node.(type) {
		case *syntax.BinaryCmd:
			if node.Op == syntax.AndStmt && ranThroughAnd(node.X, isGitCommit) && ranThroughAnd(node.Y, isGitLog) {
				trusted = true
			}
		case *syntax.CallExpr:
			if isShellScript(node) {
				script, ok := staticWord(node.Args[2])
				trusted = ok && TrustsOnelineLog(script)
			}
		}
		return !trusted
	})
	return trusted
}

// ranThroughAnd reports whether node is, or is an `&&` chain containing, a
// call matching match: the chain only completes when that call succeeded. A
// `||`, `;`, pipeline, negation or background command does not qualify.
func ranThroughAnd(node syntax.Node, match func(*syntax.CallExpr) bool) bool {
	switch node := node.(type) {
	case *syntax.Stmt:
		return !node.Negated && !node.Background && !node.Coprocess && node.Cmd != nil && ranThroughAnd(node.Cmd, match)
	case *syntax.BinaryCmd:
		return node.Op == syntax.AndStmt && (ranThroughAnd(node.X, match) || ranThroughAnd(node.Y, match))
	case *syntax.CallExpr:
		return match(node)
	}
	return false
}

func isGitLog(call *syntax.CallExpr) bool {
	if len(call.Args) < 2 || call.Args[0].Lit() != "git" {
		return false
	}
	for _, arg := range call.Args[1:] {
		if arg.Lit() == "log" {
			return true
		}
	}
	return false
}

// CommitSHAs extracts commit hashes from `git commit` stdout and, when
// trustOneline is set (see TrustsOnelineLog), from a following
// `git log --oneline` used to expose the result of a quiet commit.
func CommitSHAs(stdout string, trustOneline bool) []string {
	var shas []string
	for _, m := range commitSHARe.FindAllStringSubmatch(stdout, -1) {
		shas = append(shas, m[1])
	}
	if !trustOneline {
		return shas
	}
	if m := onelineSHARe.FindStringSubmatch(stdout); m != nil {
		shas = append(shas, m[1])
	}
	return shas
}
