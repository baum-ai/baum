package reviewindex

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// While a connection stays open, as the daemon's do, a commit reaches only
// the store's write-ahead log: the fingerprint still moves for it, and still
// looks through a lease renewal.
func TestFingerprintFollowsCommitsInTheLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	worktree := filepath.Join(dir, "clone")
	common := filepath.Join(worktree, ".git")
	if err := os.MkdirAll(common, 0o700); err != nil {
		t.Fatal(err)
	}
	picked := []repos.Repository{{Repository: "acme/a", Clones: []repos.Clone{{ID: "c", Path: worktree, CommonDir: common, Repository: "acme/a"}}}}
	fingerprint := Fingerprint(func(context.Context) ([]repos.Repository, error) { return picked, nil }, func() string { return "1" })
	s, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/a", Number: 1}
	id, err := s.BeginRun(ctx, key, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(common, "baum", "reviews", "versions.sqlite")
	holder, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	var runs int
	if err := holder.QueryRow("SELECT count(*) FROM runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	check := fingerprints(t, fingerprint)
	// Backdated, so a commit's new modification time shows whatever the file
	// system's timestamp granularity.
	backdate := func() {
		t.Helper()
		past := time.Now().Add(-time.Hour)
		for _, name := range []string{path, path + "-wal"} {
			if err := os.Chtimes(name, past, past); err != nil {
				t.Fatal(err)
			}
		}
		check.rebase()
	}
	backdate()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginRun(ctx, store.Key{Repository: "acme/a", Number: 2}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(path); err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("the commit reached versions.sqlite itself, not only its log: %v", err)
	}
	check.moved("a commit in the log")
	backdate()
	if err := s.HeartbeatRun(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check.still("a lease renewal in the log")
	if err := s.FinishRun(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	check.moved("a write after a lease renewal")
}
