package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"

	"github.com/baum-ai/baum/bundle"
)

const MaxAttachmentBytes = 10 << 20
const MaxAttachments = 20

var attachmentID = regexp.MustCompile(`^att_[0-9a-f]{32}$`)

// Image bytes are content-addressed: attachment_blobs holds one copy of each
// distinct image, keyed by the SHA-256 of its bytes, and version_attachments
// maps a version's attachment IDs to those copies. Versions stay immutable
// (rows are only ever added), so regenerating a review with the same images
// stores no new bytes. The blob table keeps a rowid because its rows are large.
const attachmentSchema = `CREATE TABLE IF NOT EXISTS attachment_blobs(sha256 TEXT PRIMARY KEY, content_type TEXT NOT NULL, bytes BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS version_attachments(repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, id TEXT NOT NULL, sha256 TEXT NOT NULL, PRIMARY KEY(repository,pr,version,id)) WITHOUT ROWID;`

// legacyAttachments is the earlier per-version copy table. Writable opens move
// its rows into the content-addressed tables; reads still fall back to it, so
// a store an older binary wrote to after that move, or one whose move has not
// finished, stays readable.
const legacyAttachments = "attachments"

// Attachment is a captured raster image. Bytes are persisted transactionally
// with the version, never inferred from a path or fetched from a document URL.
type Attachment struct {
	ID          string
	ContentType string
	Bytes       []byte
}

func attachmentIDs(document []byte) map[string]bool {
	var doc struct {
		Index struct {
			Attachments []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		}
	}
	_ = json.Unmarshal(document, &doc)
	ids := map[string]bool{}
	for _, a := range doc.Index.Attachments {
		ids[a.ID] = true
	}
	return ids
}
func validateAttachments(v Version) error {
	if len(v.Attachments) > MaxAttachments {
		return errors.New("too many stored attachments")
	}
	ids := attachmentIDs(v.Document)
	seen := map[string]bool{}
	for _, a := range v.Attachments {
		mime, _, _, ok := bundle.SniffImage(a.Bytes)
		if !attachmentID.MatchString(a.ID) || !ids[a.ID] || seen[a.ID] || len(a.Bytes) > MaxAttachmentBytes || !ok || mime != a.ContentType {
			return errors.New("invalid stored attachment")
		}
		seen[a.ID] = true
	}
	return nil
}

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func tableExists(ctx context.Context, q queryer, name string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// putAttachment stores one version's attachment, adding its bytes only when no
// identical image is stored yet. Put has already rejected a version's duplicate
// IDs; a migrated row a version already maps is left as it is.
func putAttachment(ctx context.Context, tx execer, repository string, pr, version int, a Attachment) error {
	hash := contentHash(a.Bytes)
	if _, err := tx.ExecContext(ctx, `INSERT INTO attachment_blobs(sha256,content_type,bytes) VALUES(?,?,?) ON CONFLICT(sha256) DO NOTHING`, hash, a.ContentType, a.Bytes); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO version_attachments(repository,pr,version,id,sha256) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, repository, pr, version, a.ID, hash)
	return err
}

func saveAttachments(ctx context.Context, tx *sql.Tx, k Key, v Version) error {
	if len(v.Attachments) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, attachmentSchema); err != nil {
		return err
	}
	for _, a := range v.Attachments {
		if err := putAttachment(ctx, tx, k.repository(), k.Number, v.Metadata.Version, a); err != nil {
			return err
		}
	}
	return nil
}

// migrationBatchBytes bounds the image bytes one migration transaction moves,
// so the write lock it holds stays short (the store uses a rollback journal,
// which blocks readers while a large transaction spills to the file).
var migrationBatchBytes = 32 << 20

// migrateAttachments moves the per-version copies of an earlier store into the
// content-addressed tables, then drops the old table. Each batch is its own
// BEGIN IMMEDIATE transaction that re-checks the table, copies rows in rowid
// order and deletes them, so concurrent openers wait their turn instead of
// failing, an interrupted migration resumes where it stopped, and the freed
// pages hold the new copies. Rows are read one at a time, so memory stays
// bounded by the largest image.
func migrateAttachments(ctx context.Context, db *sql.DB) error {
	if exists, err := tableExists(ctx, db, legacyAttachments); err != nil || !exists {
		return err
	}
	for done := false; !done; {
		err := immediate(ctx, db, func(conn *sql.Conn) error {
			exists, err := tableExists(ctx, conn, legacyAttachments)
			if err != nil || !exists {
				done = true
				return err
			}
			if _, err = conn.ExecContext(ctx, attachmentSchema); err != nil {
				return err
			}
			last, copied, moved := int64(math.MinInt64), 0, 0
			for moved < migrationBatchBytes {
				var a Attachment
				var repository string
				var pr, version int
				err := conn.QueryRowContext(ctx, `SELECT rowid,repository,pr,version,id,content_type,bytes FROM attachments WHERE rowid>? ORDER BY rowid LIMIT 1`, last).
					Scan(&last, &repository, &pr, &version, &a.ID, &a.ContentType, &a.Bytes)
				if errors.Is(err, sql.ErrNoRows) {
					break
				}
				if err != nil {
					return err
				}
				if err = putAttachment(ctx, conn, repository, pr, version, a); err != nil {
					return err
				}
				copied, moved = copied+1, moved+len(a.Bytes)
			}
			if copied == 0 {
				done = true
				_, err = conn.ExecContext(ctx, `DROP TABLE attachments`)
				return err
			}
			_, err = conn.ExecContext(ctx, `DELETE FROM attachments WHERE rowid<=?`, last)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Attachment reads a single document-listed captured image. Version zero
// selects latest in one read transaction. Earlier stores without bytes return
// ErrNotFound without creating a table or changing their contents.
func (s *Store) Attachment(ctx context.Context, k Key, version int, id string) (Attachment, error) {
	if !k.valid() || version < 0 || !attachmentID.MatchString(id) {
		return Attachment{}, ErrNotFound
	}
	db, err := s.database(false)
	if err != nil {
		return Attachment{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Attachment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	var document []byte
	query := `SELECT version,document FROM versions WHERE repository=? AND pr=?`
	args := []any{k.repository(), k.Number}
	if version > 0 {
		query += ` AND version=?`
		args = append(args, version)
	}
	query += ` ORDER BY version DESC LIMIT 1`
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&n, &document); errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	} else if err != nil {
		return Attachment{}, err
	}
	if !attachmentIDs(document)[id] {
		return Attachment{}, ErrNotFound
	}
	a := Attachment{ID: id}
	found := false
	if exists, err := tableExists(ctx, tx, "version_attachments"); err != nil {
		return Attachment{}, err
	} else if exists {
		var hash string
		err = tx.QueryRowContext(ctx, `SELECT b.sha256,b.content_type,b.bytes FROM version_attachments v JOIN attachment_blobs b ON b.sha256=v.sha256 WHERE v.repository=? AND v.pr=? AND v.version=? AND v.id=?`, k.repository(), k.Number, n, id).Scan(&hash, &a.ContentType, &a.Bytes)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Attachment{}, err
		}
		if err == nil {
			if contentHash(a.Bytes) != hash {
				return Attachment{}, errors.New("invalid stored attachment")
			}
			found = true
		}
	}
	if !found {
		if exists, err := tableExists(ctx, tx, legacyAttachments); err != nil {
			return Attachment{}, err
		} else if !exists {
			return Attachment{}, ErrNotFound
		}
		if err = tx.QueryRowContext(ctx, `SELECT content_type,bytes FROM attachments WHERE repository=? AND pr=? AND version=? AND id=?`, k.repository(), k.Number, n, id).Scan(&a.ContentType, &a.Bytes); errors.Is(err, sql.ErrNoRows) {
			return Attachment{}, ErrNotFound
		} else if err != nil {
			return Attachment{}, err
		}
	}
	if err = validateAttachments(Version{Document: document, Attachments: []Attachment{a}}); err != nil {
		return Attachment{}, err
	}
	return a, nil
}
