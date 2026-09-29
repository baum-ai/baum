package cursorsession

import "github.com/lastpersonlabs/baum/sessions/session"

func toolVerb(name string) session.ToolVerb {
	switch name {
	case "read_file_v2", "read_file":
		return session.VerbRead
	case "edit_file_v2", "search_replace", "write", "apply_patch":
		return session.VerbEdit
	case "ripgrep_raw_search", "grep", "glob_file_search", "codebase_search", "semantic_search_full", "web_search", "web_fetch":
		return session.VerbSearch
	case "run_terminal_command_v2", "run_terminal_cmd":
		return session.VerbExecute
	default:
		return session.VerbOther
	}
}
