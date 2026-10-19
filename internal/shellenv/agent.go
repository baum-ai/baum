package shellenv

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultAgentRetry is how often Agent asks the login shell again for an
// agent while none is usable.
const DefaultAgentRetry = time.Minute

// Agent keeps a long-lived process's SSH agent socket usable. The socket in
// its environment goes stale when the session that provided it ends (an ssh
// login closes, an agent restarts); a stale socket is replaced by the login
// shell's, or dropped, so a fetch fails clearly instead of using it.
type Agent struct {
	// Resolve returns the login shell's SSH_AUTH_SOCK.
	Resolve func(ctx context.Context) (string, error)
	// Retry spaces login shell runs while no socket is usable; zero is
	// DefaultAgentRetry.
	Retry time.Duration
	// Now is a seam for tests.
	Now func() time.Time

	mu   sync.Mutex
	last time.Time
}

// Socket is the agent socket to use, "" for none. A usable SSH_AUTH_SOCK is
// kept; otherwise the login shell's replaces it in this process's
// environment.
func (a *Agent) Socket(ctx context.Context) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if current := os.Getenv("SSH_AUTH_SOCK"); usableSocket(current) {
		return current
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	retry := a.Retry
	if retry <= 0 {
		retry = DefaultAgentRetry
	}
	if a.Resolve == nil || (!a.last.IsZero() && now.Sub(a.last) < retry) {
		_ = os.Unsetenv("SSH_AUTH_SOCK")
		return ""
	}
	a.last = now
	socket, err := a.Resolve(ctx)
	if err != nil || !usableSocket(socket) {
		_ = os.Unsetenv("SSH_AUTH_SOCK")
		return ""
	}
	_ = os.Setenv("SSH_AUTH_SOCK", socket)
	return socket
}

// usableSocket reports an absolute path to an existing socket.
func usableSocket(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
