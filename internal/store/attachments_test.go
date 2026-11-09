package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

const imageID = "att_0123456789abcdef0123456789abcdef"

func imageVersion(t *testing.T, k Key, n int) Version {
	t.Helper()
	v := version(k, n)
	var document map[string]any
	if err := json.Unmarshal(v.Document, &document); err != nil {
		t.Fatal(err)
	}
	document["index"] = map[string]any{"attachments": []any{map[string]any{"id": imageID}}}
	v.Document, _ = json.Marshal(document)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	v.Attachments = []Attachment{{imageID, "image/png", png}}
	return v
}
func TestVersionAttachmentAtomicityAndIsolation(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	first := imageVersion(t, key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.Attachment(ctx, key, 0, imageID)
	if err != nil || !bytes.Equal(got.Bytes, first.Attachments[0].Bytes) {
		t.Fatal(got, err)
	}
	second := version(key, 2)
	if err = s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Attachment(ctx, key, 0, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal("latest exposed old image", err)
	}
	if _, err = s.Attachment(ctx, key, 1, imageID); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Key{{"other/repo", 7}, {"owner/repo", 8}, {"enterprise.example/owner/repo", 7}} {
		if _, err = s.Attachment(ctx, other, 1, imageID); !errors.Is(err, ErrNotFound) {
			t.Fatal("image crossed repository", err)
		}
	}
	// The winning immutable version cannot acquire a loser's attachments.
	if err = s.Put(ctx, key, first, imageVersion(t, key, 2)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Attachment(ctx, key, 2, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal("conflicting image published", err)
	}
}
func TestAttachmentsRejectUnlistedUnsafeAndDuplicateWithoutVersion(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	for _, mutate := range []func(*Version){func(v *Version) { v.Document = version(key, 1).Document }, func(v *Version) {
		v.Attachments[0].ContentType = "image/svg+xml"
		v.Attachments[0].Bytes = []byte(`<svg onload="alert(1)"></svg>`)
	}, func(v *Version) { v.Attachments = append(v.Attachments, v.Attachments[0]) }, func(v *Version) { v.Attachments[0].ID = "../secret" }, func(v *Version) { v.Attachments[0].Bytes = make([]byte, MaxAttachmentBytes+1) }} {
		v := imageVersion(t, key, 1)
		mutate(&v)
		if err := s.Put(ctx, key, Version{}, v); err == nil {
			t.Fatal("unsafe attachment accepted")
		}
		if _, err := s.Latest(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatal("failed attachment published document", err)
		}
	}
}
func TestOldVersionHasNoAttachmentsTable(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	v := imageVersion(t, key, 1)
	v.Attachments = nil
	if err := s.Put(ctx, key, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachment(ctx, key, 1, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVersionsShareIdenticalImageBytes(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	first := imageVersion(t, key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	second := imageVersion(t, key, 2)
	if err := s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	other := Key{"other/repo", 3}
	if err := s.Put(ctx, other, Version{}, imageVersion(t, other, 1)); err != nil {
		t.Fatal(err)
	}
	if blobs, links := countRows(t, s, "attachment_blobs"), countRows(t, s, "version_attachments"); blobs != 1 || links != 3 {
		t.Fatalf("blobs=%d links=%d, want one shared copy for three versions", blobs, links)
	}
	for _, n := range []int{1, 2} {
		if got, err := s.Attachment(ctx, key, n, imageID); err != nil || !bytes.Equal(got.Bytes, first.Attachments[0].Bytes) {
			t.Fatal(n, got, err)
		}
	}
}

// legacyRow writes an image the way earlier binaries did.
func legacyRow(t *testing.T, s *Store, k Key, version int, a Attachment) {
	t.Helper()
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS attachments(repository TEXT NOT NULL,pr INTEGER NOT NULL,version INTEGER NOT NULL,id TEXT NOT NULL,content_type TEXT NOT NULL,bytes BLOB NOT NULL,PRIMARY KEY(repository,pr,version,id))`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO attachments(repository,pr,version,id,content_type,bytes) VALUES(?,?,?,?,?,?)`, k.repository(), k.Number, version, a.ID, a.ContentType, a.Bytes); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyAttachmentsMigrateIntoSharedCopies(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open("", root, true)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"owner/repo", 7}
	first := imageVersion(t, key, 1)
	image := first.Attachments[0]
	first.Attachments = nil
	if err = s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	second := imageVersion(t, key, 2)
	second.Attachments = nil
	if err = s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	legacyRow(t, s, key, 1, image)
	legacyRow(t, s, key, 2, image)

	// A read-only open neither migrates nor loses the earlier copies.
	readOnly, err := Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readOnly.Attachment(ctx, key, 2, imageID); err != nil || !bytes.Equal(got.Bytes, image.Bytes) {
		t.Fatal(got, err)
	}
	if countRows(t, s, legacyAttachments) != 2 {
		t.Fatal("read-only open changed the store")
	}

	if s, err = Open("", root, true); err != nil {
		t.Fatal(err)
	}
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := tableExists(ctx, db, legacyAttachments)
	_ = db.Close()
	if err != nil || exists {
		t.Fatal("legacy table kept", err)
	}
	if blobs, links := countRows(t, s, "attachment_blobs"), countRows(t, s, "version_attachments"); blobs != 1 || links != 2 {
		t.Fatalf("blobs=%d links=%d", blobs, links)
	}
	for _, n := range []int{1, 2} {
		if got, err := s.Attachment(ctx, key, n, imageID); err != nil || !bytes.Equal(got.Bytes, image.Bytes) {
			t.Fatal(n, got, err)
		}
	}
	// Opening again is a no-op.
	if _, err = Open("", root, true); err != nil {
		t.Fatal(err)
	}
}

// legacyStore holds n versions whose images only the earlier table has.
func legacyStore(t *testing.T, n int) (string, Key, Attachment) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open("", root, true)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"owner/repo", 7}
	previous := Version{}
	var image Attachment
	for i := 1; i <= n; i++ {
		v := imageVersion(t, key, i)
		image = v.Attachments[0]
		v.Attachments = nil
		if err = s.Put(ctx, key, previous, v); err != nil {
			t.Fatal(err)
		}
		legacyRow(t, s, key, i, image)
		previous = v
	}
	return root, key, image
}

func TestMigrationRunsInBatchesAndConcurrentOpenersWait(t *testing.T) {
	defer func(size int) { migrationBatchBytes = size }(migrationBatchBytes)
	migrationBatchBytes = 1 // one row per transaction
	ctx := context.Background()
	root, key, image := legacyStore(t, 5)
	s := &Store{root: root}
	errs := make(chan error, 6)
	for range cap(errs) {
		go func() {
			db, err := s.database(true)
			if err == nil {
				err = migrateAttachments(ctx, db)
				_ = db.Close()
			}
			errs <- err
		}()
	}
	for range cap(errs) {
		if err := <-errs; err != nil {
			t.Fatal("concurrent migration failed:", err)
		}
	}
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := tableExists(ctx, db, legacyAttachments)
	_ = db.Close()
	if err != nil || exists {
		t.Fatal("legacy table kept", err)
	}
	if blobs, links := countRows(t, s, "attachment_blobs"), countRows(t, s, "version_attachments"); blobs != 1 || links != 5 {
		t.Fatalf("blobs=%d links=%d", blobs, links)
	}
	for n := 1; n <= 5; n++ {
		if got, err := s.Attachment(ctx, key, n, imageID); err != nil || !bytes.Equal(got.Bytes, image.Bytes) {
			t.Fatal(n, got, err)
		}
	}
}

func TestPartlyMigratedStoreServesEveryImage(t *testing.T) {
	ctx := context.Background()
	root, key, image := legacyStore(t, 3)
	s := &Store{root: root}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	// One batch moved and committed before the migration stopped.
	err = immediate(ctx, db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, attachmentSchema); err != nil {
			return err
		}
		if err := putAttachment(ctx, conn, key.repository(), key.Number, 1, image); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM attachments WHERE version=1`)
		return err
	})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		if got, err := s.Attachment(ctx, key, n, imageID); err != nil || !bytes.Equal(got.Bytes, image.Bytes) {
			t.Fatal(n, got, err)
		}
	}
	if _, err = Open("", root, true); err != nil {
		t.Fatal(err)
	}
	if links := countRows(t, s, "version_attachments"); links != 3 {
		t.Fatalf("resumed migration linked %d versions", links)
	}
}

func TestLegacyRowsWrittenAfterMigrationStayReadable(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"owner/repo", 7}
	first := imageVersion(t, key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	second := imageVersion(t, key, 2)
	image := second.Attachments[0]
	second.Attachments = nil
	if err := s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	// An older binary still running writes its copy to the earlier table.
	legacyRow(t, s, key, 2, image)
	for _, n := range []int{1, 2} {
		if got, err := s.Attachment(ctx, key, n, imageID); err != nil || !bytes.Equal(got.Bytes, image.Bytes) {
			t.Fatal(n, got, err)
		}
	}
}

func TestSharedCopyMustMatchItsHash(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"owner/repo", 7}
	if err := s.Put(ctx, key, Version{}, imageVersion(t, key, 1)); err != nil {
		t.Fatal(err)
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE attachment_blobs SET bytes=bytes||x'00'`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Attachment(ctx, key, 1, imageID); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatal("altered copy served", err)
	}
}
