// Package sqlitepool keeps one database handle per SQLite file and access
// mode for the life of a process, for callers that open the file for each
// operation. A process turns it on once (Enable); until then Acquire opens a
// new handle for every caller and closes it on release, as those callers did
// before. Test binaries leave it off, so nothing keeps their temporary
// directories' files open (Windows cannot delete an open file).
//
// A kept handle is reused only while the path still names the file it was
// opened on, with the same mode: a database deleted, replaced by another
// file or made read-only is opened again, never read or written through the
// old handle. A handle nobody used for idleTimeout is closed. A file
// rewritten in place by something other than SQLite is not noticed.
package sqlitepool

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// idleTimeout is how long an unused handle stays open; sweepEvery is how
// often Acquire looks for those. Variables for tests.
var idleTimeout, sweepEvery = 5 * time.Minute, time.Minute

var (
	enabled atomic.Bool
	mu      sync.Mutex
	handles = map[key]*handle{}
	swept   time.Time
)

type key struct {
	path     string
	readOnly bool
}

type handle struct {
	// ready is closed once db or err is set.
	ready chan struct{}
	db    *sql.DB
	err   error
	// file is the database file the handle was opened on.
	file os.FileInfo
	refs int
	used time.Time
	// retired handles are out of handles: the last release closes them.
	retired bool
}

// Enable keeps handles open from now on. The daemon and the command line
// call it once at startup.
func Enable() { enabled.Store(true) }

// Enabled reports whether handles are kept.
func Enabled() bool { return enabled.Load() }

// Disable closes every kept handle not in use, and the others when they are
// released, and stops keeping them. For tests.
func Disable() {
	enabled.Store(false)
	CloseIdle(0)
}

// Acquire returns a handle for the SQLite file at path, opened by open when
// none is kept, and the function that releases it. The handle is shared:
// callers must not close it, change its pool settings or leave a
// transaction open past release. A path that does not exist (yet) is opened
// and closed again on release, never kept; so is a handle open reports not
// to keep (keep false).
func Acquire(path string, readOnly bool, open func() (db *sql.DB, keep bool, err error)) (*sql.DB, func(), error) {
	if !enabled.Load() {
		return direct(open)
	}
	info, statErr := os.Stat(path)
	k := key{path, readOnly}
	now := time.Now()
	mu.Lock()
	closing := sweep(now)
	h := handles[k]
	if h != nil && (statErr != nil || !os.SameFile(h.file, info) || h.file.Mode() != info.Mode()) {
		closing = append(closing, retire(h)...)
		h = nil
	}
	if statErr != nil {
		mu.Unlock()
		closeAll(closing)
		return direct(open)
	}
	if h == nil {
		h = &handle{ready: make(chan struct{}), file: info, refs: 1, used: now}
		handles[k] = h
		mu.Unlock()
		closeAll(closing)
		db, err := h.open(k, open)
		if err != nil {
			return nil, nil, err
		}
		return db, h.release, nil
	}
	h.refs++
	mu.Unlock()
	closeAll(closing)
	<-h.ready
	if h.err != nil {
		mu.Lock()
		h.refs--
		mu.Unlock()
		return nil, nil, h.err
	}
	return h.db, h.release, nil
}

// open opens h's database; it settles h even when open panics, so callers
// waiting for it never wait forever.
func (h *handle) open(k key, open func() (*sql.DB, bool, error)) (db *sql.DB, err error) {
	keep := false
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("opening %s: %v", k.path, r)
			defer panic(r)
		}
		mu.Lock()
		h.db, h.err = db, err
		close(h.ready)
		if err != nil {
			h.refs--
		}
		if err != nil || !keep {
			if handles[k] == h {
				delete(handles, k)
			}
			h.retired = true
		}
		mu.Unlock()
	}()
	db, keep, err = open()
	return db, err
}

func direct(open func() (*sql.DB, bool, error)) (*sql.DB, func(), error) {
	db, _, err := open()
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}

// CloseUnder closes the kept handles of files under dir: now those not in
// use, the others at their last release. A caller removing dir calls it
// first, since an open file cannot be removed on Windows.
func CloseUnder(dir string) {
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	mu.Lock()
	var closing []*sql.DB
	for k, h := range handles {
		if strings.HasPrefix(k.path, prefix) {
			closing = append(closing, retire(h)...)
		}
	}
	mu.Unlock()
	closeAll(closing)
}

func (h *handle) release() {
	mu.Lock()
	h.refs--
	h.used = time.Now()
	var db *sql.DB
	if h.retired && h.refs == 0 {
		db = h.db
	}
	mu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}

// retire takes h out of handles; it returns h's database when nobody uses it,
// for the caller to close outside mu. mu is held.
func retire(h *handle) []*sql.DB {
	for k, kept := range handles {
		if kept == h {
			delete(handles, k)
		}
	}
	h.retired = true
	if h.refs == 0 && h.db != nil {
		return []*sql.DB{h.db}
	}
	return nil
}

// sweep retires the handles unused for idleTimeout, at most every sweepEvery.
// mu is held.
func sweep(now time.Time) []*sql.DB {
	if now.Sub(swept) < sweepEvery {
		return nil
	}
	swept = now
	return idle(now, idleTimeout)
}

func idle(now time.Time, after time.Duration) []*sql.DB {
	var closing []*sql.DB
	for _, h := range handles {
		if h.refs == 0 && h.db != nil && now.Sub(h.used) >= after {
			closing = append(closing, retire(h)...)
		}
	}
	return closing
}

// CloseIdle closes the kept handles unused for at least after; 0 closes
// every handle not in use now, and the others when they are released.
func CloseIdle(after time.Duration) {
	mu.Lock()
	var closing []*sql.DB
	if after == 0 {
		for _, h := range handles {
			closing = append(closing, retire(h)...)
		}
	} else {
		closing = idle(time.Now(), after)
	}
	mu.Unlock()
	closeAll(closing)
}

var walDirs sync.Map

// WALSupported reports whether SQLite's write-ahead log works in dir: a
// scratch database there is switched to WAL, written and read back, then
// removed. The log's -shm index is memory-mapped, which some file systems
// (FUSE mounts, some network and VM shares) refuse; a database switched to
// WAL there could not be opened again, by this binary or an older one. The
// answer is kept per directory for the life of the process.
func WALSupported(dir string) bool {
	dir = filepath.Clean(dir)
	if ok, found := walDirs.Load(dir); found {
		return ok.(bool)
	}
	ok := probeWAL(dir)
	walDirs.Store(dir, ok)
	return ok
}

func probeWAL(dir string) bool {
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return false
	}
	path := filepath.Join(dir, ".baum-wal-probe-"+hex.EncodeToString(name[:])+".sqlite")
	// Created private, as the databases beside it are; SQLite gives its -wal
	// and -shm the same mode.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	defer func() {
		for _, suffix := range []string{"-wal", "-shm", ""} {
			_ = os.Remove(path + suffix)
		}
	}()
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: uriPath}).String())
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	var mode string
	var value int
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		return false
	}
	if _, err := db.Exec("CREATE TABLE probe(v); INSERT INTO probe VALUES(1)"); err != nil {
		return false
	}
	return db.QueryRow("SELECT v FROM probe").Scan(&value) == nil && value == 1
}

func closeAll(dbs []*sql.DB) {
	for _, db := range dbs {
		_ = db.Close()
	}
}
