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
	script, ok := staticWord(call.Args[2])
	return ok && IsCommitCommand(script)
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

// CommitSHAs extracts commit hashes from `git commit` stdout and from a
// following `git log --oneline` used to expose the result of a quiet commit.
func CommitSHAs(stdout string) []string {
	var shas []string
	for _, m := range commitSHARe.FindAllStringSubmatch(stdout, -1) {
		shas = append(shas, m[1])
	}
	if m := onelineSHARe.FindStringSubmatch(stdout); m != nil {
		shas = append(shas, m[1])
	}
	return shas
}
