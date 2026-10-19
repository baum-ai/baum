package gitlocal

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

// Background makes fetches safe for a process nobody watches (the baum
// daemon): no command may wait for an answer. Git already runs with
// GIT_TERMINAL_PROMPT=0; with a Background, SSH also runs with
// BatchMode=yes, appended to the ssh command the user configured, and never
// asks through SSH_ASKPASS, so a missing key or unknown host is a clear git
// error rather than a hang.
type Background struct {
	// SSHAgent returns the SSH agent socket a fetch may use, or "" for none.
	// A socket that no longer exists must not be returned.
	SSHAgent func(ctx context.Context) string
}

var background atomic.Pointer[Background]

// SetBackground applies b to every later fetch in this process; nil removes
// it.
func SetBackground(b *Background) { background.Store(b) }

// sshConfigTimeout bounds reading core.sshCommand.
const sshConfigTimeout = 5 * time.Second

// env adjusts a fetch's environment in dir for b.
func (b *Background) env(ctx context.Context, dir string, env []string) []string {
	out := make([]string, 0, len(env)+3)
	var sshCommand, gitSSH string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "SSH_AUTH_SOCK", "SSH_ASKPASS_REQUIRE":
			continue
		case "GIT_SSH_COMMAND":
			sshCommand = value
			continue
		case "GIT_SSH":
			gitSSH = value
		}
		out = append(out, kv)
	}
	if b.SSHAgent != nil {
		if socket := b.SSHAgent(ctx); socket != "" {
			out = append(out, "SSH_AUTH_SOCK="+socket)
		}
	}
	// The command git would run: GIT_SSH_COMMAND, core.sshCommand, GIT_SSH,
	// then ssh. Setting GIT_SSH_COMMAND overrides the others, so it starts
	// from whichever applied.
	if sshCommand == "" {
		ctx, cancel := context.WithTimeout(ctx, sshConfigTimeout)
		configured, err := run(ctx, runSpec{dir: dir, env: env, limit: 4096, timeout: sshConfigTimeout}, "config", "--get", "core.sshCommand")
		cancel()
		if err == nil {
			sshCommand = strings.TrimSpace(string(configured))
		}
	}
	if sshCommand == "" && gitSSH != "" {
		sshCommand = "'" + strings.ReplaceAll(gitSSH, "'", `'\''`) + "'"
	}
	if sshCommand == "" {
		sshCommand = "ssh"
	}
	return append(out, "GIT_SSH_COMMAND="+sshCommand+" -o BatchMode=yes", "SSH_ASKPASS_REQUIRE=never")
}

// fetchEnv is the environment of a fetch in the clone at dir.
func fetchEnv(ctx context.Context, dir string) []string {
	env := userEnv()
	if b := background.Load(); b != nil {
		return b.env(ctx, dir, env)
	}
	return env
}
