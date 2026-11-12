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

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/workstats"
)

// MaxAge bounds how long a snapshot is served without building again, for
// what neither an invalidation, the watcher's fingerprint nor a computed
// expiry covers, such as a clone becoming unavailable through a change the
// fingerprint does not stat, or a GitHub credential edited outside baum.
const MaxAge = 2 * time.Minute

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
	bodies     map[variant]encodedBody
	// parts are each repository's rows as the last builds read them.
	parts parts
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

// NewCache caches s's snapshots. Builds stop when ctx ends. A build after an
// invalidation reads again only the repositories whose discovery cache,
// clones or stores changed (parts.go).
func NewCache(ctx context.Context, s *Service) *Cache {
	c := &Cache{life: ctx, now: time.Now}
	c.build = func(ctx context.Context, now time.Time) (Snapshot, time.Time, error) {
		return s.build(ctx, now, &c.parts)
	}
	return c
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
		workstats.Inc("reviewindex.builds")
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
func (c *Cache) Handler() http.Handler { return handler(c.encoded) }

// encodedBody is a version JSON for one variant.
type encodedBody struct {
	version uint64
	body    etagjson.Body
}

// encoded is the JSON of the current snapshot, encoded once per cached
// version and variant. A snapshot an invalidation kept out of the cache
// (version 0) has no identity to key on, so it is encoded each time.
func (c *Cache) encoded(ctx context.Context, v variant) (etagjson.Body, error) {
	snapshot, version, err := c.Current(ctx)
	if err != nil {
		return etagjson.Body{}, err
	}
	if version != 0 {
		c.mu.Lock()
		e, ok := c.bodies[v]
		c.mu.Unlock()
		if ok && e.version == version {
			return e.body, nil
		}
	}
	body, err := v.encode(snapshot)
	if err != nil || version == 0 {
		return body, err
	}
	c.mu.Lock()
	if e, ok := c.bodies[v]; !ok || e.version < version {
		if c.bodies == nil {
			c.bodies = map[variant]encodedBody{}
		}
		c.bodies[v] = encodedBody{version: version, body: body}
	}
	c.mu.Unlock()
	return body, nil
}

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
		// A store's commits land in its write-ahead log first.
		statStore := func(path string) {
			info, err := os.Stat(path)
			stamp, stampErr := store.StampOf(path)
			if err != nil || stampErr != nil {
				_, _ = fmt.Fprintf(h, "%q -\n", path)
				return
			}
			stamp = store.SeenThroughHeartbeats(path, stamp)
			_, _ = fmt.Fprintf(h, "%q %d %d %d %d %v\n", path, stamp.Size, stamp.ModTime, stamp.WALSize, stamp.WALModTime, info.Mode())
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
					statStore(filepath.Join(root, "versions.sqlite"))
				}
			}
		}
		return hex.EncodeToString(h.Sum(nil))
	}
}
