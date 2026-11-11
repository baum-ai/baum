package sqlitepool

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func opener(path string, opens *atomic.Int32) func() (*sql.DB, bool, error) {
	return func() (*sql.DB, bool, error) {
		opens.Add(1)
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(10000)")
		return db, true, err
	}
}

// tempDir is a directory whose files enable closes before it is removed
// (Windows cannot remove an open file): call it first.
func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Enable()
	t.Cleanup(Disable)
	return dir
}

func create(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("CREATE TABLE t(v)"); err != nil {
		t.Fatal(err)
	}
}

// Off, every caller gets a handle of its own, closed on release.
func TestDisabledOpensForEachCaller(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	db, release, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := db.Ping(); err == nil {
		t.Fatal("released handle still open")
	}
	if _, release, err = Acquire(path, false, opener(path, &opens)); err != nil {
		t.Fatal(err)
	}
	release()
	if opens.Load() != 2 {
		t.Fatalf("%d opens", opens.Load())
	}
}

// On, concurrent callers share one handle, opened once, per path and mode.
func TestEnabledSharesOneHandle(t *testing.T) {
	path := filepath.Join(tempDir(t), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	var wg sync.WaitGroup
	handles := make(chan *sql.DB, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, release, err := Acquire(path, false, opener(path, &opens))
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			if _, err := db.Exec("INSERT INTO t VALUES(1)"); err != nil {
				t.Error(err)
			}
			handles <- db
		}()
	}
	wg.Wait()
	close(handles)
	first := <-handles
	for db := range handles {
		if db != first {
			t.Fatal("handles differ")
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("%d opens", opens.Load())
	}
	ro, release, err := Acquire(path, true, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if ro == first || opens.Load() != 2 {
		t.Fatal("read-only handle shared with the writable one")
	}
	if err := first.Ping(); err != nil {
		t.Fatal("kept handle closed", err)
	}
}

// A handle whose file left the path is not handed out again, and is closed
// once its last user releases it; a path with no file is opened but its
// handle is not kept.
func TestReplacedFileGetsANewHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open SQLite file cannot be removed on Windows")
	}
	path := filepath.Join(tempDir(t), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	old, releaseOld, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	// Connected to the file before it goes.
	if _, err := old.Exec("INSERT INTO t VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, release, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	if missing == old {
		t.Fatal("handle of a removed file reused")
	}
	release()
	if err := missing.Ping(); err == nil {
		t.Fatal("handle for a path with no file was kept")
	}
	// Still open for its user (SQLite itself refuses writes to a removed
	// database: SQLITE_READONLY_DBMOVED).
	var kept int
	if err := old.QueryRow("SELECT count(*) FROM t").Scan(&kept); err != nil || kept != 1 {
		t.Fatal("handle closed while in use", err)
	}
	releaseOld()
	if err := old.Ping(); err == nil {
		t.Fatal("retired handle not closed at its last release")
	}
	create(t, path)
	fresh, release, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	again, release, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if fresh != again || fresh == old {
		t.Fatal("new file's handle not kept")
	}
	var rows int
	if err := again.QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("new file read through the old handle: %d rows, %v", rows, err)
	}
}

// A handle open asks not to keep is closed at its last release.
func TestTransientHandleIsNotKept(t *testing.T) {
	path := filepath.Join(tempDir(t), "db.sqlite")
	create(t, path)
	transient := func() (*sql.DB, bool, error) {
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		return db, false, err
	}
	db, release, err := Acquire(path, true, transient)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := db.Ping(); err == nil {
		t.Fatal("transient handle kept")
	}
}

// A panic while opening reaches its caller, and later callers open again
// rather than wait for it.
func TestPanickingOpenDoesNotBlockOthers(t *testing.T) {
	path := filepath.Join(tempDir(t), "db.sqlite")
	create(t, path)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic swallowed")
			}
		}()
		_, _, _ = Acquire(path, false, func() (*sql.DB, bool, error) { panic("open failed") })
	}()
	done := make(chan error, 1)
	var opens atomic.Int32
	go func() {
		_, release, err := Acquire(path, false, opener(path, &opens))
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked behind a panicked open")
	}
}

// Handles unused for idleTimeout are closed.
func TestIdleHandlesClose(t *testing.T) {
	path := filepath.Join(tempDir(t), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	db, release, err := Acquire(path, false, opener(path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	CloseIdle(time.Hour)
	if err := db.Ping(); err != nil {
		t.Fatal("recently used handle closed")
	}
	time.Sleep(time.Millisecond)
	CloseIdle(time.Nanosecond)
	if err := db.Ping(); err == nil {
		t.Fatal("idle handle kept")
	}
}

// CloseUnder closes the handles of files under a directory, and only those.
func TestCloseUnder(t *testing.T) {
	root := tempDir(t)
	inside, outside := filepath.Join(root, "in", "db.sqlite"), filepath.Join(root, "out.sqlite")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	create(t, inside)
	create(t, outside)
	var opens atomic.Int32
	in, release, err := Acquire(inside, false, opener(inside, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	out, release, err := Acquire(outside, false, opener(outside, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	CloseUnder(filepath.Dir(inside))
	if in.Ping() == nil || out.Ping() != nil {
		t.Fatal("closed the wrong handles")
	}
}

// The write-ahead log works in an ordinary directory, and the probe leaves
// nothing behind.
func TestWALSupportedLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	if !WALSupported(dir) {
		t.Fatal("WAL reported unsupported in a temporary directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left %v (%v)", entries, err)
	}
}
