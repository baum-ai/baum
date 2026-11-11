package codex

import (
	"os"
	"sync"
	"time"
)

// Shared is the process-wide cache the collector uses when a caller passes none,
// so every discovery in the daemon reuses what the others read.
var Shared = &Cache{}

// Cache remembers what the first record of each rollout said, keyed on its
// path, size and modification time, so a scan of an unchanged store does not
// open the files again. Each scan drops the entries of its store that it did not
// meet (deleted rollouts). Only what discovery uses is kept: the first line of a
// rollout is often 20 KB, most of it instructions discovery never reads.
//
// Between BeginPass and EndPass the scan of a store is also done once: later
// lookups of the same store reuse its index. Use a pass for work that is
// content with the store as it was when the pass began, such as one analytics
// refresh over many clones.
//
// A nil *Cache caches nothing. It is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	gen     uint64
	reads   int
	entries map[string]cached
	// passes counts the open passes; memo is the scan of each store made in
	// them, by sessions directory.
	passes int
	memo   map[string]*loaded
}

type cached struct {
	home    string
	size    int64
	modTime time.Time
	entry   Entry
	gen     uint64
}

// Reads is how many rollout first records were read from disk rather than
// served from the cache.
func (c *Cache) Reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// Len is how many rollouts the cache holds a first record for.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// BeginPass starts a pass: until EndPass, the store of a sessions directory is
// scanned once however many times it is looked up. Passes may nest or overlap.
func (c *Cache) BeginPass() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.passes++
	c.mu.Unlock()
}

// EndPass ends a pass begun by BeginPass. When the last one ends the scans it
// kept are forgotten.
func (c *Cache) EndPass() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.passes > 0 {
		c.passes--
	}
	if c.passes == 0 {
		c.memo = nil
	}
	c.mu.Unlock()
}

// index scans the store of sessionsDir, or returns the scan a pass kept.
func (c *Cache) index(sessionsDir string) *loaded {
	if c == nil {
		return loadIndex(sessionsDir, nil)
	}
	c.mu.Lock()
	if l, ok := c.memo[sessionsDir]; ok {
		c.mu.Unlock()
		return l
	}
	inPass := c.passes > 0
	c.mu.Unlock()
	l := loadIndex(sessionsDir, c)
	if inPass {
		c.mu.Lock()
		if c.passes > 0 {
			if c.memo == nil {
				c.memo = map[string]*loaded{}
			}
			c.memo[sessionsDir] = l
		}
		c.mu.Unlock()
	}
	return l
}

// scan is one walk of a store reading first records through a cache.
type scan struct {
	cache *Cache
	home  string
	gen   uint64
}

func (c *Cache) beginScan(home string) *scan {
	s := &scan{cache: c, home: home}
	if c != nil {
		c.mu.Lock()
		c.gen++
		s.gen = c.gen
		c.mu.Unlock()
	}
	return s
}

// read returns the rollout's entry, from the cache when its size and
// modification time are those it was read at. Failures are not cached.
func (s *scan) read(path string) (Entry, error) {
	c := s.cache
	if c == nil {
		return readEntry(path)
	}
	info, statErr := os.Stat(path)
	if statErr == nil {
		c.mu.Lock()
		e, ok := c.entries[path]
		if ok && e.size == info.Size() && e.modTime.Equal(info.ModTime()) {
			e.gen = max(e.gen, s.gen)
			c.entries[path] = e
			c.mu.Unlock()
			return e.entry, nil
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	entry, err := readEntry(path)
	if err != nil || statErr != nil {
		return entry, err
	}
	// The file may have grown between the stat and the read. The first record
	// does not change when it does, and the entry is read again at the new size.
	entry = trimmed(entry)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]cached{}
	}
	c.entries[path] = cached{home: s.home, size: info.Size(), modTime: info.ModTime(), entry: entry, gen: s.gen}
	c.mu.Unlock()
	return entry, nil
}

// end drops the entries of the scanned store that no scan since this one began
// asked for.
func (s *scan) end() {
	c := s.cache
	if c == nil {
		return
	}
	c.mu.Lock()
	for path, e := range c.entries {
		if e.home == s.home && e.gen < s.gen {
			delete(c.entries, path)
		}
	}
	c.mu.Unlock()
}

// trimmed is entry without the raw JSON of its metadata, which discovery does
// not read.
func trimmed(entry Entry) Entry {
	if entry.Meta == nil {
		return entry
	}
	meta := *entry.Meta
	meta.Raw, meta.HistoryBase, meta.Source.Raw = nil, nil, nil
	entry.Meta = &meta
	return entry
}
