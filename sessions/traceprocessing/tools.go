package traceprocessing

import (
	"bufio"
	"regexp"
	"strings"

	"github.com/baum-ai/baum/sessions/session"
)

func GenericToolVerb(name string) session.ToolVerb {
	switch name {
	case "Read", "ReadFile", "ListDir", "ReadLints", "NotebookRead":
		return session.VerbRead
	case "Edit", "Write", "NotebookEdit", "apply_patch", "StrReplace", "ApplyPatch", "EditNotebook", "Delete":
		return session.VerbEdit
	case "Grep", "Glob", "WebSearch", "WebFetch", "ToolSearch", "rg", "Codebase":
		return session.VerbSearch
	case "Bash", "BashOutput", "exec", "exec_command", "write_stdin", "run", "Shell", "AwaitShell":
		return session.VerbExecute
	default:
		return session.VerbOther
	}
}

func GenericTouchKind(verb session.ToolVerb) (session.TouchKind, bool) {
	switch verb {
	case session.VerbRead:
		return session.TouchRead, true
	case session.VerbEdit:
		return session.TouchEdit, true
	default:
		return "", false
	}
}

var patchFileHeader = regexp.MustCompile(`^\*\*\* (?:(?:Add|Update|Delete) File:|Move to:) (.+)$`)

func PatchHeaderPaths(patch string) []string {
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(patch))
	for scanner.Scan() {
		if match := patchFileHeader.FindStringSubmatch(scanner.Text()); len(match) == 2 {
			paths = append(paths, match[1])
		}
	}
	return paths
}
