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

// DefaultMapCacheBytes bounds the estimated memory of the mapped graphs Map
// keeps (graphBytes). A graph takes about a sixth of its transcript's size,
// so this holds the graphs of several hundred MB of transcripts.
const DefaultMapCacheBytes = 64 << 20

// mapCache keeps the mapped graphs of recently read sessions, so a daemon
// that reads the same transcripts on every refresh decodes each only once
// until it changes. An entry is keyed on everything the mapping reads: the
// source and, for each file, its name, kind, path, size and modification
// time, or the hash of generated content. A file that changed gets a new key,
// and keeping the new mapping drops the one of the same session's files as
// they were. Least recently used entries go first once the estimated memory
// of the graphs held passes the limit.
type mapCache struct {
	mu    sync.Mutex
	limit int64
	// holds counts HoldMappings calls not yet released; while there is one,
	// the cache keeps up to HeldMapCacheBytes.
	holds int
	// held is the estimated memory of the entries held; transcripts is the
	// transcript bytes they were mapped from.
	held        int64
	transcripts int64
	order       *list.List
	entries     map[string]*list.Element
	// sessions finds the entry of a session and file list (mapIdentity),
	// whatever their sizes, so a newer mapping replaces it.
	sessions map[string]*list.Element
	// misses counts mappings run; tests read it.
	misses int
}

type mapEntry struct {
	key string
	// session is the entry's mapIdentity; empty when it has none.
	session string
	graph   *session.Graph
	notes   []string
	err     error
	// cost is the entry's estimated memory; transcript the bytes of its files.
	cost       int64
	transcript int64
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
	workstats.Gauge("sessions.map_cache.transcript_bytes", func() int64 {
		c := cache
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.transcripts
	})
}

func newMapCache(limit int64) *mapCache {
	return &mapCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}, sessions: map[string]*list.Element{}}
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

// SetMapCacheLimit changes the estimated memory of the mapped graphs Map
// keeps; zero or less turns the cache off and empties it.
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

// mapIdentity names the session and its files without their content, so two
// mappings of one session's files at different sizes share it. A session
// with generated content has none.
func mapIdentity(s collector.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s", s.Source, s.SessionID)
	for _, f := range s.Files {
		if f.Data != nil {
			return ""
		}
		fmt.Fprintf(&b, "\x00%s\x00%s\x00%s", f.Name, f.Kind, f.Path)
	}
	return b.String()
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
		c.remove(old)
	}
	// The same session's files read at other sizes: they changed since, and
	// that entry will not be asked for again.
	if old, ok := c.sessions[entry.session]; ok && entry.session != "" {
		c.remove(old)
	}
	element := c.order.PushFront(entry)
	c.entries[entry.key] = element
	if entry.session != "" {
		c.sessions[entry.session] = element
	}
	c.held += entry.cost
	c.transcripts += entry.transcript
	c.evict()
}

func (c *mapCache) remove(element *list.Element) {
	entry := element.Value.(*mapEntry)
	c.order.Remove(element)
	delete(c.entries, entry.key)
	if c.sessions[entry.session] == element {
		delete(c.sessions, entry.session)
	}
	c.held -= entry.cost
	c.transcripts -= entry.transcript
}

func (c *mapCache) evict() {
	limit := c.effectiveLimit()
	for c.held > max(limit, 0) || (limit <= 0 && c.order.Len() > 0) {
		last := c.order.Back()
		if last == nil {
			return
		}
		c.remove(last)
	}
}
