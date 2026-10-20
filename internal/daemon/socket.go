package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/privatefs"
)

// socketCheckInterval is how often a running daemon checks that its control
// socket still exists.
const socketCheckInterval = 5 * time.Second

// controlListener is the daemon's control socket. A socket file removed
// while the daemon runs (a cleaner of /tmp, a user tidying up) leaves the
// listener open but unreachable: clients find no socket, and hooks cannot
// start a replacement while this daemon holds the lock. watch notices and
// listens again on the same path, so clients reach the daemon again.
type controlListener struct {
	path string

	mu     sync.Mutex
	l      net.Listener
	closed bool
}

func newControlListener(path string) (*controlListener, error) {
	l, err := listenControl(path)
	if err != nil {
		return nil, err
	}
	return &controlListener{path: path, l: l}, nil
}

// Accept accepts on the current listener, moving to its replacement when
// watch replaced it.
func (c *controlListener) Accept() (net.Conn, error) {
	for {
		c.mu.Lock()
		l := c.l
		c.mu.Unlock()
		conn, err := l.Accept()
		if err == nil {
			return conn, nil
		}
		c.mu.Lock()
		replaced := !c.closed && c.l != l
		c.mu.Unlock()
		if !replaced {
			return nil, err
		}
	}
}

// Close closes the current listener, which removes its socket file.
func (c *controlListener) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.l.Close()
}

func (c *controlListener) Addr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.l.Addr()
}

// watch checks the socket file every interval until ctx ends, and listens
// again when it is gone. relistened runs after each new listener serves.
func (c *controlListener) watch(ctx context.Context, interval time.Duration, log *slog.Logger, relistened func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := os.Lstat(c.path); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := c.relisten(); err != nil {
			log.Warn("control socket removed; could not listen again", "socket", c.path, "error", err)
			continue
		}
		log.Warn("control socket was removed; listening again", "socket", c.path)
		if relistened != nil {
			relistened()
		}
	}
}

// relisten replaces the listener whose socket file is gone with a new one on
// the same path. Connections already accepted keep being served.
func (c *controlListener) relisten() error {
	if err := privatefs.MkdirAll(filepath.Dir(c.path)); err != nil {
		return err
	}
	l, err := listenControl(c.path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = l.Close()
		return net.ErrClosed
	}
	old := c.l
	c.l = l
	c.mu.Unlock()
	// Closing the old listener must not remove the new socket file, which
	// has the same path.
	if unix, ok := old.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
	_ = old.Close()
	return nil
}

// checkSocketDirectory refuses a control socket whose directory is not
// private to the user: the /tmp fallback has a predictable name, and another
// user who created it first must not receive this user's requests. On
// Windows the socket's directory is protected by its DACL and never in a
// shared directory.
func checkSocketDirectory(socket string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir := filepath.Dir(socket)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return privatefs.ProtectDirectory(dir)
}
