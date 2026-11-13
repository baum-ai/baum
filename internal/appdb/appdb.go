// Package appdb owns the desktop application's private per-user SQLite file.
// Relational stores share its connection policy; small state documents use
// bucket/key rows rather than separate JSON files.
package appdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/sqlitepool"
)

// Version is the current application database schema.
const Version = 2
const applicationID = 0x6261756d

// Directory is the application data directory, beside daemon runtime state.
func Directory() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "baum"), nil
}

// Path is the single per-user application database path.
func Path() (string, error) {
	dir, err := Directory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "app.sqlite"), nil
}

// JournalSizeLimit is the size, in bytes, a write-ahead log is cut back to
// once a checkpoint has copied it into the database.
const JournalSizeLimit = 16 << 20

// sharedConnections is how many connections a shared handle (Acquire) opens
// at most: readers do not wait for each other or for the writer.
const sharedConnections = 8

// Open opens the common database policy. Read-only opens never create the
// database, change permissions, or initialize a schema; on a database in WAL
// mode SQLite creates its -shm (and an empty -wal) beside it, private like the
// database, as any connection to it does. A newer schema is always refused.
func Open(path string, readOnly bool) (*sql.DB, error) {
	if path == "" {
		var err error
		path, err = Path()
		if err != nil {
			return nil, err
		}
	}
	if readOnly {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !privatefs.PrivateFile(path, info) {
			return nil, errors.New("application database must be a private regular file owned by the current user")
		}
	} else {
		if err := privatefs.MkdirAll(filepath.Dir(path)); err != nil {
			return nil, err
		}
		// Closing any ordinary descriptor to an existing SQLite file can drop
		// this process's POSIX locks. Only create it through a new descriptor.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			err = file.Close()
		} else if errors.Is(err, fs.ErrExist) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !privatefs.PrivateFile(path, info) {
			return nil, errors.New("application database must be a private regular file owned by the current user")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath}).String() + "?mode=" + mode + "&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
	// Read-then-write transactions must reserve the writer before reading;
	// upgrading a deferred read lock can fail immediately despite busy_timeout.
	if !readOnly {
		uri += "&_txlock=immediate&_pragma=journal_size_limit(" + strconv.Itoa(JournalSizeLimit) + ")"
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = initialize(db, readOnly, filepath.Dir(absolute)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Acquire returns a handle opened as Open opens one, shared with the rest of
// the process once sqlitepool is enabled, and the function that releases it.
// Callers must not close it or leave a transaction open past release.
func Acquire(path string, readOnly bool) (*sql.DB, func(), error) {
	if path == "" {
		var err error
		if path, err = Path(); err != nil {
			return nil, nil, err
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	return sqlitepool.Acquire(absolute, readOnly, func() (*sql.DB, bool, error) {
		db, err := Open(absolute, readOnly)
		if err == nil {
			db.SetMaxOpenConns(sharedConnections)
			db.SetMaxIdleConns(2)
			db.SetConnMaxIdleTime(time.Minute)
		}
		return db, true, err
	})
}

func initialize(db *sql.DB, readOnly bool, dir string) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if !readOnly {
		// Best effort: a database another connection holds stays in its
		// journal mode until a later write-open switches it, and one in a
		// directory where the log does not work is never switched.
		var mode string
		if conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) == nil && mode != "wal" && sqlitepool.WALSupported(dir) {
			_, _ = conn.ExecContext(ctx, "PRAGMA journal_mode=WAL")
		}
		if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
			}
		}()
	}
	var version, id int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if version > Version {
		return fmt.Errorf("application database schema %d is newer than supported version %d", version, Version)
	}
	if version < 0 {
		return errors.New("unsupported application database baseline")
	}
	if id != 0 && id != applicationID {
		return errors.New("not a baum application database")
	}
	if readOnly {
		if version != Version || id != applicationID {
			return errors.New("unsupported application database baseline")
		}
		rows, err := conn.QueryContext(ctx, "SELECT bucket,key,value FROM app_documents LIMIT 0")
		if err != nil {
			return err
		}
		return rows.Close()
	}
	if version == 0 {
		var tables int
		if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("unrecognized application database baseline")
		}
		if _, err = conn.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE app_documents(bucket TEXT NOT NULL,key TEXT NOT NULL,value BLOB NOT NULL,PRIMARY KEY(bucket,key)); PRAGMA application_id=%d; PRAGMA user_version=%d;`, applicationID, Version)); err != nil {
			return err
		}
	} else if id != applicationID {
		return errors.New("unsupported application database baseline")
	}
	if version == 1 {
		var groups int
		if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='groups'").Scan(&groups); err != nil {
			return err
		}
		if groups > 0 {
			if _, err = conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS groups_repository ON groups(repository,deleted,created,id)`); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT bucket,key,value FROM app_documents LIMIT 0")
	if err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// Read returns fs.ErrNotExist for a missing database or document.
func Read(path, bucket, key string) ([]byte, error) {
	db, release, err := Acquire(path, true)
	if err != nil {
		return nil, err
	}
	defer release()
	var raw []byte
	err = db.QueryRow(`SELECT value FROM app_documents WHERE bucket=? AND key=?`, bucket, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fs.ErrNotExist
	}
	return raw, err
}

// Write replaces one document, or deletes it when raw is nil.
func Write(path, bucket, key string, raw []byte) error {
	return Update(path, bucket, key, func([]byte) ([]byte, error) { return raw, nil })
}

// Update serializes read-modify-write across processes. Missing rows pass nil
// to change; a failed callback or commit leaves the prior value untouched.
func Update(path, bucket, key string, change func([]byte) ([]byte, error)) (err error) {
	db, release, err := Acquire(path, false)
	if err != nil {
		return err
	}
	defer release()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	// Also on a panic in change: the connection goes back to a shared handle.
	defer func() {
		if !committed {
			Abandon(ctx, conn)
		}
	}()
	var raw []byte
	err = conn.QueryRowContext(ctx, `SELECT value FROM app_documents WHERE bucket=? AND key=?`, bucket, key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	next, err := change(raw)
	if err != nil {
		return err
	}
	if next == nil {
		_, err = conn.ExecContext(ctx, `DELETE FROM app_documents WHERE bucket=? AND key=?`, bucket, key)
	} else {
		_, err = conn.ExecContext(ctx, `INSERT INTO app_documents(bucket,key,value) VALUES(?,?,?) ON CONFLICT(bucket,key) DO UPDATE SET value=excluded.value`, bucket, key, next)
	}
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// Transact runs change in one immediate write transaction on a connection of
// db and commits it. A failed change or commit, or a panic, rolls it back
// (Abandon).
func Transact(ctx context.Context, db *sql.DB, change func(*sql.Conn) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			Abandon(ctx, conn)
		}
	}()
	if err = change(conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// Abandon rolls back the transaction open on conn; when that fails, the
// connection is closed rather than returned to its handle, so a transaction
// left open cannot keep holding the database's write lock.
func Abandon(ctx context.Context, conn *sql.Conn) {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}
