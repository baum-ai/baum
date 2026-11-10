//go:build !windows

package hookcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitEventsNameTheSessionFromTheAgentEnvironment(t *testing.T) {
	const id = "7f3c1a2e-0b44-4c3e-9d11-5a6b7c8d9e0f"
	for name, tc := range map[string]struct{ value, want string }{
		"session":  {id, id},
		"empty":    {"", ""},
		"unsafe":   {"abc/../def", ""},
		"space":    {"abc def", ""},
		"too long": {strings.Repeat("a", maxSessionID+1), ""},
	} {
		t.Run(name, func(t *testing.T) {
			paths := useTestHookDaemon(t)
			t.Setenv("CLAUDE_CODE_SESSION_ID", tc.value)
			t.Setenv("CLAUDECODE", "1")
			if _, _, err := runCLI(t, "hooks", "git", "post-commit"); err != nil {
				t.Fatal(err)
			}
			events, raw := spooledHookEvents(t, paths)
			if len(events) != 1 {
				t.Fatalf("events=%+v", events)
			}
			e := events[0]
			if tc.want == "" {
				if e.Agent != "" || e.Session != nil {
					t.Fatalf("event names %q %+v", e.Agent, e.Session)
				}
				return
			}
			if e.Kind != "git" || e.Agent != "claude-code" || e.Session == nil || e.Session.ID != tc.want {
				t.Fatalf("event=%+v session=%+v", e, e.Session)
			}
			if bytes.Contains(raw[0], []byte("CLAUDECODE")) {
				t.Fatal("other environment was spooled")
			}
		})
	}
}

func TestPostRewriteKeepsItsRewrittenCommits(t *testing.T) {
	paths := useTestHookDaemon(t)
	input := "1111111111111111111111111111111111111111 2222222222222222222222222222222222222222\n" +
		"3333333333333333333333333333333333333333 4444444444444444444444444444444444444444 extra\n"
	if _, _, err := runCLIWithInput(t, input, "hooks", "git", "post-rewrite", "rebase"); err != nil {
		t.Fatal(err)
	}
	events, _ := spooledHookEvents(t, paths)
	if len(events) != 1 || events[0].Name != "post-rewrite" || events[0].Git == nil ||
		len(events[0].Git.Args) != 1 || events[0].Git.Args[0] != "rebase" || len(events[0].Git.Lines) != 2 {
		t.Fatalf("events=%+v", events)
	}
}

func TestDispatcherCheckFollowsDesktopAndRepository(t *testing.T) {
	paths := useTestHookDaemon(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if _, _, err := runCLI(t, "hooks", "git", "--check-dispatcher", "post-commit"); err == nil {
		t.Fatal("dispatcher admitted before the desktop app ran")
	}
	markDaemonRan(t, paths)
	for _, hook := range []string{"post-commit", "post-rewrite", "pre-push"} {
		if _, _, err := runCLI(t, "hooks", "git", "--check-dispatcher", hook); err != nil {
			t.Errorf("%s declined: %v", hook, err)
		}
	}
	if _, _, err := runCLI(t, "hooks", "git", "--check-dispatcher", "bogus"); err == nil {
		t.Error("unknown hook admitted")
	}
	if _, _, err := runCLI(t, "hooks", "git", "--check", "post-commit"); err == nil {
		t.Error("legacy check admitted")
	}
}
