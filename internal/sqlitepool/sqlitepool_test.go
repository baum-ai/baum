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

func opener(t *testing.T, path string, opens *atomic.Int32) func() (*sql.DB, error) {
	t.Helper()
	return func() (*sql.DB, error) {
		opens.Add(1)
		return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(10000)")
	}
}

func enable(t *testing.T) {
	t.Helper()
	Enable()
	t.Cleanup(Disable)
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
	db, release, err := Acquire(path, false, opener(t, path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := db.Ping(); err == nil {
		t.Fatal("released handle still open")
	}
	if _, release, err = Acquire(path, false, opener(t, path, &opens)); err != nil {
		t.Fatal(err)
	}
	release()
	if opens.Load() != 2 {
		t.Fatalf("%d opens", opens.Load())
	}
}

// On, concurrent callers share one handle, opened once, per path and mode.
func TestEnabledSharesOneHandle(t *testing.T) {
	enable(t)
	path := filepath.Join(t.TempDir(), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	var wg sync.WaitGroup
	handles := make(chan *sql.DB, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, release, err := Acquire(path, false, opener(t, path, &opens))
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
	ro, release, err := Acquire(path, true, opener(t, path, &opens))
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
// once its last user releases it.
func TestReplacedFileGetsANewHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open SQLite file cannot be removed on Windows")
	}
	enable(t)
	path := filepath.Join(t.TempDir(), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	old, releaseOld, err := Acquire(path, false, opener(t, path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A missing file is opened (here: created) but not kept.
	created, release, err := Acquire(path, false, opener(t, path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	if created == old {
		t.Fatal("handle of a removed file reused")
	}
	release()
	if err := created.Ping(); err == nil {
		t.Fatal("handle of a file that did not exist was kept")
	}
	if err := old.Ping(); err != nil {
		t.Fatal("handle closed while in use", err)
	}
	releaseOld()
	if err := old.Ping(); err == nil {
		t.Fatal("retired handle not closed at its last release")
	}
	fresh, release, err := Acquire(path, false, opener(t, path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	again, release, err := Acquire(path, false, opener(t, path, &opens))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if fresh != again || fresh == old {
		t.Fatal("new file's handle not kept")
	}
}

// Handles unused for idleTimeout are closed.
func TestIdleHandlesClose(t *testing.T) {
	enable(t)
	path := filepath.Join(t.TempDir(), "db.sqlite")
	create(t, path)
	var opens atomic.Int32
	db, release, err := Acquire(path, false, opener(t, path, &opens))
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
