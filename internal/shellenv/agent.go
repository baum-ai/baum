package shellenv

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultAgentRetry is how long Agent waits after a failed login shell run
// before asking again.
const DefaultAgentRetry = time.Minute

// Agent keeps a long-lived process's SSH agent socket usable. The socket in
// its environment goes stale when the session that provided it ends (an ssh
// login closes, an agent restarts); a stale socket is replaced by the login
// shell's, or dropped, so a fetch fails clearly instead of using it.
//
// The shell is asked only when a socket this process had has gone stale: a
// user without an agent never pays for a shell run. An answer without a
// usable socket is remembered; a failed run is retried after Retry. Callers
// share one run and never wait on each other otherwise.
type Agent struct {
	// Resolve returns the login shell's SSH_AUTH_SOCK.
	Resolve func(ctx context.Context) (string, error)
	// Retry spaces login shell runs after a failure; zero is
	// DefaultAgentRetry.
	Retry time.Duration
	// Now is a seam for tests.
	Now func() time.Time

	mu sync.Mutex
	// had is set once SSH_AUTH_SOCK was set; none once the shell answered
	// without a usable socket; failed is when a run last failed.
	had, none bool
	failed    time.Time
	group     singleflight.Group
}

// Socket is the agent socket to use, "" for none. A usable SSH_AUTH_SOCK is
// kept; a stale one is replaced in this process's environment by the login
// shell's, or removed.
func (a *Agent) Socket(ctx context.Context) string {
	a.mu.Lock()
	current := os.Getenv("SSH_AUTH_SOCK")
	if current != "" {
		a.had = true
	}
	if usableSocket(current) {
		a.mu.Unlock()
		return current
	}
	if current != "" {
		_ = os.Unsetenv("SSH_AUTH_SOCK")
	}
	retry := a.Retry
	if retry <= 0 {
		retry = DefaultAgentRetry
	}
	ask := a.Resolve != nil && a.had && !a.none && (a.failed.IsZero() || a.now().Sub(a.failed) >= retry)
	a.mu.Unlock()
	if !ask {
		return ""
	}
	value, _, _ := a.group.Do("resolve", func() (any, error) {
		socket, err := a.Resolve(ctx)
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case err != nil:
			a.failed = a.now()
		case usableSocket(socket):
			_ = os.Setenv("SSH_AUTH_SOCK", socket)
			return socket, nil
		default:
			a.none = true
		}
		return "", nil
	})
	return value.(string)
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// usableSocket reports an absolute path to an existing socket.
func usableSocket(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
