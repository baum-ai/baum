package store

import (
	"context"
	"testing"
)

// revisionOf reads the revision Revision reports and checks Latest or Get
// reads the same one with the document.
func revisionOf(t *testing.T, s *Store, k Key, n int) int64 {
	t.Helper()
	version, revision, err := s.Revision(context.Background(), k, n)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Version
	if n == 0 {
		loaded, err = s.Latest(context.Background(), k)
	} else {
		loaded, err = s.Get(context.Background(), k, n)
	}
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != revision || loaded.Metadata.Version != version {
		t.Fatalf("Revision says version %d revision %d, the read %d %d", version, revision, loaded.Metadata.Version, loaded.Revision)
	}
	return revision
}

// Every write to a version, a same-length rewrite in place included, gives it
// a new revision; reads alone do not, and other versions keep theirs.
func TestRevisionChangesWithEveryWriteOfTheDocument(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"owner/repo", 7}
	first := version(key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	one := revisionOf(t, s, key, 0)
	if one == 0 || revisionOf(t, s, key, 1) != one || revisionOf(t, s, key, 0) != one {
		t.Fatal("revision of an unchanged version moved, or is unknown")
	}
	second := version(key, 2)
	if err := s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	two := revisionOf(t, s, key, 0)
	if two == 0 || two == one || revisionOf(t, s, key, 1) != one {
		t.Fatal("a new version's revision", one, two)
	}
	// Two rewrites to documents of the same length, within any clock tick.
	a := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":2},"checks":"aaaa"}`)
	b := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":2},"checks":"bbbb"}`)
	if err := s.RefreshCurrent(ctx, key, second, a); err != nil {
		t.Fatal(err)
	}
	afterA := revisionOf(t, s, key, 0)
	refreshed, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCurrent(ctx, key, refreshed, b); err != nil {
		t.Fatal(err)
	}
	afterB := revisionOf(t, s, key, 2)
	if afterA == two || afterB == afterA || afterB == 0 {
		t.Fatal("a rewrite kept its revision", two, afterA, afterB)
	}
}

// A store whose triggers are not the current ones (here dropped, as in a store
// no write-open of this version has seen) has no revisions; the next
// write-open restores the triggers and renews every revision.
func TestRevisionIsUnknownWithoutTheTriggers(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"owner/repo", 7}
	first := version(key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	before := revisionOf(t, s, key, 0)
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	names, err := triggerNames(ctx, db, revisionsPrefix)
	if err != nil || len(names) != 3 {
		t.Fatal(names, err)
	}
	if _, err := db.Exec(`DROP TRIGGER "` + names[0] + `"`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if revisionOf(t, s, key, 0) != 0 {
		t.Fatal("trusted a revision without its triggers")
	}
	reopened, err := Open("", s.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	after := revisionOf(t, reopened, key, 0)
	if after == 0 || after == before {
		t.Fatal("write-open did not renew revisions", before, after)
	}
	// A store from before revisions existed reads, without writing, as unknown.
	db, err = s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE document_revisions`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	readOnly, err := Open("", s.Root(), false)
	if err != nil {
		t.Fatal(err)
	}
	if revisionOf(t, readOnly, key, 0) != 0 {
		t.Fatal("revision without the table")
	}
	if _, _, err := readOnly.Revision(ctx, key, 3); err != ErrNotFound {
		t.Fatal("missing version", err)
	}
}
