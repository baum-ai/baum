package daemon

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

// errLocked reports a lock another process (or connection) holds.
var errLocked = errors.New("locked")

// fileLock is an SQLite write transaction on a private file. SQLite's OS
// locks are released when the process exits, however it exits.
type fileLock struct {
	db   *sql.DB
	conn *sql.Conn
}

// tryLock takes the lock at path or returns errLocked at once.
func tryLock(ctx context.Context, path string) (*fileLock, error) {
	// A relative path would be read as a URI authority below.
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
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
		// must not pass for a running daemon.
		var code interface{ Code() int }
		if errors.As(err, &code) && (code.Code()&255 == 5 || code.Code()&255 == 6) {
			return nil, errLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &fileLock{db: db, conn: conn}, nil
}

// waitLock retries tryLock until timeout.
func waitLock(ctx context.Context, path string, timeout time.Duration) (*fileLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		l, err := tryLock(ctx, path)
		if !errors.Is(err, errLocked) || !time.Now().Before(deadline) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// held reports whether another process holds the lock at path.
func held(ctx context.Context, path string) (bool, error) {
	l, err := tryLock(ctx, path)
	if errors.Is(err, errLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, l.release()
}

func (l *fileLock) release() error {
	if l == nil || l.conn == nil {
		return nil
	}
	_, rollback := l.conn.ExecContext(context.Background(), "ROLLBACK")
	err := errors.Join(rollback, l.conn.Close(), l.db.Close())
	l.conn = nil
	return err
}
