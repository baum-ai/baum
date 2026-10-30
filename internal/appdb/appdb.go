// Package appdb owns the desktop application's private per-user SQLite file.
// Relational stores share its connection policy; small state documents use
// bucket/key rows rather than separate JSON files.
package appdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/privatefs"
)

// Version is the first public application database baseline.
const Version = 1
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

// Open opens the common database policy. Read-only opens never create a file,
// change permissions, or initialize a schema. A newer schema is always refused.
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
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = initialize(db, readOnly); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func initialize(db *sql.DB, readOnly bool) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if !readOnly {
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
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// Read returns fs.ErrNotExist for a missing database or document.
func Read(path, bucket, key string) ([]byte, error) {
	db, err := Open(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
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
	db, err := Open(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
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
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
