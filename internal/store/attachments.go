package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/baum-ai/baum/bundle"
)

const MaxAttachmentBytes = 10 << 20
const MaxAttachments = 20

var attachmentID = regexp.MustCompile(`^att_[0-9a-f]{32}$`)

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
func saveAttachments(ctx context.Context, tx *sql.Tx, k Key, v Version) error {
	if len(v.Attachments) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attachments(repository TEXT NOT NULL,pr INTEGER NOT NULL,version INTEGER NOT NULL,id TEXT NOT NULL,content_type TEXT NOT NULL,bytes BLOB NOT NULL,PRIMARY KEY(repository,pr,version,id))`); err != nil {
		return err
	}
	for _, a := range v.Attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(repository,pr,version,id,content_type,bytes) VALUES(?,?,?,?,?,?)`, k.repository(), k.Number, v.Metadata.Version, a.ID, a.ContentType, a.Bytes); err != nil {
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
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='attachments'`).Scan(&exists); err != nil {
		return Attachment{}, err
	}
	if exists == 0 {
		return Attachment{}, ErrNotFound
	}
	var a Attachment
	a.ID = id
	if err = tx.QueryRowContext(ctx, `SELECT content_type,bytes FROM attachments WHERE repository=? AND pr=? AND version=? AND id=?`, k.repository(), k.Number, n, id).Scan(&a.ContentType, &a.Bytes); errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	} else if err != nil {
		return Attachment{}, err
	}
	if err = validateAttachments(Version{Document: document, Attachments: []Attachment{a}}); err != nil {
		return Attachment{}, err
	}
	return a, nil
}
