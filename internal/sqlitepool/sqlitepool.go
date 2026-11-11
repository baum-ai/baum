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
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"time"
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
// and closed again on release, never kept.
func Acquire(path string, readOnly bool, open func() (*sql.DB, error)) (*sql.DB, func(), error) {
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
		db, err := open()
		mu.Lock()
		h.db, h.err = db, err
		close(h.ready)
		if err != nil {
			h.refs--
			if handles[k] == h {
				delete(handles, k)
			}
			mu.Unlock()
			return nil, nil, err
		}
		mu.Unlock()
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

func direct(open func() (*sql.DB, error)) (*sql.DB, func(), error) {
	db, err := open()
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
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

func closeAll(dbs []*sql.DB) {
	for _, db := range dbs {
		_ = db.Close()
	}
}
