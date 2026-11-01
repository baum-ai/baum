package appdb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
)

func TestBaselineStoresShareOneDatabaseAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	t.Setenv("XDG_STATE_HOME", appdbtest.TempDir(t))
	t.Setenv("XDG_CONFIG_HOME", appdbtest.TempDir(t))
	path, err := appdb.Path()
	if err != nil {
		t.Fatal(err)
	}
	registryPath, err := repos.Path()
	if err != nil || registryPath != path {
		t.Fatalf("registry path %q %v", registryPath, err)
	}
	registry, err := repos.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Pick(ctx, "owner/repo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.UpdatePreferences(ctx, func(p repos.Preferences) repos.Preferences { p.JobConcurrency = 2; return p }); err != nil {
		t.Fatal(err)
	}
	groups, err := reviewgroups.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	group, err := groups.Create(ctx, "owner/repo", "Together", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := decisionlog.Open(path, decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = decisions.Skip(ctx, decisionlog.Meta{Agent: "codex", ID: "session", Repository: "owner/repo"}, "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err = inboxes.Open(path).Replace(0, nil); err != nil {
		t.Fatal(err)
	}
	if err = appdb.Write(path, "state", "jobs", []byte(`[{"id":"job","state":"queued"}]`)); err != nil {
		t.Fatal(err)
	}
	if err = appdb.Write(path, "cache", "discovery/account/repo", []byte(`{"head":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	_ = registry.Close()
	_ = groups.Close()
	_ = decisions.Close()
	registry, err = repos.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	picked, err := registry.Repositories(ctx)
	if err != nil || len(picked) != 1 {
		t.Fatalf("picked %v %v", picked, err)
	}
	prefs, err := registry.Preferences(ctx)
	if err != nil || prefs.JobConcurrency != 2 {
		t.Fatalf("preferences %v %v", prefs, err)
	}
	groups, err = reviewgroups.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = groups.Close() }()
	listed, err := groups.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != group.ID {
		t.Fatalf("groups %v %v", listed, err)
	}
	decisions, err = decisionlog.Open(path, decisionlog.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decisions.Close() }()
	if session, ok, err := decisions.Session(ctx, "codex", "session"); err != nil || !ok || session.Coverage != decisionlog.CoverageSkipped {
		t.Fatalf("session %v %v %v", session, ok, err)
	}
	if state, err := inboxes.Open(path).Read(); err != nil || state.Revision != 1 {
		t.Fatalf("inboxes %v %v", state, err)
	}
	if raw, err := appdb.Read(path, "state", "jobs"); err != nil || !strings.Contains(string(raw), "queued") {
		t.Fatalf("jobs %s %v", raw, err)
	}
	db, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != appdb.Version {
		t.Fatalf("version %d %v", version, err)
	}
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %q %v", integrity, err)
	}
}

func TestConcurrentDocumentUpdatesAndFailedTransaction(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	const writers = 8
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				err := appdb.Update(path, "state", "counter", func(raw []byte) ([]byte, error) {
					n := 0
					if raw != nil {
						var err error
						n, err = strconv.Atoi(string(raw))
						if err != nil {
							return nil, err
						}
					}
					return []byte(strconv.Itoa(n + 1)), nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	failure := errors.New("abort")
	if err := appdb.Update(path, "state", "counter", func([]byte) ([]byte, error) { return []byte("wrong"), failure }); !errors.Is(err, failure) {
		t.Fatalf("failure %v", err)
	}
	raw, err := appdb.Read(path, "state", "counter")
	if err != nil || string(raw) != strconv.Itoa(writers*12) {
		t.Fatalf("counter %s %v", raw, err)
	}
	if _, err := appdb.Read(path, "cache", "counter"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bucket isolation %v", err)
	}
}

func TestMissingReadDoesNotCreateFilesAndFutureSchemaIsRefused(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "missing", "app.sqlite")
	if _, err := appdb.Read(path, "state", "x"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created parent: %v", err)
	}
	if err := appdb.Write(path, "state", "x", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err = appdb.Read(path, "state", "x"); err == nil {
		t.Fatal("read accepted future schema")
	}
	if err = appdb.Write(path, "state", "x", []byte("lost")); err == nil {
		t.Fatal("write accepted future schema")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var raw []byte
	if err = db.QueryRow("SELECT value FROM app_documents WHERE bucket='state' AND key='x'").Scan(&raw); err != nil || string(raw) != "kept" {
		t.Fatalf("future data %s %v", raw, err)
	}
}

func TestUnrecognizedDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE unrelated(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err = appdb.Open(path, false); err == nil {
		t.Fatal("adopted unrecognized schema")
	}
}

func TestDatabaseAndRollbackJournalArePrivate(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	db, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec("INSERT INTO app_documents(bucket,key,value) VALUES('state','private','secret')"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-journal"} {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		if !privatefs.PrivateFile(name, info) {
			t.Fatalf("not private: %s %v", name, info.Mode())
		}
	}
	// A read-only connection during a writer's transaction must leave it intact.
	reader, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxRevisionIsAtomicAcrossStores(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, err := inboxes.Open(path).Replace(0, nil); results <- err }()
	}
	close(start)
	successes, stale := 0, 0
	for range 2 {
		err := <-results
		var conflict *inboxes.StaleError
		switch {
		case err == nil:
			successes++
		case errors.As(err, &conflict):
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("successes %d stale %d", successes, stale)
	}
	state, err := inboxes.Open(path).Read()
	if err != nil || state.Revision != 1 {
		t.Fatalf("state %v %v", state, err)
	}
}

func TestMalformedBaselineIsRefusedWithoutChangingData(t *testing.T) {
	for _, mutation := range []string{"PRAGMA user_version=-1", "DROP TABLE app_documents"} {
		t.Run(mutation, func(t *testing.T) {
			path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
			db, err := appdb.Open(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, readOnly := range []bool{false, true} {
				if opened, err := appdb.Open(path, readOnly); err == nil {
					_ = opened.Close()
					t.Fatalf("accepted malformed schema, readOnly=%v", readOnly)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("changed malformed database: %v", err)
			}
		})
	}
}

// Group edits and feature synchronization read before writing. Reserving the
// writer at Begin prevents a competing document writer from making that
// transaction's later write fail immediately with SQLITE_BUSY.
func TestWritableTransactionsReserveBeforeReading(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	groups, err := reviewgroups.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = groups.Close() }()
	group, err := groups.Create(ctx, "owner/repo", "Original", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	first, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// A short timeout makes the competing reservation observable without sleep.
	if _, err = second.Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	tx, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var name string
	if err = tx.QueryRow("SELECT name FROM groups WHERE id=?", group.ID).Scan(&name); err != nil || name != "Original" {
		t.Fatalf("read %s %v", name, err)
	}
	if other, err := second.BeginTx(ctx, nil); err == nil {
		_ = other.Rollback()
		t.Fatal("a read-then-write transaction did not reserve its writer")
	} else if !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("competing writer %v", err)
	}
	reader, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = ro.QueryRow("SELECT name FROM groups WHERE id=?", group.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	_ = ro.Rollback()
	_ = reader.Close()
	if _, err = tx.Exec("UPDATE groups SET name='Changed' WHERE id=?", group.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	other, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec("INSERT INTO app_documents(bucket,key,value) VALUES('state','job','saved')"); err != nil {
		_ = other.Rollback()
		t.Fatal(err)
	}
	if err = other.Commit(); err != nil {
		t.Fatal(err)
	}
	if saved, err := groups.Get(ctx, group.ID); err != nil || saved.Name != "Changed" {
		t.Fatalf("group %v %v", saved, err)
	}
	if raw, err := appdb.Read(path, "state", "job"); err != nil || string(raw) != "saved" {
		t.Fatalf("document %s %v", raw, err)
	}
}
