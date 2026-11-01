package reviewindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// MaxAge bounds how long a snapshot is served without building again, for
// what neither an invalidation, the watcher's fingerprint nor a computed
// expiry covers, such as a clone becoming unavailable through a change the
// fingerprint does not stat, or a GitHub credential edited outside baum.
const MaxAge = 30 * time.Second

// Cache serves one built snapshot to every reader until it is invalidated or
// expires. Concurrent readers share one build, which runs under the cache's
// own context so a reader that gives up does not fail the others. Failed
// builds are not kept. Readers must not mutate a snapshot: it is shared.
type Cache struct {
	life  context.Context
	build func(context.Context, time.Time) (Snapshot, time.Time, error)
	now   func() time.Time

	mu         sync.Mutex
	generation uint64
	version    uint64
	current    *cached
	flight     *flight
	builds     uint64
}

type cached struct {
	snapshot Snapshot
	version  uint64
	expires  time.Time
}

type flight struct {
	generation uint64
	waiting    int // readers that joined, for tests
	done       chan struct{}
	snapshot   Snapshot
	version    uint64
	err        error
}

// NewCache caches s's snapshots. Builds stop when ctx ends.
func NewCache(ctx context.Context, s *Service) *Cache {
	return &Cache{life: ctx, build: s.build, now: time.Now}
}

// Invalidate makes the next reader build again. A build already running
// still answers the readers waiting on it, but its snapshot is not kept.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.generation++
	c.current = nil
	c.mu.Unlock()
}

// Snapshot is the current snapshot, built if there is none.
func (c *Cache) Snapshot(ctx context.Context) (Snapshot, error) {
	snapshot, _, err := c.Current(ctx)
	return snapshot, err
}

// Current is Snapshot with the version of the cached snapshot it returns,
// which moves each time the cache keeps a new one. It is 0 for a snapshot an
// invalidation kept out of the cache.
func (c *Cache) Current(ctx context.Context) (Snapshot, uint64, error) {
	c.mu.Lock()
	now := c.now()
	if v := c.current; v != nil && now.Before(v.expires) {
		c.mu.Unlock()
		return v.snapshot, v.version, nil
	}
	f := c.flight
	if f == nil || f.generation != c.generation {
		f = &flight{generation: c.generation, done: make(chan struct{})}
		c.flight = f
		c.builds++
		go c.run(f, now)
	}
	f.waiting++
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.snapshot, f.version, f.err
	case <-ctx.Done():
		return Snapshot{}, 0, ctx.Err()
	}
}

// Fresh reports the version of the cached snapshot while one is kept and
// has not expired.
func (c *Cache) Fresh() (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v := c.current; v != nil && c.now().Before(v.expires) {
		return v.version, true
	}
	return 0, false
}

func (c *Cache) run(f *flight, started time.Time) {
	ctx := c.life
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot, next, err := c.recovered(ctx, started)
	c.mu.Lock()
	if err == nil && f.generation == c.generation {
		expires := started.Add(MaxAge)
		if !next.IsZero() && next.Before(expires) {
			expires = next
		}
		c.version++
		f.version = c.version
		c.current = &cached{snapshot: snapshot, version: c.version, expires: expires}
	}
	f.snapshot, f.err = snapshot, err
	if c.flight == f {
		c.flight = nil
	}
	c.mu.Unlock()
	close(f.done)
}

// recovered builds with a panic turned into an error for the waiting
// readers, as net/http contains one in a handler.
func (c *Cache) recovered(ctx context.Context, started time.Time) (snapshot Snapshot, next time.Time, err error) {
	defer func() {
		if r := recover(); r != nil {
			snapshot, next, err = Snapshot{}, time.Time{}, fmt.Errorf("review index build panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return c.build(ctx, started)
}

// Handler serves GET /api/index as Service.Handler does, from the cache.
func (c *Cache) Handler() http.Handler { return handler(c.Snapshot) }

// Fingerprint digests what a build reads that may change with no
// invalidation, without running git or touching a store's files beyond stat:
// the registry rows (rows, which must not probe clones), identity (the
// GitHub account revision), and for each clone the size and modification
// time of its store under both roots store.Open chooses between and of its git
// config, which holds its remotes, and whether its working tree is still
// there. A missing file is part of the digest, not an error.
//
// Stores are not opened even to read their header: closing any descriptor
// of a SQLite file drops the locks this process's own connections hold on it.
func Fingerprint(rows func(context.Context) ([]repos.Repository, error), identity func() string) func(context.Context) string {
	return func(ctx context.Context) string {
		h := sha256.New()
		stat := func(path string, times bool) {
			info, err := os.Stat(path)
			switch {
			case err != nil:
				_, _ = fmt.Fprintf(h, "%q -\n", path)
			case times:
				_, _ = fmt.Fprintf(h, "%q %d %d %v\n", path, info.Size(), info.ModTime().UnixNano(), info.Mode())
			default:
				_, _ = fmt.Fprintf(h, "%q %v\n", path, info.Mode().Type())
			}
		}
		if identity != nil {
			_, _ = fmt.Fprintf(h, "identity %q\n", identity())
		}
		picked, err := rows(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(h, "registry: %v\n", err)
		}
		for _, repository := range picked {
			raw, _ := json.Marshal(repository)
			_, _ = fmt.Fprintf(h, "%s\n", raw)
			for _, clone := range repository.Clones {
				stat(clone.Path, false)
				stat(filepath.Join(clone.Path, ".git"), false)
				stat(filepath.Join(clone.CommonDir, "config"), true)
				roots, err := store.Roots(clone.CommonDir)
				if err != nil {
					_, _ = fmt.Fprintf(h, "%q roots: %v\n", clone.CommonDir, err)
					continue
				}
				for _, root := range roots {
					stat(filepath.Join(root, "versions.sqlite"), true)
				}
			}
		}
		return hex.EncodeToString(h.Sum(nil))
	}
}
