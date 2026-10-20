package agenthooks

import (
	"bytes"
	"fmt"
	"regexp"
)

// Placement says where the user-wide hook for one Entire event goes in an
// agent's settings: the agent's own hook event, the tool matcher of its group
// and the handler timeout in seconds. A nil Matcher writes no matcher key; zero
// Timeout writes no timeout. They mirror what `entire enable` writes for the
// pinned release (the onboarding templates hold a captured copy).
type Placement struct {
	Event     string
	HookEvent string
	Matcher   *string
	Timeout   int
}

// UserWide describes an agent's user-wide hook settings: the file below the
// home directory that holds them, where each event goes, and every file in a
// project that can carry hooks of the agent's own, which the user-wide hooks
// yield to.
type UserWide struct {
	SettingsFile string
	Placements   []Placement
	ProjectFiles []string
}

func matcher(value string) *string { return &value }

var userWide = map[string]UserWide{
	"claude-code": {
		SettingsFile: ".claude/settings.json",
		Placements: []Placement{
			{Event: "session-start", HookEvent: "SessionStart", Matcher: matcher("")},
			{Event: "user-prompt-submit", HookEvent: "UserPromptSubmit", Matcher: matcher("")},
			{Event: "pre-task", HookEvent: "PreToolUse", Matcher: matcher("Agent")},
			{Event: "post-task", HookEvent: "PostToolUse", Matcher: matcher("Agent")},
			{Event: "post-todo", HookEvent: "PostToolUse", Matcher: matcher("TaskCreate|TaskUpdate")},
			{Event: "stop", HookEvent: "Stop", Matcher: matcher("")},
			{Event: "session-end", HookEvent: "SessionEnd", Matcher: matcher("")},
			{Event: "subagent-stop", HookEvent: "SubagentStop", Matcher: matcher("")},
		},
		ProjectFiles: []string{".claude/settings.json", ".claude/settings.local.json"},
	},
	"codex": {
		// Codex also reads inline [hooks] tables from config.toml, but the
		// JSON file keeps baum's edits away from the TOML the user writes
		// by hand, and Codex merges both.
		SettingsFile: ".codex/hooks.json",
		Placements: []Placement{
			{Event: "session-start", HookEvent: "SessionStart", Timeout: 30},
			{Event: "user-prompt-submit", HookEvent: "UserPromptSubmit", Timeout: 30},
			{Event: "post-tool-use", HookEvent: "PostToolUse", Timeout: 30},
			{Event: "stop", HookEvent: "Stop", Timeout: 30},
			{Event: "session-end", HookEvent: "SessionEnd", Timeout: 3},
			{Event: "subagent-start", HookEvent: "SubagentStart", Timeout: 30},
			{Event: "subagent-stop", HookEvent: "SubagentStop", Timeout: 30},
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

// UserDispatcher renders the hook command baum installs in an agent's
// user-wide settings for one event. Like the committed Dispatcher it runs
// baum only when `--check` succeeds, but with `--user`, and it never falls
// through to Entire: a repository whose own settings run Entire for the
// event already gets it from them, and one without them never asked for it.
// Without baum, or with a baum too old to know `--user`, it does nothing.
//
// Agents remember a user's trust in a hook by the exact command (Codex), so
// keep this string stable and change what the hook does in the binary.
func UserDispatcher(spec Spec, event string) string {
	return userPrefix(spec.Slug, event) + `>/dev/null 2>&1; then exec baum hooks agent ` + spec.Slug + " " + event + `; fi'`
}

func userPrefix(agent, event string) string {
	return `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user ` + agent + " " + event + " "
}

var userDispatcherPattern = regexp.MustCompile(`^sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user ([a-z0-9-]+) ([a-z0-9-]+) `)

// UserDispatcherEvent reports the agent and event of a user-wide hook command
// baum installed, of this or an earlier form, so that install replaces and
// uninstall removes it. The event is returned even when this baum no longer
// knows it.
func UserDispatcherEvent(command string) (agent, event string, ok bool) {
	match := userDispatcherPattern.FindStringSubmatch(command)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// HandlesEvent reports whether a project's agent hook file runs Entire for
// this agent event: Entire's own command, baum's committed dispatcher and the
// pre-rename one all end in `entire hooks AGENT EVENT`, and so does a command
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
