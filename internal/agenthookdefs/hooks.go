// Package agenthookdefs defines baum's local coding-agent plugin events.
package agenthookdefs

import "fmt"

type Placement struct {
	Event     string
	HookEvent string
	Matcher   *string
	Timeout   int
	// Async runs the hook in the background (Claude Code's "async"), so a
	// hook that fires on every tool call never makes the agent wait for it.
	Async bool
}

// UserWide describes the events a user-scoped marketplace plugin runs.
type UserWide struct {
	Placements []Placement
}

func matcher(value string) *string { return &value }

var userWide = map[string]UserWide{
	"claude-code": {
		Placements: []Placement{
			{Event: "session-start", HookEvent: "SessionStart", Matcher: matcher("")},
			{Event: "user-prompt-submit", HookEvent: "UserPromptSubmit", Matcher: matcher("")},
			{Event: "pre-task", HookEvent: "PreToolUse", Matcher: matcher("Agent")},
			{Event: "post-task", HookEvent: "PostToolUse", Matcher: matcher("Agent")},
			{Event: "post-todo", HookEvent: "PostToolUse", Matcher: matcher("TaskCreate|TaskUpdate")},
			{Event: "stop", HookEvent: "Stop", Matcher: matcher("")},
			{Event: "session-end", HookEvent: "SessionEnd", Matcher: matcher("")},
			{Event: "subagent-stop", HookEvent: "SubagentStop", Matcher: matcher("")},
			// Claude Code asks for a permission or an answer through its
			// notifications.
			{Event: "notification", HookEvent: "Notification", Matcher: matcher("")},
			// Every tool call, so a session that waited for a permission is
			// seen working again once it was given: the hooks above report
			// only subagent and todo tools.
			{Event: "post-tool-use", HookEvent: "PostToolUse", Matcher: matcher(""), Async: true},
			// A tool call that failed, which PostToolUse does not report: the
			// session works on, and a permission the user gave in the
			// terminal for it is answered.
			{Event: "post-tool-use-failure", HookEvent: "PostToolUseFailure", Matcher: matcher(""), Async: true},
			// Claude Code asks for a permission or an answer: the hook holds
			// until the app answers, the request is withdrawn (the terminal,
			// which asks alongside, answered first) or the timeout kills it,
			// which leaves the terminal's dialog as it was.
			{Event: "permission-request", HookEvent: "PermissionRequest", Matcher: matcher(""), Timeout: 3600},
		},
	},
	"codex": {
		Placements: []Placement{
			{Event: "session-start", HookEvent: "SessionStart", Timeout: 30},
			{Event: "user-prompt-submit", HookEvent: "UserPromptSubmit", Timeout: 30},
			{Event: "post-tool-use", HookEvent: "PostToolUse", Timeout: 30},
			{Event: "stop", HookEvent: "Stop", Timeout: 30},
			{Event: "session-end", HookEvent: "SessionEnd", Timeout: 3},
			{Event: "subagent-start", HookEvent: "SubagentStart", Timeout: 30},
			{Event: "subagent-stop", HookEvent: "SubagentStop", Timeout: 30},
			// Codex is about to ask its user to approve a command. Unless the
			// user lets the app answer Codex (agentrequests.Settings), the hook
			// prints nothing, so it decides nothing and Codex asks as usual;
			// this definition stays as it is, so the user's trust in it stands.
			{Event: "permission-request", HookEvent: "PermissionRequest", Timeout: 30},
		},
	},
}

// LookupUserWide returns an agent's user-wide hook settings. Only Claude Code
// and Codex have them.
func LookupUserWide(agent string) (UserWide, bool) {
	settings, ok := userWide[agent]
	return settings, ok
}

// UserWidePlacement returns the placement of an agent's user-wide hook for an
// event.
func UserWidePlacement(agent, event string) (Placement, bool) {
	for _, p := range userWide[agent].Placements {
		if p.Event == event {
			return p, true
		}
	}
	return Placement{}, false
}

// ValidateUserWide accepts only events shipped by baum's local plugins.
func ValidateUserWide(agent, event string) error {
	if _, ok := UserWidePlacement(agent, event); !ok {
		return fmt.Errorf("unsupported %s hook event %q", agent, event)
	}
	return nil
}

// UserDispatcher is the command in baum's marketplace plugins. Keep it stable
// because coding agents remember trust by the exact command string.
func UserDispatcher(agent string, event string) string {
	return userPrefix(agent, event) + `>/dev/null 2>&1; then exec baum hooks agent --user ` + agent + " " + event + `; fi'`
}

func userPrefix(agent, event string) string {
	return `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user ` + agent + " " + event + " "
}
