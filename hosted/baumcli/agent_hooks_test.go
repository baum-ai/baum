//go:build !windows

package baumcli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

func TestAgentHookPassesPayloadOutputAndExitStatusThrough(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" >&2\ncat\nexit 2\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := runCLIWithInput(t, `{"session_id":"abc"}`, "hooks", "agent", "claude-code", "stop")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("err = %v, want exit status 2", err)
	}
	if stdout != `{"session_id":"abc"}` {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "hooks claude-code stop" {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestAgentHookCheckAcceptsOnlyKnownAgentEvents(t *testing.T) {
	// Logged in, so the answer depends only on the agent and event.
	t.Setenv(tokenEnvVar, "token")
	tests := []struct {
		agent   string
		event   string
		wantErr bool
	}{
		{agent: "claude-code", event: "post-todo"},
		{agent: "claude-code", event: "subagent-stop"},
		{agent: "codex", event: "post-tool-use"},
		{agent: "cursor", event: "subagent-stop"},
		// Entire 0.11.2 gives Codex these three. A dispatcher checks before it
		// execs, so a binary that does not know them must say so.
		{agent: "codex", event: "session-end"},
		{agent: "codex", event: "subagent-start"},
		{agent: "codex", event: "subagent-stop"},
		// Events belong to one agent; Cursor's prompt hook is not Claude Code's.
		{agent: "claude-code", event: "before-submit-prompt", wantErr: true},
		{agent: "codex", event: "post-todo", wantErr: true},
		{agent: "gemini", event: "stop", wantErr: true},
	}
	for _, test := range tests {
		_, _, err := runCLI(t, "hooks", "agent", "--check", test.agent, test.event)
		if (err != nil) != test.wantErr {
			t.Errorf("--check %s %s: err = %v", test.agent, test.event, err)
		}
	}
	if _, _, err := runCLI(t, "hooks", "agent", "--check", "claude-code"); err == nil {
		t.Error("--check without an event was accepted")
	}
}

func TestAgentHookRejectsUnknownAgentEvent(t *testing.T) {
	_, _, err := runCLI(t, "hooks", "agent", "claude-code", "frobnicate")
	if err == nil || !strings.Contains(err.Error(), `unsupported claude-code hook event "frobnicate"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentHookFallsBackWhenEntireIsMissing(t *testing.T) {
	withoutEntire(t)
	// Outside a repository the session-start bootstrap has nothing to install,
	// which keeps this test off the network.
	t.Chdir(t.TempDir())

	tests := []struct {
		agent   string
		event   string
		message string
	}{
		{agent: "claude-code", event: "stop"},
		{agent: "claude-code", event: "session-start", message: agentMissingNote(t, "claude-code")},
		{agent: "codex", event: "session-start", message: agentMissingNote(t, "codex")},
		// Cursor's committed session start says nothing when Entire is absent.
		{agent: "cursor", event: "session-start"},
	}
	for _, test := range tests {
		stdout, _, err := runCLI(t, "hooks", "agent", test.agent, test.event)
		if err != nil {
			t.Fatalf("%s %s: %v", test.agent, test.event, err)
		}
		if test.message == "" {
			if stdout != "" {
				t.Errorf("%s %s: stdout = %q, want silence", test.agent, test.event, stdout)
			}
			continue
		}
		var payload struct {
			SystemMessage string `json:"systemMessage"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("%s %s: stdout %q: %v", test.agent, test.event, stdout, err)
		}
		if payload.SystemMessage != test.message {
			t.Errorf("%s %s: systemMessage = %q", test.agent, test.event, payload.SystemMessage)
		}
	}
}

func TestAgentHookBootstrapsGitHooksOnSessionStart(t *testing.T) {
	repo := initHookTestRepo(t)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	t.Chdir(repo)
	// Only a hook of a user opted in to the hosted service bootstraps.
	t.Setenv(tokenEnvVar, "token")

	if _, _, err := runCLI(t, "hooks", "agent", "claude-code", "session-start"); err != nil {
		t.Fatalf("session start: %v", err)
	}
	for _, name := range entireGitHookNames() {
		contents := readHookTestFile(t, filepath.Join(repo, ".git", "hooks", name))
		if !strings.Contains(contents, baumHookMarker) {
			t.Errorf("%s hook = %q", name, contents)
		}
	}
}

func TestOwnEntireAgentHooksRewritesOnlyEntireCommands(t *testing.T) {
	root := t.TempDir()
	settings := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "sh -c 'if ! command -v entire >/dev/null 2>&1; then printf \"%s\\n\" \"{\\\"systemMessage\\\":\\\"\\\\n\\\\nEntire CLI is enabled but not installed or not on PATH.\\\\nInstallation guide: https://docs.entire.io/cli/installation#installation-methods\\\"}\"; exit 0; fi; exec entire hooks claude-code session-start'"
          },
          {
            "type": "command",
            "command": "./scripts/greet.sh"
          }
        ]
      }
    ]
  },
  "permissions": {
    "deny": [
      "Read(./.entire/metadata/**)"
    ]
  }
}
`
	writeAgentSettings(t, root, ".claude/settings.json", settings)

	changed, err := ownEntireAgentHooks(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != ".claude/settings.json" {
		t.Fatalf("changed = %v", changed)
	}
	got := readHookTestFile(t, filepath.Join(root, ".claude", "settings.json"))
	spec, _ := lookupAgentHookSpec("claude-code")
	want, err := agenthooks.EncodeJSON(agenthooks.Dispatcher(spec, "session-start"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, string(want)) {
		t.Errorf("rewritten settings = %s", got)
	}
	if !strings.Contains(got, `"command": "./scripts/greet.sh"`) {
		t.Errorf("foreign hook command was rewritten: %s", got)
	}
	if !strings.Contains(got, `"Read(./.entire/metadata/**)"`) || !strings.Contains(got, `"matcher": "",`) {
		t.Errorf("unrelated settings were not preserved: %s", got)
	}

	changed, err = ownEntireAgentHooks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("second run changed %v", changed)
	}
}

// TestSetupOwnsPreRenameAgentHooks covers a checkout set up before the product
// was renamed to baum. Its hooks must count as owned, or setup would ask Entire
// to add the agent again and every event would run twice; and setup rewrites
// them to the baum dispatcher.
func TestSetupOwnsPreRenameAgentHooks(t *testing.T) {
	root := t.TempDir()
	legacy := strings.ReplaceAll(`sh -c 'if command -v {old} >/dev/null 2>&1 && {old} hooks agent --check cursor stop >/dev/null 2>&1; then exec {old} hooks agent cursor stop; fi; if ! command -v entire >/dev/null 2>&1; then exit 0; fi; exec entire hooks cursor stop'`, "{old}", "recount"+"able")
	encoded, err := agenthooks.EncodeJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	writeAgentSettings(t, root, ".cursor/hooks.json", `{"hooks":{"stop":[{"command":`+string(encoded)+`}]}}`)

	if !agentHooksOwned(root, "cursor") {
		t.Fatal("pre-rename cursor hooks were not recognised as owned")
	}
	changed, err := ownEntireAgentHooks(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != ".cursor/hooks.json" {
		t.Fatalf("changed = %v", changed)
	}
	spec, _ := lookupAgentHookSpec("cursor")
	want, err := agenthooks.EncodeJSON(agenthooks.Dispatcher(spec, "stop"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, filepath.Join(root, ".cursor", "hooks.json")); got != `{"hooks":{"stop":[{"command":`+string(want)+`}]}}` {
		t.Errorf("rewritten hooks = %s", got)
	}
}

func TestOwnEntireAgentHooksSkipsMissingAndInvalidFiles(t *testing.T) {
	root := t.TempDir()
	changed, err := ownEntireAgentHooks(root)
	if err != nil || len(changed) != 0 {
		t.Fatalf("empty repository: changed = %v, err = %v", changed, err)
	}
	writeAgentSettings(t, root, ".cursor/hooks.json", "{not json")
	if _, err := ownEntireAgentHooks(root); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

// TestAgentHookDispatcherIsFrozen guards the exact commands onboarding writes
// into a repository. They stay in customer repositories indefinitely and the
// onboarding pull request templates embed them, so a change here is a change to
// every repository already onboarded.
func TestAgentHookDispatcherIsFrozen(t *testing.T) {
	claudeCode, _ := lookupAgentHookSpec("claude-code")
	cursor, _ := lookupAgentHookSpec("cursor")
	tests := []struct {
		spec  agentHookSpec
		event string
		want  string
	}{
		{
			spec:  claudeCode,
			event: "session-start",
			want:  `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check claude-code session-start >/dev/null 2>&1; then exec baum hooks agent claude-code session-start; fi; if ! command -v entire >/dev/null 2>&1; then printf "%s\n" "{\"systemMessage\":\"\\n\\nEntire CLI is enabled but not installed or not on PATH.\\nInstallation guide: https://docs.entire.io/cli/installation#installation-methods\"}"; exit 0; fi; exec entire hooks claude-code session-start'`,
		},
		{
			spec:  cursor,
			event: "subagent-stop",
			want:  `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check cursor subagent-stop >/dev/null 2>&1; then exec baum hooks agent cursor subagent-stop; fi; if ! command -v entire >/dev/null 2>&1; then exit 0; fi; exec entire hooks cursor subagent-stop'`,
		},
	}
	for _, test := range tests {
		if got := agenthooks.Dispatcher(test.spec, test.event); got != test.want {
			t.Errorf("%s %s dispatcher =\n%s\nwant\n%s", test.spec.Slug, test.event, got, test.want)
		}
	}
}

func TestOwnedAgentHookCommandIgnoresUnrelatedCommands(t *testing.T) {
	for _, command := range []string{
		"./scripts/greet.sh",
		"entire hooks claude-code stop",
		"sh -c 'exec entire hooks gemini SessionStart'",
		// A repository wrapping the hook in work of its own: rewriting this to
		// the bare dispatcher would silently drop run-my-check.
		"sh -c 'run-my-check; entire hooks codex stop'",
		"sh -c 'if ! command -v entire >/dev/null 2>&1; then exit 0; fi; run-my-check; exec entire hooks codex stop'",
		"sh -c 'if ! command -v entire >/dev/null 2>&1; then exit 0; fi; exec entire hooks codex stop; run-my-check'",
	} {
		if _, _, ok := agenthooks.OwnedCommand(command); ok {
			t.Errorf("%q was treated as an owned hook", command)
		}
	}
}

// TestOwnedAgentHookCommandMatchesEntireAndBaumCommands pins what setup
// is allowed to replace: Entire's own command, whose missing-Entire branch a
// later release may reword, and a command already dispatching through
// baum.
func TestOwnedAgentHookCommandMatchesEntireAndBaumCommands(t *testing.T) {
	codex, _ := lookupAgentHookSpec("codex")
	tests := []string{
		agenthooks.EntireCommand(codex, "session-start"),
		agenthooks.Dispatcher(codex, "session-start"),
		`sh -c 'if ! command -v entire >/dev/null 2>&1; then printf "%s\n" "a reworded notice"; exit 0; fi; exec entire hooks codex session-start'`,
	}
	for _, command := range tests {
		spec, event, ok := agenthooks.OwnedCommand(command)
		if !ok || spec.Slug != "codex" || event != "session-start" {
			t.Errorf("%q => %q %q %v", command, spec.Slug, event, ok)
		}
	}
}

// TestCodexMissingEntireNoticeIsFrozen pins the payload Codex prints when Entire
// is absent. The dispatcher freeze below only covers Claude Code's notice and a
// silent Cursor event, so a Codex reword would otherwise pass unnoticed.
func TestCodexMissingEntireNoticeIsFrozen(t *testing.T) {
	spec, ok := lookupAgentHookSpec("codex")
	if !ok {
		t.Fatal("no hook spec for codex")
	}
	const want = `{"systemMessage":"Entire CLI is enabled but not installed or not on PATH. Installation guide: https://docs.entire.io/cli/installation#installation-methods"}`
	if got := missingEntireNotice(spec, "session-start"); got != want {
		t.Errorf("codex missing notice =\n%s\nwant\n%s", got, want)
	}
}

func writeAgentSettings(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// systemBinaryPath keeps git and the shell reachable while a test controls what
// else the PATH contains.
const systemBinaryPath = "/usr/bin:/bin"

// withoutEntire hides Entire from both the PATH and the directory this CLI
// installs it into.
func withoutEntire(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+systemBinaryPath)
	original := entireInstallDir
	t.Cleanup(func() { entireInstallDir = original })
	entireInstallDir = t.TempDir()
}

// stubEntireBinary puts a fake Entire on the PATH and keeps the real install
// directory out of the way.
func stubEntireBinary(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "entire"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	original := entireInstallDir
	t.Cleanup(func() { entireInstallDir = original })
	entireInstallDir = t.TempDir()
	return bin
}

// agentMissingNote is the notice one agent's session start prints when Entire
// is absent. Claude Code's literal is pinned by TestAgentHookDispatcherIsFrozen
// and Codex's by TestCodexMissingEntireNoticeIsFrozen.
func agentMissingNote(t *testing.T, agent string) string {
	t.Helper()
	spec, ok := lookupAgentHookSpec(agent)
	if !ok {
		t.Fatalf("no hook spec for %s", agent)
	}
	return spec.MissingNote
}
