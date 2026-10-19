//go:build unix

package gitlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSSH records its arguments and SSH_AUTH_SOCK, then fails as ssh does
// when it cannot authenticate.
func fakeSSH(t *testing.T) (path, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	path = filepath.Join(dir, "ssh")
	write(t, dir, "ssh", "#!/bin/sh\necho \"args: $*\" >> '"+record+"'\necho \"agent: $SSH_AUTH_SOCK\" >> '"+record+"'\necho \"askpass: $SSH_ASKPASS_REQUIRE\" >> '"+record+"'\nexit 255\n", 0o755)
	return path, record
}

// A background fetch over SSH never waits for a prompt and only gets the
// agent socket the daemon vouches for; a configured ssh command runs as the
// user wrote it, and plain ssh gets BatchMode=yes.
func TestBackgroundFetchOverSSHIsNonInteractive(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(t *testing.T, clone, ssh string)
		args      string
	}{
		{"core.sshCommand", func(t *testing.T, clone, ssh string) {
			git(t, clone, "config", "core.sshCommand", ssh+" -o Custom=yes")
		}, "args: -o Custom=yes"},
		{"GIT_SSH_COMMAND", func(t *testing.T, _, ssh string) { t.Setenv("GIT_SSH_COMMAND", ssh+" -o Custom=yes") }, "args: -o Custom=yes"},
		{"plain ssh", func(t *testing.T, _, ssh string) {
			// Put the fake first on PATH as "ssh".
			t.Setenv("PATH", filepath.Dir(ssh)+string(os.PathListSeparator)+os.Getenv("PATH"))
		}, "-o BatchMode=yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			ssh, record := fakeSSH(t)
			git(t, f.clone, "remote", "set-url", "origin", "ssh://git@example.invalid/acme/demo.git")
			tc.configure(t, f.clone, ssh)
			t.Setenv("SSH_AUTH_SOCK", "/stale/agent.sock")
			SetBackground(&Background{SSHAgent: func(context.Context) string { return "/live/agent.sock" }})
			t.Cleanup(func() { SetBackground(nil) })
			r := open(t, f.clone, Options{})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := r.ResolvePull(ctx, PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}); err == nil {
				t.Fatal("fetch through a failing ssh succeeded")
			}
			if ctx.Err() != nil {
				t.Fatal("fetch waited")
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatal("ssh never ran:", err)
			}
			got := string(raw)
			for _, want := range []string{tc.args, "agent: /live/agent.sock", "askpass: never"} {
				if !strings.Contains(got, want) {
					t.Errorf("ssh saw %q, want %q", got, want)
				}
			}
			if tc.name != "plain ssh" && strings.Contains(got, "BatchMode") {
				t.Errorf("changed the configured ssh command: %q", got)
			}
			if strings.Contains(got, "/stale/") {
				t.Errorf("stale agent socket passed: %q", got)
			}
		})
	}
}

func TestBackgroundEnvAsksForTheAgentOnlyForSSH(t *testing.T) {
	asked := 0
	b := &Background{SSHAgent: func(context.Context) string { asked++; return "/live" }}
	env := strings.Join(b.env(context.Background(), t.TempDir(), []string{"PATH=/usr/bin", "SSH_AUTH_SOCK=/stale", "SSH_ASKPASS_REQUIRE=force"}, false), "\n")
	if asked != 0 || strings.Contains(env, "SSH_AUTH_SOCK") || strings.Contains(env, "GIT_SSH_COMMAND") || !strings.Contains(env, "SSH_ASKPASS_REQUIRE=never") || strings.Contains(env, "=force") {
		t.Fatalf("asked %d:\n%s", asked, env)
	}
	env = strings.Join(b.env(context.Background(), t.TempDir(), []string{"GIT_SSH=/opt/plink"}, true), "\n")
	if asked != 1 || !strings.Contains(env, "SSH_AUTH_SOCK=/live") || strings.Contains(env, "BatchMode") || !strings.Contains(env, "GIT_SSH=/opt/plink") {
		t.Fatalf("GIT_SSH:\n%s", env)
	}
}

func TestSSHRemote(t *testing.T) {
	for url, want := range map[string]bool{
		"ssh://git@github.com/a/b": true, "git+ssh://h/x": true, "git@github.com:a/b.git": true, "github.com:a/b": true,
		"https://github.com/a/b": false, "/srv/repo.git": false, "./repo": false, `C:\repos\x`: false, "file:///srv/x": false, "": true,
	} {
		if got := sshRemote(url); got != want {
			t.Errorf("sshRemote(%q) = %v", url, got)
		}
	}
}
