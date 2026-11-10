//go:build !windows

package hookcli

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

func TestAgentHookEventLocatesTheTerminalOnStartAndPrompts(t *testing.T) {
	paths := useTestHookDaemon(t)
	old := captureTerminal
	captureTerminal = func() *hookevents.Terminal {
		return &hookevents.Terminal{TTY: "/dev/pts/3", Env: map[string]string{"TMUX_PANE": "%1"}, Processes: []hookevents.Process{{PID: 7, Start: "9", Name: "claude"}}}
	}
	t.Cleanup(func() { captureTerminal = old })
	for _, event := range []string{"session-start", "pre-task", "user-prompt-submit", "stop"} {
		if _, _, err := runCLIWithInput(t, `{"session_id":"s-1","cwd":"/work"}`, "hooks", "agent", "claude-code", event); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
		time.Sleep(time.Millisecond)
	}
	events, _ := spooledHookEvents(t, paths)
	located := map[string]bool{}
	for _, e := range events {
		located[e.Name] = e.Terminal != nil && e.Terminal.TTY == "/dev/pts/3" && e.Terminal.Env["TMUX_PANE"] == "%1"
	}
	if want := map[string]bool{"session-start": true, "pre-task": false, "user-prompt-submit": true, "stop": false}; !maps.Equal(located, want) {
		t.Errorf("located = %v, want %v", located, want)
	}
}

func TestGitHookEventNamesItsAncestorsOnCommitAndPush(t *testing.T) {
	paths := useTestHookDaemon(t)
	old := captureAncestors
	captureAncestors = func() []hookevents.Process {
		return []hookevents.Process{{PID: 9, Start: "1", Name: "git"}, {PID: 7, Start: "9", Name: "claude"}}
	}
	oldCommit := captureCommit
	captureCommit = func(context.Context) (string, string) { return strings.Repeat("c", 40), "fix" }
	t.Cleanup(func() { captureAncestors, captureCommit = old, oldCommit })
	for _, args := range [][]string{{"post-commit"}, {"pre-push", "origin", "https://github.com/acme/demo.git"}} {
		if _, _, err := runCLIWithInput(t, "", append([]string{"hooks", "git"}, args...)...); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
	}
	events, _ := spooledHookEvents(t, paths)
	named := map[string]bool{}
	for _, e := range events {
		named[e.Name] = e.Terminal != nil && len(e.Terminal.Processes) == 2 && e.Terminal.Processes[1].Name == "claude" &&
			e.Terminal.TTY == "" && len(e.Terminal.Env) == 0
		// A commit says which it is and its branch, as the hook read them.
		recorded := e.Git != nil && e.Git.Head == strings.Repeat("c", 40) && e.Git.Branch == "fix"
		if recorded != (e.Name == "post-commit") {
			t.Errorf("%s: git = %+v", e.Name, e.Git)
		}
	}
	if want := map[string]bool{"post-commit": true, "pre-push": true}; !maps.Equal(named, want) {
		t.Errorf("named = %v, want %v", named, want)
	}
}
