package agenthooks

import (
	"bytes"
	"fmt"
)

// Placement says where the user-wide hook for one event goes in an agent's
// settings: the agent's own hook event, the tool matcher of its group and the
// handler timeout in seconds. A nil Matcher writes no matcher key; zero
// Timeout writes no timeout. For Entire's events they mirror what `entire
// enable` writes for the pinned release (the onboarding templates hold a
// captured copy).
type Placement struct {
	Event     string
	HookEvent string
	Matcher   *string
	Timeout   int
	// Async runs the hook in the background (Claude Code's "async"), so a
	// hook that fires on every tool call never makes the agent wait for it.
	Async bool
	// RecordOnly is an event Entire has no hook for, which only the daemon
	// reads (a live session waiting on its user, or working again): baum
	// records it and never hands it to Entire. No committed hook file
	// carries one, so the committed vocabulary and the hosted onboarding
	// stay as they are.
	RecordOnly bool
}

// UserWide describes the agent events a user-scoped marketplace plugin runs,
// and project hook files that take precedence over those hooks.
type UserWide struct {
	Placements   []Placement
	ProjectFiles []string
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
			{Event: "notification", HookEvent: "Notification", Matcher: matcher(""), RecordOnly: true},
			// Every tool call, so a session that waited for a permission is
			// seen working again once it was given: the hooks above report
			// only subagent and todo tools.
			{Event: "post-tool-use", HookEvent: "PostToolUse", Matcher: matcher(""), Async: true, RecordOnly: true},
		},
		ProjectFiles: []string{".claude/settings.json", ".claude/settings.local.json"},
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
			// Codex is about to ask its user to approve a command. The hook
			// prints nothing, so it decides nothing and Codex asks as usual.
			{Event: "permission-request", HookEvent: "PermissionRequest", Timeout: 30, RecordOnly: true},
		},
		ProjectFiles: []string{".codex/hooks.json", ".codex/config.toml"},
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

// ValidateUserWide is Validate for a user-wide hook, which runs the
// record-only events too.
func ValidateUserWide(agent, event string) error {
	if RecordOnly(agent, event) {
		return nil
	}
	return Validate(agent, event)
}

// RecordOnly reports whether a user-wide hook's event is one baum only
// records (Placement.RecordOnly).
func RecordOnly(agent, event string) bool {
	p, ok := UserWidePlacement(agent, event)
	return ok && p.RecordOnly
}

// UserDispatcher renders the hook command baum installs in an agent's
// marketplace plugin for one event. Like the committed Dispatcher it runs
// baum only when `--check` succeeds, but with `--user`, and it never falls
// through to Entire: a repository whose own settings run Entire for the
// event already gets it from them, and one without them never asked for it.
// Without baum, or with a baum too old to know `--user`, it does nothing.
// The run carries `--user` too, so a hook admitted only because the user has
// a baum daemon records its event and runs no Entire.
//
// Agents remember a user's trust in a hook by the exact command (Codex), so
// keep this string stable and change what the hook does in the binary.
func UserDispatcher(spec Spec, event string) string {
	return userPrefix(spec.Slug, event) + `>/dev/null 2>&1; then exec baum hooks agent --user ` + spec.Slug + " " + event + `; fi'`
}

func userPrefix(agent, event string) string {
	return `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user ` + agent + " " + event + " "
}

// HandlesEvent reports whether a project's agent hook file runs Entire for
// this agent event: Entire's own command and baum's committed dispatcher
// end in `entire hooks AGENT EVENT`, and so does a command
// a repository wrapped in work of its own. The text is searched rather than
// parsed so that a JSON file and a TOML file are read alike, and an
// unreadable file still counts.
func HandlesEvent(contents []byte, spec Spec, event string) bool {
	needle := []byte(fmt.Sprintf("entire hooks %s %s", spec.Slug, event))
	for offset := 0; ; {
		index := bytes.Index(contents[offset:], needle)
		if index < 0 {
			return false
		}
		end := offset + index + len(needle)
		if end == len(contents) || !isEventByte(contents[end]) {
			return true
		}
		offset = end
	}
}

func isEventByte(c byte) bool {
	return c == '-' || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9')
}
