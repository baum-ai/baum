package store

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/baum-ai/baum/internal/sqlitepool"
)

// A write-open switches the store to the write-ahead log, and a revision
// still names each write of a document (the triggers run in the writer's
// transaction whatever the journal).
func TestWriteOpenSwitchesToTheWriteAheadLog(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	err = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	_ = db.Close()
	if err != nil || mode != "wal" {
		t.Fatalf("journal mode %q %v", mode, err)
	}
	k := Key{"acme/widgets", 1}
	first := putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"one"`))
	stored, err := s.Latest(ctx, k)
	if err != nil || stored.Revision == 0 {
		t.Fatalf("revision %d %v", stored.Revision, err)
	}
	refreshed := docFor(k, 1, `,"pr_state":"closed"`, `"title":"one"`)
	if err := s.RefreshCurrent(ctx, k, stored, refreshed); err != nil {
		t.Fatal(err)
	}
	_, revision, err := s.Revision(ctx, k, 0)
	if err != nil || revision == 0 || revision == stored.Revision {
		t.Fatalf("refresh kept revision %d (was %d): %v", revision, stored.Revision, err)
	}
	_ = first
}

// A store in a directory this user cannot write is still read, once its last
// writer has closed (no log, no -shm left beside it).
func TestReadOnlyDirectoryStoreIsRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write")
	}
	ctx := context.Background()
	s := opened(t)
	k := Key{"acme/widgets", 1}
	putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"one"`))
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(s.path() + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s left after the last writer closed: %v", suffix, err)
		}
	}
	if err := os.Chmod(s.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.root, 0o700) })
	ro, err := Open("", s.root, false)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := ro.Latest(ctx, k); err != nil || v.Metadata.Version != 1 {
		t.Fatalf("latest %+v %v", v.Metadata, err)
	}
	if rows, err := ro.Snapshot(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("snapshot %+v %v", rows, err)
	}
}

// With shared handles on, the daemon's reader keeps its handle and sees a
// version another process publishes.
func TestSharedReaderSeesAnotherWriter(t *testing.T) {
	sqlitepool.Enable()
	t.Cleanup(sqlitepool.Disable)
	ctx := context.Background()
	s := opened(t)
	k := Key{"acme/widgets", 1}
	first := putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"one"`))
	if v, err := s.Latest(ctx, k); err != nil || v.Metadata.Version != 1 {
		t.Fatalf("latest %+v %v", v.Metadata, err)
	}
	// Another process: its own connection, not the shared handle.
	other, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	next := version(k, 2)
	next.Document = docFor(k, 2, `,"pr_state":"open"`, `"title":"two"`)
	if _, err := other.Exec(`INSERT INTO versions(repository,pr,version,metadata,document) VALUES(?,?,?,?,?)`, k.repository(), k.Number, 2, []byte(`{"version":2}`), []byte(next.Document)); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Latest(ctx, k); err != nil || v.Metadata.Version != 2 {
		t.Fatalf("other writer not seen: %+v %v", v.Metadata, err)
	}
	_ = first
}
