package appdb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/sqlitepool"
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
	if _, err = inboxes.Open(path).Read(); err != nil {
		t.Fatal(err)
	}
	// A journal from before the jobs table: the queue moves it into rows.
	if err = appdb.Write(path, "state", "jobs", []byte(`[{"id":"job","state":"completed","updated_at":"`+time.Now().UTC().Format(time.RFC3339)+`"}]`)); err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.Open(path, idleExecutor{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	queue.Close()
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
	if raw, err := appdb.Read(path, "state", "jobs"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("jobs document kept: %s %v", raw, err)
	}
	queue, err = jobs.Open(path, idleExecutor{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if listed := queue.List(); len(listed) != 1 || listed[0].ID != "job" || listed[0].State != "completed" {
		t.Fatalf("jobs %+v", listed)
	}
	queue.Close()
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

// idleExecutor runs nothing: the baseline queue holds only finished jobs.
type idleExecutor struct{}

func (idleExecutor) Prepare(context.Context, jobs.Item) (jobs.Prepared, string, error) {
	return jobs.Prepared{}, "", errors.New("not used")
}
func (idleExecutor) Execute(context.Context, jobs.Prepared, func(jobs.Progress)) (*float64, error) {
	return nil, errors.New("not used")
}

func TestConcurrentDocumentUpdatesAndFailedTransaction(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	// SQLite's busy wait is not fair: a writer can lose every retry for as long
	// as the others keep the lock busy. Keep the serialized work well under the
	// 10 s busy_timeout; 96 updates took over 12 s on a Windows runner.
	const writers, updates = 4, 12
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range updates {
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
	if err != nil || string(raw) != strconv.Itoa(writers*updates) {
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

func TestDatabaseAndWriteAheadLogArePrivate(t *testing.T) {
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
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
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
	if _, err := inboxes.Open(path).Read(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := inboxes.Open(path).Replace(1, []inboxes.Inbox{{Name: "A", Color: "blue", What: "any", Risk: "any"}})
			results <- err
		}()
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
	if err != nil || state.Revision != 2 {
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

// The database switches to the write-ahead log at its first write-open, and
// keeps it: a plain connection, as an older baum makes, finds it in that mode
// and reads it.
func TestWriteOpenSwitchesToTheWriteAheadLog(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	if err := appdb.Write(path, "state", "x", []byte("1")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q %v", mode, err)
	}
	var raw []byte
	if err = db.QueryRow("SELECT value FROM app_documents WHERE bucket='state' AND key='x'").Scan(&raw); err != nil || string(raw) != "1" {
		t.Fatalf("plain read %s %v", raw, err)
	}
}

// A read-only open reads what a writer committed while the writer is still
// connected (the commit is only in the log), and after it closed.
func TestReadOnlyOpenOfWriteAheadLogDatabase(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	writer, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec("INSERT INTO app_documents(bucket,key,value) VALUES('state','x','logged')"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("commit not in the log: %v", err)
	}
	if raw, err := appdb.Read(path, "state", "x"); err != nil || string(raw) != "logged" {
		t.Fatalf("read beside a writer %s %v", raw, err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := appdb.Read(path, "state", "x"); err != nil || string(raw) != "logged" {
		t.Fatalf("read after the writer closed %s %v", raw, err)
	}
	if _, err := appdb.Read(path, "state", "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing %v", err)
	}
}

// A reader's open transaction does not hold up a writer, and keeps reading
// the snapshot it began with until it ends.
func TestReaderDoesNotBlockWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	if err := appdb.Write(path, "state", "x", []byte("before")); err != nil {
		t.Fatal(err)
	}
	reader, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	read := func() string {
		t.Helper()
		var raw []byte
		if err := tx.QueryRow("SELECT value FROM app_documents WHERE bucket='state' AND key='x'").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if got := read(); got != "before" {
		t.Fatal(got)
	}
	start := time.Now()
	written := make(chan error, 1)
	go func() { written <- appdb.Write(path, "state", "x", []byte("after")) }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("the writer waited for a reader")
	}
	// With a rollback journal it would wait for the reader until busy_timeout
	// (10 s) and fail.
	if waited := time.Since(start); waited > 8*time.Second {
		t.Fatalf("the writer waited %v", waited)
	}
	if got := read(); got != "before" {
		t.Fatalf("snapshot changed under a reader: %s", got)
	}
	_ = tx.Rollback()
	if raw, err := appdb.Read(path, "state", "x"); err != nil || string(raw) != "after" {
		t.Fatalf("after %s %v", raw, err)
	}
}

// With shared handles on, a reader keeps one handle and still sees what
// another connection commits; a database replaced at the path is opened
// again rather than read through the old handle.
func TestSharedHandleSeesOtherWritersAndFollowsTheFile(t *testing.T) {
	// After TempDir, so the kept handles close before it is removed.
	dir := appdbtest.TempDir(t)
	sqlitepool.Enable()
	t.Cleanup(sqlitepool.Disable)
	path := filepath.Join(dir, "app.sqlite")
	if err := appdb.Write(path, "state", "x", []byte("1")); err != nil {
		t.Fatal(err)
	}
	first, release, err := appdb.Acquire(path, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	again, release, err := appdb.Acquire(path, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if again != first {
		t.Fatal("handle not reused")
	}
	// Another process's connection.
	other, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec("UPDATE app_documents SET value='2' WHERE bucket='state' AND key='x'"); err != nil {
		t.Fatal(err)
	}
	if raw, err := appdb.Read(path, "state", "x"); err != nil || string(raw) != "2" {
		t.Fatalf("other writer not seen %s %v", raw, err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// An open SQLite file cannot be renamed there.
		return
	}
	moved := filepath.Join(dir, "moved.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, moved+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := appdb.Write(path, "state", "x", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if raw, err := appdb.Read(path, "state", "x"); err != nil || string(raw) != "new" {
		t.Fatalf("replaced database read through the old handle: %s %v", raw, err)
	}
	moved2, release, err := appdb.Acquire(path, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if moved2 == first {
		t.Fatal("old handle kept for a new file")
	}
}

func TestVersionOneUpgradeAddsGroupRepositoryIndex(t *testing.T) {
	for _, withGroups := range []bool{false, true} {
		t.Run(strconv.FormatBool(withGroups), func(t *testing.T) {
			path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
			if err := appdb.Write(path, "state", "kept", []byte("value")); err != nil {
				t.Fatal(err)
			}
			if withGroups {
				groups, err := reviewgroups.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = groups.Create(context.Background(), "org/repo", "Pair", []int{1, 2}); err != nil {
					t.Fatal(err)
				}
				_ = groups.Close()
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("DROP INDEX IF EXISTS groups_repository; PRAGMA user_version=1"); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if db, err := appdb.Open(path, true); err == nil {
				_ = db.Close()
				t.Fatal("read-only open silently upgraded the schema")
			}
			db, err = appdb.Open(path, false)
			if err != nil {
				t.Fatal(err)
			}
			var version, indices int
			if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != appdb.Version {
				t.Fatalf("version %d %v", version, err)
			}
			if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='groups_repository'").Scan(&indices); err != nil {
				t.Fatal(err)
			}
			if withGroups && indices != 1 || !withGroups && indices != 0 {
				t.Fatalf("indices %d", indices)
			}
			_ = db.Close()
			if raw, err := appdb.Read(path, "state", "kept"); err != nil || string(raw) != "value" {
				t.Fatalf("document %s %v", raw, err)
			}
			groups, err := reviewgroups.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = groups.Close() }()
			listed, err := groups.List(context.Background())
			if err != nil || withGroups && len(listed) != 1 || !withGroups && len(listed) != 0 {
				t.Fatalf("groups %+v %v", listed, err)
			}
			// Every stack lookup must search the repository index rather than scan
			// all repositories while its immediate transaction holds the writer.
			db, err = appdb.Open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			rows, err := db.Query("EXPLAIN QUERY PLAN SELECT id FROM groups WHERE repository=? AND deleted=0 ORDER BY created,id", "org/repo")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			used := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				used = used || strings.Contains(detail, "SEARCH groups USING") && strings.Contains(detail, "groups_repository")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !used {
				t.Fatal("repository lookup did not use its index")
			}
		})
	}
}

func TestFailedVersionOneUpgradeRollsBack(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	if err := appdb.Write(path, "state", "kept", []byte("value")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A malformed group table makes creating the index fail. The migration
	// must leave the schema version and existing records alone.
	if _, err = db.Exec("CREATE TABLE groups(id TEXT); PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if opened, err := appdb.Open(path, false); err == nil {
		_ = opened.Close()
		t.Fatal("upgrade accepted a malformed group table")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("version %d %v", version, err)
	}
	var raw string
	if err = db.QueryRow("SELECT value FROM app_documents WHERE bucket='state' AND key='kept'").Scan(&raw); err != nil || raw != "value" {
		t.Fatalf("document %s %v", raw, err)
	}
}
