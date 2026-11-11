package uiserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/viewserver"
)

// citing is a document citing a.txt, which a patch request may ask for.
func citing(n int) []byte {
	return fmt.Appendf(nil, `{"review":{"repository":"owner/repo","head":"aaaaaaaa","base":"bbbbbbbb","version":%d},"index":{"hunks":[{"path":"a.txt"}]}}`, n)
}

// closings records the viewers a cache closes.
type closings struct {
	mu     sync.Mutex
	closed map[*viewserver.Server]bool
}

func (c *closings) has(server *viewserver.Server) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed[server]
}

// testCache answers every patch at once and records the viewers it closes.
func testCache(t *testing.T) (*documentCache, *closings) {
	t.Helper()
	cache := newDocumentCache()
	t.Cleanup(cache.close)
	cache.patch = func(context.Context, locallink.PatchRequest) (locallink.PatchResult, error) {
		return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-a\n+b\n"}, nil
	}
	seen := &closings{closed: map[*viewserver.Server]bool{}}
	cache.onClose = func(server *viewserver.Server) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		if seen.closed[server] {
			t.Error("viewer closed twice")
		}
		seen.closed[server] = true
	}
	return cache, seen
}

// answers reports whether the viewer answers a patch request.
func answers(t *testing.T, server *viewserver.Server) bool {
	t.Helper()
	w := httptest.NewRecorder()
	server.Mux().ServeHTTP(w, httptest.NewRequest("GET", "/api/patch?path=a.txt", nil))
	return w.Code == 200
}

func (c *documentCache) kept() (entries, revisions int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), len(c.revisions)
}

// Concurrent requests for one document share one build; each still gets the
// viewer, and later requests by its revision find it without the document.
func TestDocumentCacheBuildsADocumentOnce(t *testing.T) {
	cache, _ := testCache(t)
	clone := repos.Clone{ID: "one", Repository: "owner/repo"}
	var wg sync.WaitGroup
	servers := make([]*viewserver.Server, 16)
	for i := range servers {
		wg.Go(func() {
			server, release, err := cache.get(context.Background(), clone, citing(1), "rev-1")
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			servers[i] = server
			if !answers(t, server) {
				t.Error("viewer does not answer")
			}
		})
	}
	wg.Wait()
	for _, server := range servers {
		if server != servers[0] {
			t.Fatal("concurrent requests built separate viewers")
		}
	}
	server, release, ok, err := cache.cached(context.Background(), "rev-1")
	if err != nil || !ok || server != servers[0] {
		t.Fatal("revision did not find the viewer", ok, err)
	}
	release()
	if _, _, ok, _ := cache.cached(context.Background(), "rev-2"); ok {
		t.Fatal("another revision found a viewer")
	}
	cache.mu.Lock()
	built := cache.built
	cache.mu.Unlock()
	if built != 1 {
		t.Fatal("builds", built)
	}
}

// A viewer nobody asks for is closed after the idle time, together with the
// revisions naming it.
func TestDocumentCacheDropsIdleViewers(t *testing.T) {
	cache, closed := testCache(t)
	cache.idle = 20 * time.Millisecond
	server, release, err := cache.get(context.Background(), repos.Clone{ID: "one"}, citing(1), "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	// Held, it is never idle.
	time.Sleep(3 * cache.idle)
	if entries, _ := cache.kept(); entries != 1 || closed.has(server) || !answers(t, server) {
		t.Fatal("dropped a viewer in use")
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, revisions := cache.kept()
		if entries == 0 && revisions == 0 && closed.has(server) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle viewer kept or not closed", entries, revisions)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A viewer evicted while a request holds it keeps answering that request and
// is closed when the request releases it.
func TestDocumentCacheClosesAnEvictedViewerAfterItsRequests(t *testing.T) {
	cache, closed := testCache(t)
	cache.maxEntries = 1
	held, release, err := cache.get(context.Background(), repos.Clone{ID: "one"}, citing(1), "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	other, releaseOther, err := cache.get(context.Background(), repos.Clone{ID: "one"}, citing(2), "rev-2")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()
	if entries, revisions := cache.kept(); entries != 1 || revisions != 1 {
		t.Fatal("count bound", entries, revisions)
	}
	if _, _, ok, _ := cache.cached(context.Background(), "rev-1"); ok {
		t.Fatal("evicted viewer still found")
	}
	if closed.has(held) || !answers(t, held) {
		t.Fatal("evicted viewer closed under its request")
	}
	release()
	release()
	if !closed.has(held) || closed.has(other) {
		t.Fatal("evicted viewer not closed once released")
	}
}

// Over the byte bound, the least recently used viewers go, but the last one
// asked for stays.
func TestDocumentCacheBoundsBytes(t *testing.T) {
	cache, closed := testCache(t)
	cache.maxBytes = 1
	first, release, err := cache.get(context.Background(), repos.Clone{ID: "one"}, citing(1), "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if entries, _ := cache.kept(); entries != 1 || closed.has(first) {
		t.Fatal("dropped the only viewer")
	}
	second, release, err := cache.get(context.Background(), repos.Clone{ID: "one"}, citing(2), "")
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := cache.kept(); entries != 1 || !closed.has(first) || closed.has(second) {
		t.Fatal("byte bound", entries)
	}
	before := second.CachedBytes()
	if !answers(t, second) || second.CachedBytes() <= before {
		t.Fatal("patch answers not counted", before, second.CachedBytes())
	}
	release()
}

// Answers a kept viewer adds count once its request ends: when they take the
// cache over the byte bound, the least recently used viewer goes, after the
// request holding it, and the one asked for last stays.
func TestDocumentCacheBoundsBytesAsAnswersGrow(t *testing.T) {
	cache, closed := testCache(t)
	clone := repos.Clone{ID: "one"}
	first, release, err := cache.get(context.Background(), clone, citing(1), "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	release()
	second, release, err := cache.get(context.Background(), clone, citing(2), "rev-2")
	if err != nil {
		t.Fatal(err)
	}
	release()
	cache.mu.Lock()
	cache.maxBytes = first.CachedBytes() + second.CachedBytes() + 10
	cache.mu.Unlock()
	// A request holds the first viewer; then the second is asked for, by its
	// revision as a request after the first one is, and answers a patch.
	_, releaseFirst, ok, err := cache.cached(context.Background(), "rev-1")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	_, releaseSecond, ok, err := cache.cached(context.Background(), "rev-2")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if !answers(t, second) {
		t.Fatal("viewer does not answer")
	}
	releaseSecond()
	if entries, revisions := cache.kept(); entries != 1 || revisions != 1 || closed.has(second) {
		t.Fatal("byte bound after answers grew", entries, revisions)
	}
	if closed.has(first) || !answers(t, first) {
		t.Fatal("evicted viewer closed under its request")
	}
	releaseFirst()
	if !closed.has(first) || closed.has(second) {
		t.Fatal("evicted viewer not closed once released")
	}
}

// A request that gives up waiting for a build lets go of the viewer.
func TestDocumentCacheWaitHonoursContext(t *testing.T) {
	cache, _ := testCache(t)
	entry := &cachedDocument{key: "k", ready: make(chan struct{})}
	cache.mu.Lock()
	cache.entries["k"] = entry
	cache.revisions["rev"] = entry
	entry.revisions = []string{"rev"}
	cache.hold(entry)
	cache.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, ok, err := cache.cached(ctx, "rev"); !ok || err != context.Canceled {
		t.Fatal(ok, err)
	}
	cache.mu.Lock()
	active := entry.active
	cache.mu.Unlock()
	if active != 1 {
		t.Fatal("a cancelled wait kept its hold", active)
	}
}
