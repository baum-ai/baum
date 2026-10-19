// Package filelock is an exclusive inter-process lock on a private file: an
// SQLite write transaction, which the operating system releases however the
// holding process exits, so a lock is never stale.
//
// The file is only ever opened through SQLite. On Unix, closing any other
// descriptor of a file drops every POSIX lock the process holds on it; SQLite
// keeps its own descriptors open while a lock is held, so probing a lock this
// process holds (Held) leaves it in place.
package filelock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/privatefs"
)

// ErrLocked reports a lock another holder has.
var ErrLocked = errors.New("locked")

// Lock is a held lock.
type Lock struct {
	db   *sql.DB
	conn *sql.Conn
}

// TryLock takes the lock at path, creating the file with mode 0600, or
// returns ErrLocked at once.
func TryLock(ctx context.Context, path string) (*Lock, error) {
	// A relative path would be read as a URI authority below.
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Create the file only when it is missing: nobody can hold a lock on a
	// file that did not exist, so closing this descriptor drops none.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600); err == nil {
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !privatefs.PrivateFile(path, info) {
		return nil, fmt.Errorf("lock file must be private (0600): %s", path)
	}
	// A Windows drive path needs a leading slash so "C:" is not read as the URI authority.
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: uriPath}).String()+"?_pragma=busy_timeout(0)")
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		// Only contention means another holder. A corrupt or unreadable file
		// must not pass for a held lock.
		var code interface{ Code() int }
		if errors.As(err, &code) && (code.Code()&255 == 5 || code.Code()&255 == 6) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{db: db, conn: conn}, nil
}

// Wait retries TryLock until timeout.
func Wait(ctx context.Context, path string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		l, err := TryLock(ctx, path)
		if !errors.Is(err, ErrLocked) || !time.Now().Before(deadline) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Held reports whether a holder (another process, or another Lock in this
// one) has the lock at path. A missing file is not held.
func Held(ctx context.Context, path string) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	l, err := TryLock(ctx, path)
	if errors.Is(err, ErrLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, l.Release()
}

// Release gives the lock up. It is safe on a nil or released lock.
func (l *Lock) Release() error {
	if l == nil || l.conn == nil {
		return nil
	}
	_, rollback := l.conn.ExecContext(context.Background(), "ROLLBACK")
	err := errors.Join(rollback, l.conn.Close(), l.db.Close())
	l.conn = nil
	return err
}
