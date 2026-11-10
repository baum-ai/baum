package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
)

// Document revisions name the stored bytes of each version's document without
// reading them, so a reader can tell a document it already parsed from one
// rewritten since: RefreshCurrent and CompleteCurrent rewrite the latest
// version in place, often to the same length. Triggers on versions give a row
// a new random revision in the transaction of every write to it, by this
// binary or an older one, as the summaries triggers do. A store whose
// triggers are not exactly the current ones (one not write-opened by this
// version yet) has no revision a reader may trust, and Revision is then 0.
const revisionsSchema = `CREATE TABLE IF NOT EXISTS document_revisions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(repository,pr,version)) WITHOUT ROWID`

const revisionsPrefix = "revisions_"

// revisionTriggers renew a row's revision on any insert or update, whatever
// columns it changes, and drop it with the row. Names carry a hash of the SQL,
// as summaryTriggers' do.
func revisionTriggers() []trigger {
	forget := `DELETE FROM document_revisions WHERE repository=OLD.repository AND pr=OLD.pr AND version=OLD.version;`
	renew := `INSERT OR REPLACE INTO document_revisions(repository,pr,version,revision) VALUES(NEW.repository,NEW.pr,NEW.version,random());`
	out := []trigger{
		{revisionsPrefix + "insert", ` AFTER INSERT ON versions BEGIN ` + renew + ` END`},
		{revisionsPrefix + "update", ` AFTER UPDATE ON versions BEGIN ` + forget + renew + ` END`},
		{revisionsPrefix + "delete", ` AFTER DELETE ON versions BEGIN ` + forget + ` END`},
	}
	h := sha256.New()
	h.Write([]byte(revisionsSchema))
	for _, t := range out {
		h.Write([]byte(t.name + t.body))
	}
	suffix := "_" + hex.EncodeToString(h.Sum(nil))[:8]
	for i := range out {
		out[i].name += suffix
	}
	return out
}

// currentRevisions reports whether the table and exactly the current triggers
// exist, so every revision in the table changed with its row.
func currentRevisions(ctx context.Context, q interface {
	querier
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var tables int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_revisions'`).Scan(&tables); err != nil || tables != 1 {
		return false, err
	}
	have, err := triggerNames(ctx, q, revisionsPrefix)
	if err != nil {
		return false, err
	}
	want := revisionTriggers()
	if len(have) != len(want) {
		return false, nil
	}
	for _, t := range want {
		if !slices.Contains(have, t.name) {
			return false, nil
		}
	}
	return true, nil
}

// setupRevisions makes the table and triggers current. A store that already
// has them is only read. Otherwise one transaction replaces the triggers and
// gives every row a new revision, since rows written while they were missing
// may have kept an old one.
func setupRevisions(ctx context.Context, db *sql.DB) error {
	if current, err := currentRevisions(ctx, db); err != nil || current {
		return err
	}
	return immediate(ctx, db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, revisionsSchema); err != nil {
			return err
		}
		have, err := triggerNames(ctx, conn, revisionsPrefix)
		if err != nil {
			return err
		}
		for _, name := range have {
			if _, err := conn.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
				return err
			}
		}
		for _, t := range revisionTriggers() {
			if _, err := conn.ExecContext(ctx, `CREATE TRIGGER "`+t.name+`"`+t.body); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM document_revisions`); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO document_revisions(repository,pr,version,revision) SELECT repository,pr,version,random() FROM versions`)
		return err
	})
}

// revisionSelect is the revision column and join of a query over versions v:
// the row's revision when the store keeps them current, else 0.
func revisionSelect(current bool) (column, join string) {
	if !current {
		return `0`, ``
	}
	return `COALESCE(r.revision,0)`, ` LEFT JOIN document_revisions r ON r.repository=v.repository AND r.pr=v.pr AND r.version=v.version`
}

// Revision returns the version Latest (n 0) or Get (n positive) would read and
// its document's revision (Version.Revision), without reading the document.
// The revision is 0 when the store cannot vouch for one; it is then not an
// identity of the document.
func (s *Store) Revision(ctx context.Context, k Key, n int) (version int, revision int64, err error) {
	if !k.valid() || n < 0 {
		return 0, 0, fmt.Errorf("invalid repository, pull request or version")
	}
	db, err := s.database(false)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := currentRevisions(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	column, join := revisionSelect(current)
	query := `SELECT v.version,` + column + ` FROM versions v` + join + ` WHERE v.repository=? AND v.pr=?`
	args := []any{k.repository(), k.Number}
	if n > 0 {
		query += ` AND v.version=?`
		args = append(args, n)
	}
	query += ` ORDER BY v.version DESC LIMIT 1`
	err = tx.QueryRowContext(ctx, query, args...).Scan(&version, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	return version, revision, err
}
