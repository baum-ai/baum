package claudesession

import "github.com/lastpersonlabs/baum/sessions/session"

// verbs is the Claude tool name → our verb table.
var verbs = map[string]session.ToolVerb{
	"Read":         session.VerbRead,
	"NotebookRead": session.VerbRead,

	"Edit":         session.VerbEdit,
	"Write":        session.VerbEdit,
	"MultiEdit":    session.VerbEdit,
	"NotebookEdit": session.VerbEdit,

	"Grep":           session.VerbSearch,
	"Glob":           session.VerbSearch,
	"WebSearch":      session.VerbSearch,
	"WebFetch":       session.VerbSearch,
	"SemanticSearch": session.VerbSearch,

	"Bash": session.VerbExecute,
}

// verbFor classifies a tool call.
func verbFor(name string, spawned bool) session.ToolVerb {
	if spawned {
		return session.VerbSpawnSubagent
	}
	if verb, known := verbs[name]; known {
		return verb
	}
	return session.VerbOther
}
