package sessions

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/session"
)

// DefaultMapCacheBytes bounds the transcript bytes whose mapped graphs Map
// keeps. A graph takes memory of the same order as its transcript, so this is
// roughly the cache's resident cost.
const DefaultMapCacheBytes = 64 << 20

// mapCache keeps the mapped graphs of recently read sessions, so a daemon
// that reads the same transcripts on every refresh decodes each only once
// until it changes. An entry is keyed on everything the mapping reads: the
// source and, for each file, its name, kind, path, size and modification
// time, or the hash of generated content. A file that changed gets a new key;
// the old entry ages out. Least recently used entries go first once the
// transcript bytes held pass the limit.
type mapCache struct {
	mu    sync.Mutex
	limit int64
	// holds counts HoldMappings calls not yet released; while there is one,
	// the cache keeps up to HeldMapCacheBytes.
	holds   int
	held    int64
	order   *list.List
	entries map[string]*list.Element
	// misses counts mappings run; tests read it.
	misses int
}

type mapEntry struct {
	key   string
	graph *session.Graph
	notes []string
	err   error
	cost  int64
}

var cache = newMapCache(DefaultMapCacheBytes)

func init() {
	workstats.Gauge("sessions.map_cache.bytes", func() int64 {
		c := cache
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.held
	})
	workstats.Gauge("sessions.map_cache.entries", func() int64 {
		c := cache
		c.mu.Lock()
		defer c.mu.Unlock()
		return int64(c.order.Len())
	})
}

func newMapCache(limit int64) *mapCache {
	return &mapCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

// HeldMapCacheBytes is the cache's limit while HoldMappings holds it.
const HeldMapCacheBytes = 256 << 20

// HoldMappings lets the cache keep up to HeldMapCacheBytes until release is
// called, then trims it back to its limit. A caller about to load the same
// sessions several times in a row, such as a background pass refreshing
// several pull requests of one clone, holds it so each session is decoded
// once in the pass, at the cost of that memory for the pass's duration.
func HoldMappings() (release func()) {
	cache.mu.Lock()
	cache.holds++
	cache.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			cache.holds--
			cache.evict()
		})
	}
}

// effectiveLimit is the limit, raised while a hold is active.
func (c *mapCache) effectiveLimit() int64 {
	if c.holds > 0 && c.limit > 0 {
		return max(c.limit, HeldMapCacheBytes)
	}
	return c.limit
}

// SetMapCacheLimit changes how many transcript bytes Map keeps mapped
// graphs for; zero or less turns the cache off and empties it.
func SetMapCacheLimit(limit int64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.limit = limit
	cache.evict()
}

// mapKey describes the session's input as it stands now, and its size in
// bytes. ok is false when a file cannot be described, and the session is then
// mapped without the cache.
func mapKey(s collector.Session) (key string, cost int64, ok bool) {
	var b strings.Builder
	b.WriteString(string(s.Source))
	for _, f := range s.Files {
		fmt.Fprintf(&b, "\x00%s\x00%s\x00", f.Name, f.Kind)
		if f.Data != nil {
			sum := sha256.Sum256(f.Data)
			fmt.Fprintf(&b, "data:%s", hex.EncodeToString(sum[:]))
			cost += int64(len(f.Data))
			continue
		}
		info, err := os.Stat(f.Path)
		if err != nil || !info.Mode().IsRegular() {
			return "", 0, false
		}
		fmt.Fprintf(&b, "file:%s\x00%d\x00%d", f.Path, info.Size(), info.ModTime().UnixNano())
		cost += info.Size()
	}
	return b.String(), cost, true
}

// get returns the cached mapping for key.
func (c *mapCache) get(key string) (*mapEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		c.misses++
		return nil, false
	}
	workstats.Inc("sessions.map_cache.hits")
	c.order.MoveToFront(element)
	return element.Value.(*mapEntry), true
}

// put keeps a mapping unless it alone passes the limit.
func (c *mapCache) put(entry *mapEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.cost > c.effectiveLimit() {
		return
	}
	if old, ok := c.entries[entry.key]; ok {
		c.held -= old.Value.(*mapEntry).cost
		c.order.Remove(old)
	}
	c.entries[entry.key] = c.order.PushFront(entry)
	c.held += entry.cost
	c.evict()
}

func (c *mapCache) evict() {
	limit := c.effectiveLimit()
	for c.held > max(limit, 0) || (limit <= 0 && c.order.Len() > 0) {
		last := c.order.Back()
		if last == nil {
			return
		}
		entry := last.Value.(*mapEntry)
		c.order.Remove(last)
		delete(c.entries, entry.key)
		c.held -= entry.cost
	}
}
