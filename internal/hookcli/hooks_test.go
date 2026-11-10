//go:build !windows

package hookcli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agenthookdefs"
	"github.com/baum-ai/baum/internal/daemon"
)

type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("hook attempted an HTTP request")
	return nil, errors.New("no network")
}

func markDaemonRan(t *testing.T, paths daemon.Paths) {
	t.Helper()
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LockFile(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHooksIgnoreHostedOptInsAndIndependentEntire(t *testing.T) {
	paths := useTestHookDaemon(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("BAUM_TOKEN", "legacy-token")
	t.Setenv("BAUM_SERVER", "http://must-not-contact.invalid")
	writeAgentSettings(t, repo, ".git/baum/setup-complete", "")
	writeAgentSettings(t, repo, ".claude/settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"entire hooks claude-code stop"}]}]}}`)
	writeAgentSettings(t, repo, ".entire/settings.json", `{"enabled":true}`)
	writeAgentSettings(t, os.Getenv("XDG_CONFIG_HOME"), "baum/credentials.json", `{"token":"legacy-token"}`)
	bin := t.TempDir()
	called := filepath.Join(bin, "called")
	writeExecutable(t, filepath.Join(bin, "entire"), "#!/bin/sh\ntouch '"+called+"'\nexit 7\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	transport := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = transport })
	check := []string{"hooks", "agent", "--check", "--user", "claude-code", "stop"}
	if _, _, err := runCLI(t, check...); err == nil {
		t.Fatal("legacy hosted credentials/marker enabled a hook")
	}
	markDaemonRan(t, paths)
	if _, _, err := runCLI(t, check...); err != nil {
		t.Fatalf("independent Entire suppressed local hook: %v", err)
	}
	if stdout, stderr, err := runCLIWithInput(t, `{"session_id":"s-1","prompt":"private prompt"}`, "hooks", "agent", "--user", "claude-code", "stop"); err != nil || stdout != "" || stderr != "" {
		t.Fatalf("hook output: %q %q %v", stdout, stderr, err)
	}
	events, raw := spooledHookEvents(t, paths)
	if len(events) != 1 || events[0].Session == nil || events[0].Session.ID != "s-1" {
		t.Fatalf("events=%+v", events)
	}
	if bytes.Contains(raw[0], []byte("private prompt")) {
		t.Fatal("prompt was spooled")
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("hook executed Entire")
	}
	for _, path := range []string{".entire/settings.json", ".claude/settings.json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	if _, _, err := runCLI(t, check...); err == nil {
		t.Fatal("user hook admitted outside Git worktree")
	}
}

func TestLegacyRepositoryChecksDeclineAfterDesktopHasRun(t *testing.T) {
	paths := useTestHookDaemon(t)
	markDaemonRan(t, paths)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	writeAgentSettings(t, repo, ".entire/settings.json", `{"enabled":true}`)
	for _, agent := range []string{"claude-code", "codex"} {
		settings, _ := agenthookdefs.LookupUserWide(agent)
		for _, placement := range settings.Placements {
			t.Run(agent+"/"+placement.Event, func(t *testing.T) {
				// Repository dispatchers exec baum on success, otherwise their own fallback.
				if _, _, err := runCLI(t, "hooks", "agent", "--check", agent, placement.Event); err == nil {
					t.Fatal("legacy dispatcher would intercept the independent hook")
				}
				if _, _, err := runCLI(t, "hooks", "agent", "--check", "--user", agent, placement.Event); err != nil {
					t.Fatalf("desktop plugin declined: %v", err)
				}
			})
		}
	}
	for _, event := range []string{"post-commit", "pre-push"} {
		t.Run("git/"+event, func(t *testing.T) {
			if _, _, err := runCLI(t, "hooks", "git", "--check", event); err == nil {
				t.Fatal("legacy Git dispatcher would intercept the independent hook")
			}
		})
	}
	if events, _ := spooledHookEvents(t, paths); len(events) != 0 {
		t.Fatalf("admission checks recorded events: %+v", events)
	}
}

func TestGitEventsAreLocalAndOmitRemoteCredentials(t *testing.T) {
	paths := useTestHookDaemon(t)
	input := "refs/heads/main 1111111111111111111111111111111111111111 refs/heads/main 2222222222222222222222222222222222222222\n"
	url := "https://secret@example.com/acme/repo"
	if stdout, stderr, err := runCLIWithInput(t, input, "hooks", "git", "pre-push", "origin", url); err != nil || stdout != "" || stderr != "" {
		t.Fatalf("output: %q %q %v", stdout, stderr, err)
	}
	events, raw := spooledHookEvents(t, paths)
	if len(events) != 1 || events[0].Git == nil || len(events[0].Git.Lines) != 1 || len(events[0].Git.Args) != 1 || events[0].Git.Args[0] != "origin" || events[0].Exit != 0 {
		t.Fatalf("events=%+v", events)
	}
	if bytes.Contains(raw[0], []byte("secret")) {
		t.Fatal("remote credential was spooled")
	}
	if git := gitHookEvent([]string{"pre-push", url, url}, nil); git != nil {
		t.Fatalf("URL retained as remote name: %+v", git)
	}
}

func TestStoppedDaemonIsNotRestartedByHook(t *testing.T) {
	paths := useTestHookDaemon(t)
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.MarkStopped(paths); err != nil {
		t.Fatal(err)
	}
	spawned := false
	hookDaemonSpawn = func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		spawned = true
		return nil, errors.New("should not spawn")
	}
	if _, _, err := runCLIWithInput(t, `{"session_id":"s"}`, "hooks", "agent", "--user", "codex", "session-start"); err != nil {
		t.Fatal(err)
	}
	if spawned {
		t.Fatal("hook restarted stopped daemon")
	}
	events, _ := spooledHookEvents(t, paths)
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
}

func TestUnknownHooksAndBootstrapAreRejected(t *testing.T) {
	for _, args := range [][]string{{"hooks", "bootstrap"}, {"hooks", "agent", "--user", "codex", "bogus"}, {"hooks", "git", "bogus"}} {
		if _, _, err := runCLI(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestIncompleteInputDoesNotHoldHook(t *testing.T) {
	useTestHookDaemon(t)
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	var stdout, stderr strings.Builder
	if err := runContext(t.Context(), []string{"hooks", "agent", "--user", "codex", "session-start"}, reader, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatal("ordinary hook printed output")
	}
}
