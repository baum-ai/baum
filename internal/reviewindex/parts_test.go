package reviewindex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/sqlitepool"
	"github.com/baum-ai/baum/internal/store"
)

// revisedSource is a discovery with revisions that counts its reads.
type revisedSource struct {
	mu        sync.Mutex
	snapshots map[string]discovery.Snapshot
	writes    map[string]uint64
	reads     map[string]int
	failing   map[string]bool
}

func newRevisedSource() *revisedSource {
	return &revisedSource{snapshots: map[string]discovery.Snapshot{}, writes: map[string]uint64{}, reads: map[string]int{}, failing: map[string]bool{}}
}

// set stores a snapshot as a cache write does: the revision moves.
func (s *revisedSource) set(repository string, snapshot discovery.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[repository] = snapshot
	s.writes[repository]++
}

func (s *revisedSource) Cached(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[r.String()]++
	if s.failing[r.String()] {
		return discovery.Snapshot{}, errors.New("cache unreadable")
	}
	return s.snapshots[r.String()], nil
}

func (s *revisedSource) Revision(_ context.Context, r github.Repository) (discovery.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return discovery.Revision{Partition: r.String(), Writes: s.writes[r.String()], Validated: true}, nil
}

func (s *revisedSource) readsOf(repository string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[repository]
}

// fakeStores serves summaries per common directory, stamped by a counter
// that each change moves, and counts the reads.
type fakeStores struct {
	mu        sync.Mutex
	summaries map[string][]store.Summary
	stamps    map[string]int
	reads     map[string]int
	untrusted map[string]bool
}

func newFakeStores() *fakeStores {
	return &fakeStores{summaries: map[string][]store.Summary{}, stamps: map[string]int{}, reads: map[string]int{}, untrusted: map[string]bool{}}
}

func (f *fakeStores) set(common string, summaries ...store.Summary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[common] = summaries
	f.stamps[common]++
}

func (f *fakeStores) open(common string) (Reader, error) {
	return readerFunc(func(context.Context) ([]store.Summary, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads[common]++
		return f.summaries[common], nil
	}), nil
}

func (f *fakeStores) stamp(common string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strconv.Itoa(f.stamps[common]), !f.untrusted[common]
}

func (f *fakeStores) readsOf(common string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[common]
}

func openPRs(numbers ...int) []discovery.PullRequest {
	out := []discovery.PullRequest{}
	for _, n := range numbers {
		out = append(out, discovery.PullRequest{Number: n, HeadSHA: "head" + strconv.Itoa(n), State: "OPEN", Title: "PR " + strconv.Itoa(n)})
	}
	return out
}

func reviewed(repository string, number int, head string) store.Summary {
	return store.Summary{Key: store.Key{Repository: repository, Number: number}, VersionCount: 1, Latest: &store.Metadata{Version: 1, Head: head}}
}

// partsFixture is two picked repositories with a clone each, read through a
// Cache on a test clock.
type partsFixture struct {
	discovery *revisedSource
	stores    *fakeStores
	service   *Service
	cache     *Cache
	clock     *clock
}

func newPartsFixture(t *testing.T) *partsFixture {
	t.Helper()
	f := &partsFixture{discovery: newRevisedSource(), stores: newFakeStores(), clock: &clock{now: time.Now()}}
	far := f.clock.now.Add(time.Hour)
	for _, repository := range []string{"acme/a", "acme/b"} {
		f.discovery.set(repository, discovery.Snapshot{PullRequests: openPRs(1, 2), FetchedAt: f.clock.now, NextPoll: far})
		f.stores.set("/"+repository, reviewed(repository, 1, "head1"))
	}
	f.service = &Service{
		Registry: registry{
			{ID: "a", Repository: "acme/a", CommonDir: "/acme/a", Available: true},
			{ID: "b", Repository: "acme/b", CommonDir: "/acme/b", Available: true},
		},
		Discovery:  f.discovery,
		OpenStore:  f.stores.open,
		StoreStamp: f.stores.stamp,
	}
	f.cache = NewCache(context.Background(), f.service)
	f.cache.now = f.clock.Now
	return f
}

// rebuild invalidates the cache, reads it again and checks the snapshot is
// the one an uncached build gives.
func (f *partsFixture) rebuild(t *testing.T) Snapshot {
	t.Helper()
	f.cache.Invalidate()
	got, err := f.cache.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An uncached build reads everything: count it apart.
	plain := *f.service
	plain.Discovery = cachedOnly{f.discovery.clone()}
	plain.OpenStore = f.stores.copy().open
	plain.StoreStamp = nil
	want, err := plain.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
		t.Fatalf("cached build differs from a full one:\n%s\n%s", a, b)
	}
	return got
}

// clone is a copy of s with no reads counted.
func (s *revisedSource) clone() *revisedSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := newRevisedSource()
	for k, v := range s.snapshots {
		out.snapshots[k] = v
		out.writes[k] = s.writes[k]
	}
	for k, v := range s.failing {
		out.failing[k] = v
	}
	return out
}

func (s *revisedSource) copySnapshot(repository string) discovery.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshots[repository]
}

// cachedOnly hides a discovery's revisions.
type cachedOnly struct{ d Discovery }

func (c cachedOnly) Cached(ctx context.Context, r github.Repository) (discovery.Snapshot, error) {
	return c.d.Cached(ctx, r)
}

func (f *fakeStores) copy() *fakeStores {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := newFakeStores()
	for k, v := range f.summaries {
		out.summaries[k] = v
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f *partsFixture) reads() [4]int {
	return [4]int{f.discovery.readsOf("acme/a"), f.stores.readsOf("/acme/a"), f.discovery.readsOf("acme/b"), f.stores.readsOf("/acme/b")}
}

func entry(t *testing.T, s Snapshot, repository string, number int) Entry {
	t.Helper()
	for _, e := range s.Entries {
		if e.PullRequest.Repository == repository && e.PullRequest.Number == number {
			return e
		}
	}
	t.Fatalf("no row for %s#%d in %+v", repository, number, s.Entries)
	return Entry{}
}

// A change to one repository's discovery cache or store reads that
// repository again, and only it; the change shows in the same build.
func TestCacheRebuildReadsOnlyTheRepositoryThatChanged(t *testing.T) {
	f := newPartsFixture(t)
	f.rebuild(t)
	if got := f.reads(); got != [4]int{1, 1, 1, 1} {
		t.Fatalf("first build reads %v", got)
	}

	// Nothing changed: an invalidation reads nothing again.
	f.rebuild(t)
	if got := f.reads(); got != [4]int{1, 1, 1, 1} {
		t.Fatalf("unchanged rebuild reads %v", got)
	}

	// A discovery write for acme/a: a new pull request, and a push to #1.
	f.discovery.set("acme/a", discovery.Snapshot{PullRequests: append(openPRs(2, 3), discovery.PullRequest{Number: 1, HeadSHA: "pushed", State: "OPEN"}), FetchedAt: f.clock.now, NextPoll: f.clock.now.Add(time.Hour)})
	snapshot := f.rebuild(t)
	if got := f.reads(); got != [4]int{2, 2, 1, 1} {
		t.Fatalf("after a discovery write for acme/a, reads %v", got)
	}
	if e := entry(t, snapshot, "acme/a", 3); e.Status != "no_review" {
		t.Fatalf("new pull request %+v", e)
	}
	if e := entry(t, snapshot, "acme/a", 1); e.Status != "outdated" {
		t.Fatalf("pushed pull request %+v", e)
	}
	if e := entry(t, snapshot, "acme/b", 1); e.Status != "current" {
		t.Fatalf("other repository %+v", e)
	}

	// A new version stored in acme/b's store.
	f.stores.set("/acme/b", reviewed("acme/b", 1, "head1"), reviewed("acme/b", 2, "head2"))
	snapshot = f.rebuild(t)
	if got := f.reads(); got != [4]int{2, 2, 2, 2} {
		t.Fatalf("after a store write for acme/b, reads %v", got)
	}
	if e := entry(t, snapshot, "acme/b", 2); e.Status != "current" || e.VersionCount != 1 {
		t.Fatalf("new review %+v", e)
	}

	// A clone becoming unavailable is in the registry, read on every build.
	f.service.Registry = registry{
		{ID: "a", Repository: "acme/a", CommonDir: "/acme/a", Available: true},
		{ID: "b", Repository: "acme/b", CommonDir: "/acme/b", Available: false},
	}
	snapshot = f.rebuild(t)
	if got := f.reads(); got != [4]int{2, 2, 3, 3} {
		t.Fatalf("after acme/b's clone went away, reads %v", got)
	}
	if e := entry(t, snapshot, "acme/b", 1); !e.Stale || !e.NeedsClone {
		t.Fatalf("unavailable clone %+v", e)
	}
}

// Every repository is read again MaxAge after the build that last read them
// all, and each one when its own next change comes.
func TestCacheReadsRepositoriesAgainWhenTheyMayHaveChanged(t *testing.T) {
	f := newPartsFixture(t)
	poll := f.clock.now.Add(time.Minute)
	f.discovery.set("acme/a", discovery.Snapshot{PullRequests: openPRs(1, 2), FetchedAt: f.clock.now, NextPoll: poll})
	f.rebuild(t)
	f.clock.Advance(30 * time.Second)
	f.rebuild(t)
	if got := f.reads(); got != [4]int{1, 1, 1, 1} {
		t.Fatalf("reads %v", got)
	}
	// Poll deadlines alone do not invalidate the review index.
	f.clock.Advance(31 * time.Second)
	f.rebuild(t)
	if got := f.reads(); got != [4]int{1, 1, 1, 1} {
		t.Fatalf("after acme/a's next poll, reads %v", got)
	}
	// The kept snapshot expires when every repository is due again.
	f.cache.mu.Lock()
	expires := f.cache.current.expires
	f.cache.mu.Unlock()
	f.clock.Advance(MaxAge - 61*time.Second)
	if !expires.Equal(f.clock.Now()) {
		t.Fatalf("expires %v, want %v", expires, f.clock.Now())
	}
	f.rebuild(t)
	if got := f.reads(); got != [4]int{2, 2, 2, 2} {
		t.Fatalf("after MaxAge, reads %v", got)
	}
}

// What cannot be vouched for is read on every build: a store whose stamp
// does not hold, a discovery without revisions, a repository whose read
// failed.
func TestCacheReadsWhatItCannotVouchForEveryTime(t *testing.T) {
	f := newPartsFixture(t)
	f.stores.untrusted["/acme/a"] = true
	f.rebuild(t)
	f.rebuild(t)
	if got := f.reads(); got != [4]int{2, 2, 1, 1} {
		t.Fatalf("untrusted store: reads %v", got)
	}

	f = newPartsFixture(t)
	f.discovery.failing["acme/b"] = true
	f.rebuild(t)
	f.rebuild(t)
	if got := f.reads(); got != [4]int{1, 1, 2, 2} {
		t.Fatalf("failed discovery read: reads %v", got)
	}

	f = newPartsFixture(t)
	f.service.Discovery = cachedOnly{f.discovery}
	f.rebuild(t)
	f.rebuild(t)
	if got := [2]int{f.stores.readsOf("/acme/a"), f.stores.readsOf("/acme/b")}; got != [2]int{2, 2} {
		t.Fatalf("no revisions: store reads %v", got)
	}
}

// With real stores, a version written to one repository's store shows in the
// next build, which reads no other store.
func TestCacheSeesStoreWritesThroughFileStamps(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// Connections stay open, as the daemon's do, so a read leaves the files
	// as they were. Closed before the directory is removed.
	sqlitepool.Enable()
	t.Cleanup(sqlitepool.Disable)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	discovered := newRevisedSource()
	clones := registry{}
	stores := map[string]*store.Store{}
	var mu sync.Mutex
	opened := map[string]int{}
	for _, repository := range []string{"acme/a", "acme/b"} {
		common := filepath.Join(root, repository)
		s, err := store.Open(common, "", true)
		if err != nil {
			t.Fatal(err)
		}
		stores[repository] = s
		clones = append(clones, repos.Clone{ID: repository, Repository: repository, CommonDir: common, Available: true})
		discovered.set(repository, discovery.Snapshot{PullRequests: openPRs(1, 2), FetchedAt: time.Now(), NextPoll: time.Now().Add(time.Hour)})
	}
	put := func(repository string, n int) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"review": map[string]any{"repository": repository, "pull_request": n, "version": 1}})
		if err := stores[repository].Put(ctx, store.Key{Repository: repository, Number: n}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: "head" + strconv.Itoa(n), Backend: "claude", Model: "model", GeneratedAt: "2026-11-12T12:00:00Z"}, Document: raw}); err != nil {
			t.Fatal(err)
		}
	}
	// Files last written more than racyWindow ago, so their stamps vouch
	// for them.
	old := time.Now().Add(-time.Minute)
	age := func(repository string) {
		t.Helper()
		roots, err := store.Roots(filepath.Join(root, repository))
		if err != nil {
			t.Fatal(err)
		}
		old = old.Add(time.Second)
		for _, dir := range roots {
			for _, name := range []string{"versions.sqlite", "versions.sqlite-wal"} {
				if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
		}
	}
	put("acme/a", 1)
	put("acme/b", 1)
	age("acme/a")
	age("acme/b")
	service := &Service{
		Registry:  clones,
		Discovery: discovered,
		OpenStore: func(common string) (Reader, error) {
			mu.Lock()
			opened[common]++
			mu.Unlock()
			return store.Open(common, "", false)
		},
		StoreStamp: func(common string) (string, bool) {
			stamp, _, ok := fileStoreStamp(common, time.Now())
			return stamp, ok
		},
	}
	c := NewCache(ctx, service)
	read := func() Snapshot {
		t.Helper()
		c.Invalidate()
		s, err := c.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	counts := func() [2]int {
		mu.Lock()
		defer mu.Unlock()
		return [2]int{opened[filepath.Join(root, "acme/a")], opened[filepath.Join(root, "acme/b")]}
	}
	read()
	read()
	if got := counts(); got != [2]int{1, 1} {
		t.Fatalf("unchanged stores opened %v", got)
	}
	// Just written: the stamp is too recent to vouch, so it is read.
	put("acme/a", 2)
	if e := entry(t, read(), "acme/a", 2); e.Status != "current" {
		t.Fatalf("new review %+v", e)
	}
	if got := counts(); got != [2]int{2, 1} {
		t.Fatalf("after a write to acme/a, opened %v", got)
	}
	// Once old enough, the moved stamp is kept and vouches again.
	age("acme/a")
	read()
	read()
	if got := counts(); got != [2]int{3, 1} {
		t.Fatalf("after acme/a settled, opened %v", got)
	}
	// A write whose files settled before the next build still moves the
	// stamp.
	put("acme/b", 2)
	age("acme/b")
	if e := entry(t, read(), "acme/b", 2); e.Status != "current" {
		t.Fatalf("new review %+v", e)
	}
	if got := counts(); got != [2]int{3, 2} {
		t.Fatalf("after a settled write to acme/b, opened %v", got)
	}
}

// A store's stamp moves with a lease renewal, which can bring a lapsed run
// back to generating, and with a file replaced by another of the same size,
// times and mode.
func TestStoreStampFollowsRenewalsAndReplacedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	sqlitepool.Enable()
	t.Cleanup(sqlitepool.Disable)
	common := filepath.Join(root, "clone")
	s, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginRun(ctx, store.Key{Repository: "acme/a", Number: 1}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// Seen from later on, so that no file is too recent to vouch.
	later := func() partKey {
		t.Helper()
		stamp, files, ok := fileStoreStamp(common, time.Now().Add(time.Hour))
		if !ok {
			t.Fatal("stamp does not vouch")
		}
		return partKey{inputs: stamp, files: files}
	}
	before := later()
	if !before.same(later()) {
		t.Fatal("an unchanged store's stamp moved")
	}
	if err := s.HeartbeatRun(ctx, id, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if later().same(before) {
		t.Fatal("a lease renewal did not move the stamp")
	}

	// Replaced by a copy with the same bytes, size, times and mode. Handles
	// are closed first, as a file open elsewhere cannot be replaced on
	// Windows; closing the last one checkpoints, so the stamp is taken after.
	sqlitepool.Disable()
	path := filepath.Join(common, "baum", "reviews", "versions.sqlite")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stamped := later()
	copied := path + ".copy"
	if err := os.WriteFile(copied, raw, info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(copied, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(copied, path); err != nil {
		t.Fatal(err)
	}
	replaced := later()
	if replaced.inputs != stamped.inputs {
		t.Fatalf("the copy differs in size, time or mode:\n%s\n%s", replaced.inputs, stamped.inputs)
	}
	if replaced.same(stamped) {
		t.Fatal("a replaced file kept the stamp")
	}
}
