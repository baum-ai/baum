package gitlocal

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// Background makes fetches safe for a process nobody watches (the baum
// daemon): no command may wait for an answer. Git already runs with
// GIT_TERMINAL_PROMPT=0 and a timeout; with a Background, SSH never asks
// through SSH_ASKPASS, and when the user configured no ssh command
// (GIT_SSH_COMMAND, core.sshCommand or GIT_SSH) OpenSSH runs with
// BatchMode=yes, so a missing key or unknown host is a clear git error rather
// than a hang. A configured command is left alone: it may be plink or a
// wrapper that does not take OpenSSH options.
type Background struct {
	// SSHAgent returns the SSH agent socket a fetch may use, or "" for none.
	// A socket that no longer exists must not be returned. It is asked only
	// for fetches from SSH remotes.
	SSHAgent func(ctx context.Context) string
}

var background atomic.Pointer[Background]

// SetBackground applies b to every later fetch in this process; nil removes
// it.
func SetBackground(b *Background) { background.Store(b) }

// sshConfigTimeout bounds reading core.sshCommand.
const sshConfigTimeout = 5 * time.Second

// env adjusts the environment of a fetch in dir from a remote that is SSH
// (or might be) for b.
func (b *Background) env(ctx context.Context, dir string, env []string, ssh bool) []string {
	out := make([]string, 0, len(env)+3)
	configured := false
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "SSH_AUTH_SOCK", "SSH_ASKPASS_REQUIRE":
			continue
		case "GIT_SSH_COMMAND", "GIT_SSH":
			configured = configured || value != ""
		}
		out = append(out, kv)
	}
	out = append(out, "SSH_ASKPASS_REQUIRE=never")
	if !ssh {
		return out
	}
	if b.SSHAgent != nil {
		if socket := b.SSHAgent(ctx); socket != "" {
			out = append(out, "SSH_AUTH_SOCK="+socket)
		}
	}
	if configured {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, sshConfigTimeout)
	defer cancel()
	command, err := run(ctx, runSpec{dir: dir, env: env, limit: 4096, timeout: sshConfigTimeout}, "config", "--get", "core.sshCommand")
	var exit *exec.ExitError
	switch {
	case err == nil && strings.TrimSpace(string(command)) != "":
		// The user's own ssh command.
	case err != nil && errors.As(err, &exit) && exit.ExitCode() == 1:
		// Not configured: git would run plain ssh.
		out = append(out, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	// Any other failure leaves git's own choice.
	return out
}

// sshRemote reports whether url is fetched over SSH: ssh://, git+ssh://,
// ssh+git:// or the scp-like user@host:path. An unknown URL counts as SSH.
func sshRemote(url string) bool {
	if url == "" {
		return true
	}
	lower := strings.ToLower(url)
	for _, scheme := range []string{"ssh://", "git+ssh://", "ssh+git://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	if strings.Contains(url, "://") || strings.HasPrefix(url, "/") || strings.HasPrefix(url, ".") {
		return false
	}
	colon := strings.IndexByte(url, ':')
	slash := strings.IndexByte(url, '/')
	// A single letter before the colon is a Windows drive, not a host.
	return colon > 1 && (slash < 0 || colon < slash)
}

// fetchEnv is the environment of a fetch in the clone at dir.
func (r *Repo) fetchEnv(ctx context.Context) []string {
	env := userEnv()
	b := background.Load()
	if b == nil {
		return env
	}
	url, err := r.RemoteFetchURL(ctx, r.opts.Remote)
	return b.env(ctx, r.dir, env, err != nil || sshRemote(url))
}
