package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A kept head is a newer head of a pull request that its saved review was
// found to still cover: with automatic reviews set to review on change, the
// review agent read what the head changed since the review and found nothing
// that would make a difference to it, so baum wrote no new version. The
// review counts as current at that head as at its own. Only the latest kept
// head of a version is recorded, and a newer version leaves the row behind
// without meaning.

// keptSchema creates the kept_heads table. openRoot runs it on every writable
// open, so a store made before it existed gains it on its next write.
const keptSchema = `CREATE TABLE IF NOT EXISTS kept_heads (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, head TEXT NOT NULL, reason TEXT NOT NULL, kept_at INTEGER NOT NULL, PRIMARY KEY(repository,pr,version)) WITHOUT ROWID`

// maxKeptReason bounds the reason stored with a kept head.
const maxKeptReason = 2000

// Kept is the latest head a version was kept for, and why.
type Kept struct {
	Head   string    `json:"head"`
	Reason string    `json:"reason"`
	At     time.Time `json:"kept_at"`
}

// KeepHead records that version of k still covers head (see Kept). It
// replaces what the version recorded before.
func (s *Store) KeepHead(ctx context.Context, k Key, version int, head, reason string, at time.Time) error {
	if !k.valid() || version <= 0 || head == "" {
		return errors.New("invalid kept head")
	}
	if len(reason) > maxKeptReason {
		reason = reason[:maxKeptReason]
	}
	db, release, err := s.acquire(true)
	if err != nil {
		return err
	}
	defer release()
	if _, err = db.ExecContext(ctx, keptSchema); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO kept_heads(repository,pr,version,head,reason,kept_at) VALUES (?,?,?,?,?,?)`, k.repository(), k.Number, version, head, reason, at.UnixNano())
	return err
}

// KeptHead is the kept head of version of k, nil when it has none.
func (s *Store) KeptHead(ctx context.Context, k Key, version int) (*Kept, error) {
	if !k.valid() || version <= 0 {
		return nil, fmt.Errorf("invalid repository, pull request or version")
	}
	db, release, err := s.acquire(false)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer release()
	if ok, err := hasKeptHeads(ctx, db); err != nil || !ok {
		return nil, err
	}
	var kept Kept
	var at int64
	err = db.QueryRowContext(ctx, `SELECT head,reason,kept_at FROM kept_heads WHERE repository=? AND pr=? AND version=?`, k.repository(), k.Number, version).Scan(&kept.Head, &kept.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kept.At = time.Unix(0, at).UTC()
	return &kept, nil
}

// hasKeptHeads reports whether the table exists: a store no writer of this
// version opened has none, and reads must not create it.
func hasKeptHeads(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='kept_heads'`).Scan(&n)
	return n == 1, err
}

// keptHeads attaches each summary's latest version's kept head, inside
// Snapshot's read transaction.
func keptHeads(ctx context.Context, tx *sql.Tx, entries map[Key]*Summary) error {
	if ok, err := hasKeptHeads(ctx, tx); err != nil || !ok {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT repository,pr,version,head,reason,kept_at FROM kept_heads`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k Key
		var version int
		var kept Kept
		var at int64
		if err := rows.Scan(&k.Repository, &k.Number, &version, &kept.Head, &kept.Reason, &at); err != nil {
			return err
		}
		item := entries[k]
		if item == nil || item.Latest == nil || item.Latest.Version != version {
			continue
		}
		kept.At = time.Unix(0, at).UTC()
		item.Kept = &kept
	}
	return rows.Err()
}
