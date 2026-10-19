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
	write(t, dir, "ssh", "#!/bin/sh\necho \"args: $*\" >> '"+record+"'\necho \"agent: $SSH_AUTH_SOCK\" >> '"+record+"'\nexit 255\n", 0o755)
	return path, record
}

// A background fetch over SSH never waits for a prompt: the configured ssh
// command gets BatchMode=yes appended and only the agent socket the daemon
// vouches for.
func TestBackgroundFetchOverSSHIsNonInteractive(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(t *testing.T, clone, ssh string)
	}{
		{"core.sshCommand", func(t *testing.T, clone, ssh string) {
			git(t, clone, "config", "core.sshCommand", ssh+" -o Custom=yes")
		}},
		{"GIT_SSH_COMMAND", func(t *testing.T, _, ssh string) { t.Setenv("GIT_SSH_COMMAND", ssh+" -o Custom=yes") }},
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
			for _, want := range []string{"-o Custom=yes -o BatchMode=yes", "agent: /live/agent.sock"} {
				if !strings.Contains(got, want) {
					t.Errorf("ssh saw %q, want %q", got, want)
				}
			}
			if strings.Contains(got, "/stale/") {
				t.Errorf("stale agent socket passed: %q", got)
			}
		})
	}
}

func TestBackgroundEnvWithoutAgentOrSSHSettings(t *testing.T) {
	b := &Background{SSHAgent: func(context.Context) string { return "" }}
	env := b.env(context.Background(), t.TempDir(), []string{"PATH=/usr/bin", "SSH_AUTH_SOCK=/stale", "SSH_ASKPASS_REQUIRE=force", "GIT_SSH=/opt/my ssh"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_SSH_COMMAND='/opt/my ssh' -o BatchMode=yes", "SSH_ASKPASS_REQUIRE=never"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "SSH_AUTH_SOCK") || strings.Contains(joined, "=force") {
		t.Errorf("kept:\n%s", joined)
	}
}
