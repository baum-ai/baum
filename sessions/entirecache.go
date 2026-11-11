package sessions

import (
	"container/list"
	"io/fs"
	"slices"
	"sync"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/collector/entire"
)

// maxEntireListings bounds how many clones' Entire listings are kept. A
// listing holds an id and a tree id per checkpoint, about 100 bytes each.
const maxEntireListings = 32

// entireListings keeps one entire.Listing per clone, so a refresh whose
// checkpoint branch and refs did not move lists and dates them without git.
var entireListings = struct {
	sync.Mutex
	byRoot map[string]*entire.Listing
	order  []string // oldest first
}{byRoot: map[string]*entire.Listing{}}

// listingFor is the listing of the clone at root.
func listingFor(root string) *entire.Listing {
	entireListings.Lock()
	defer entireListings.Unlock()
	if listing, ok := entireListings.byRoot[root]; ok {
		return listing
	}
	if len(entireListings.order) >= maxEntireListings {
		delete(entireListings.byRoot, entireListings.order[0])
		entireListings.order = entireListings.order[1:]
	}
	listing := &entire.Listing{}
	entireListings.byRoot[root] = listing
	entireListings.order = append(entireListings.order, root)
	return listing
}

// DefaultCheckpointCacheBytes bounds the checkpoint bytes whose mapped
// sessions the checkpoint cache keeps. As for the mapping cache, a graph takes
// memory of the same order as the transcript it was mapped from, or less, so
// this is roughly the cache's resident cost.
const DefaultCheckpointCacheBytes = 64 << 20

// checkpointErrorCost is what a checkpoint that could not be parsed or
// mapped counts for: its outcome keeps only the error.
const checkpointErrorCost = 1 << 10

// checkpointCache keeps what reading a checkpoint whole gave: the sessions
// it maps to, or why it could not be parsed. A checkpoint is named by its
// tree, so its content cannot change under the same key; the key also names
// the clone, so one clone's reads never answer for another's. Only a
// checkpoint git exported in full is kept: one whose objects are missing, as
// in a partial clone, is read again, and reported again, next time. The cost
// of an entry is the size of the checkpoint's files, its transcripts above
// all; least recently used entries go first once the bytes held pass the
// limit, and a checkpoint larger than the limit is not kept. The mapped
// graphs are shared with every caller, which must not change them, as for
// Map.
type checkpointCache struct {
	mu      sync.Mutex
	limit   int64
	held    int64
	order   *list.List
	entries map[string]*list.Element
}

type checkpointEntry struct {
	key     string
	outcome readOutcome
	cost    int64
}

var checkpointReads = newCheckpointCache(DefaultCheckpointCacheBytes)

func init() {
	workstats.Gauge("sessions.checkpoint_cache.bytes", func() int64 {
		checkpointReads.mu.Lock()
		defer checkpointReads.mu.Unlock()
		return checkpointReads.held
	})
	workstats.Gauge("sessions.checkpoint_cache.entries", func() int64 {
		checkpointReads.mu.Lock()
		defer checkpointReads.mu.Unlock()
		return int64(checkpointReads.order.Len())
	})
}

func newCheckpointCache(limit int64) *checkpointCache {
	return &checkpointCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

// SetCheckpointCacheLimit changes how many checkpoint bytes the checkpoint
// cache keeps mapped sessions for; zero or less turns it off and empties it.
func SetCheckpointCacheLimit(limit int64) {
	checkpointReads.mu.Lock()
	defer checkpointReads.mu.Unlock()
	checkpointReads.limit = limit
	checkpointReads.evict()
}

// checkpointKey names one checkpoint's content in one clone.
func checkpointKey(root, tree string) string { return root + "\x00" + tree }

// get returns the cached outcome for key, with slices of its own, so a
// caller that appends to one changes nothing else.
func (c *checkpointCache) get(key string) (readOutcome, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return readOutcome{}, false
	}
	c.order.MoveToFront(element)
	outcome := element.Value.(*checkpointEntry).outcome
	outcome.results = slices.Clone(outcome.results)
	for i := range outcome.results {
		outcome.results[i].Warnings = slices.Clip(outcome.results[i].Warnings)
	}
	return outcome, true
}

// put keeps an outcome unless it alone passes the limit.
func (c *checkpointCache) put(key string, outcome readOutcome, cost int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cost > c.limit {
		return
	}
	outcome.results = slices.Clone(outcome.results)
	for i := range outcome.results {
		// Only what Load and Checkpoints.Load hand on is kept.
		outcome.results[i].Envelope = nil
		outcome.results[i].Warnings = slices.Clip(outcome.results[i].Warnings)
	}
	if old, ok := c.entries[key]; ok {
		c.held -= old.Value.(*checkpointEntry).cost
		c.order.Remove(old)
	}
	c.entries[key] = c.order.PushFront(&checkpointEntry{key: key, outcome: outcome, cost: cost})
	c.held += cost
	c.evict()
}

func (c *checkpointCache) evict() {
	for c.held > max(c.limit, 0) || (c.limit <= 0 && c.order.Len() > 0) {
		last := c.order.Back()
		if last == nil {
			return
		}
		entry := last.Value.(*checkpointEntry)
		c.order.Remove(last)
		delete(c.entries, entry.key)
		c.held -= entry.cost
	}
}

// checkpointBytes is the size of the files of an exported checkpoint.
func checkpointBytes(fsys fs.FS) int64 {
	var total int64
	_ = fs.WalkDir(fsys, ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
