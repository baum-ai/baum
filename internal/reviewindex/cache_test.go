package reviewindex

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// gatedBuilds hands out one build per call, each waiting for its release.
type gatedBuilds struct {
	mu       sync.Mutex
	started  chan int
	releases []chan buildResult
	contexts []context.Context
}

type buildResult struct {
	snapshot Snapshot
	next     time.Time
	err      error
}

func newGated() *gatedBuilds { return &gatedBuilds{started: make(chan int, 16)} }

func (g *gatedBuilds) build(ctx context.Context, _ time.Time) (Snapshot, time.Time, error) {
	release := make(chan buildResult)
	g.mu.Lock()
	n := len(g.releases)
	g.releases = append(g.releases, release)
	g.contexts = append(g.contexts, ctx)
	g.mu.Unlock()
	g.started <- n
	r := <-release
	return r.snapshot, r.next, r.err
}

func (g *gatedBuilds) release(t *testing.T, n int, r buildResult) {
	t.Helper()
	g.mu.Lock()
	ch := g.releases[n]
	g.mu.Unlock()
	ch <- r
}

func (g *gatedBuilds) await(t *testing.T) int {
	t.Helper()
	select {
	case n := <-g.started:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no build started")
		return -1
	}
}

// snap is a snapshot marked n, so a test can tell which build it came from.
func snap(n int) Snapshot {
	return Snapshot{Entries: []Entry{}, Problems: []Problem{{CloneID: strconv.Itoa(n)}}}
}

func mark(s Snapshot) int {
	if len(s.Problems) != 1 {
		return -1
	}
	n, err := strconv.Atoi(s.Problems[0].CloneID)
	if err != nil {
		return -1
	}
	return n
}

func testCache(build func(context.Context, time.Time) (Snapshot, time.Time, error), now func() time.Time) *Cache {
	return &Cache{life: context.Background(), build: build, now: now}
}

type read struct {
	snapshot Snapshot
	err      error
}

func readAsync(ctx context.Context, c *Cache) <-chan read {
	out := make(chan read, 1)
	go func() {
		s, err := c.Snapshot(ctx)
		out <- read{s, err}
	}()
	return out
}

func (c *Cache) joined(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		got := 0
		if c.flight != nil {
			got = c.flight.waiting
		}
		c.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d readers joined, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *Cache) buildCount() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.builds
}

func TestCacheConcurrentReadersShareOneBuild(t *testing.T) {
	g := newGated()
	c := testCache(g.build, (&clock{now: time.Unix(1000, 0)}).Now)
	const n = 16
	results := make([]<-chan read, n)
	for i := range results {
		results[i] = readAsync(context.Background(), c)
	}
	first := g.await(t)
	c.joined(t, n)
	g.release(t, first, buildResult{snapshot: snap(3)})
	for _, r := range results {
		got := <-r
		if got.err != nil || mark(got.snapshot) != 3 {
			t.Fatalf("got %+v", got)
		}
	}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.buildCount(); got != 1 {
		t.Fatal("builds", got)
	}
}

func TestCacheInvalidationDuringBuildStartsAnother(t *testing.T) {
	g := newGated()
	c := testCache(g.build, (&clock{now: time.Unix(1000, 0)}).Now)
	before := readAsync(context.Background(), c)
	old := g.await(t)
	c.joined(t, 1)
	c.Invalidate()
	after := readAsync(context.Background(), c)
	fresh := g.await(t)
	// The newer build finishes first; the older one must not replace it.
	g.release(t, fresh, buildResult{snapshot: snap(2)})
	if got := <-after; got.err != nil || mark(got.snapshot) != 2 {
		t.Fatalf("reader after the invalidation got %+v", got)
	}
	g.release(t, old, buildResult{snapshot: snap(1)})
	if got := <-before; got.err != nil || mark(got.snapshot) != 1 {
		t.Fatalf("reader before the invalidation got %+v", got)
	}
	got, err := c.Snapshot(context.Background())
	if err != nil || mark(got) != 2 {
		t.Fatalf("cached %+v %v", got, err)
	}
	if n := c.buildCount(); n != 2 {
		t.Fatal("builds", n)
	}
}

func TestCacheStaleBuildIsNotKept(t *testing.T) {
	g := newGated()
	c := testCache(g.build, (&clock{now: time.Unix(1000, 0)}).Now)
	before := readAsync(context.Background(), c)
	old := g.await(t)
	c.joined(t, 1)
	c.Invalidate()
	g.release(t, old, buildResult{snapshot: snap(1)})
	<-before
	if _, ok := c.Fresh(); ok {
		t.Fatal("a build started before the invalidation was kept")
	}
	after := readAsync(context.Background(), c)
	g.release(t, g.await(t), buildResult{snapshot: snap(2)})
	if got := <-after; mark(got.snapshot) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestCacheReaderCancellationLeavesOthers(t *testing.T) {
	g := newGated()
	c := testCache(g.build, (&clock{now: time.Unix(1000, 0)}).Now)
	ctx, cancel := context.WithCancel(context.Background())
	quitter := readAsync(ctx, c)
	n := g.await(t)
	stayer := readAsync(context.Background(), c)
	c.joined(t, 2)
	cancel()
	if got := <-quitter; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled reader got %+v", got)
	}
	g.mu.Lock()
	buildCtx := g.contexts[n]
	g.mu.Unlock()
	if buildCtx.Err() != nil {
		t.Fatal("the shared build was cancelled")
	}
	g.release(t, n, buildResult{snapshot: snap(5)})
	if got := <-stayer; got.err != nil || mark(got.snapshot) != 5 {
		t.Fatalf("other reader got %+v", got)
	}
}

func TestCacheDoesNotKeepErrors(t *testing.T) {
	g := newGated()
	c := testCache(g.build, (&clock{now: time.Unix(1000, 0)}).Now)
	r := readAsync(context.Background(), c)
	g.release(t, g.await(t), buildResult{err: errors.New("boom")})
	if got := <-r; got.err == nil {
		t.Fatal("error not returned")
	}
	r = readAsync(context.Background(), c)
	g.release(t, g.await(t), buildResult{snapshot: snap(1)})
	if got := <-r; got.err != nil || mark(got.snapshot) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestCacheExpiresAtComputedTimeAndMaxAge(t *testing.T) {
	clk := &clock{now: time.Unix(1000, 0)}
	next := clk.Now().Add(10 * time.Second)
	builds := 0
	c := testCache(func(context.Context, time.Time) (Snapshot, time.Time, error) {
		builds++
		n := next
		next = time.Time{}
		return snap(builds), n, nil
	}, clk.Now)
	ctx := context.Background()
	expect := func(want int) {
		t.Helper()
		got, err := c.Snapshot(ctx)
		if err != nil || mark(got) != want || builds != want {
			t.Fatalf("snapshot %d builds %d err %v, want %d", mark(got), builds, err, want)
		}
	}
	expect(1)
	clk.Advance(10*time.Second - time.Nanosecond)
	expect(1)
	clk.Advance(time.Nanosecond)
	expect(2)
	// Without a computed instant, MaxAge bounds the snapshot's life.
	clk.Advance(MaxAge - time.Nanosecond)
	expect(2)
	clk.Advance(time.Nanosecond)
	expect(3)
}

func TestBuildReportsNextChange(t *testing.T) {
	now := time.Now()
	lease := now.Add(90 * time.Second)
	poll := now.Add(3 * time.Minute)
	fetched := now.Add(-time.Minute)
	s := &Service{
		Registry: registry{{ID: "c", Repository: "acme/a", CommonDir: "/c", Available: true}},
		Discovery: source{"acme/a": {
			PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "h", State: "OPEN"}, {Number: 2, HeadSHA: "h", State: "OPEN"}},
			Observations: []discovery.Observation{{Number: 2, HeadSHA: "h", State: "OPEN", FetchedAt: fetched}},
			FetchedAt:    fetched.Add(-time.Second),
			NextPoll:     poll,
		}},
		OpenStore: func(string) (Reader, error) {
			return readerFunc(func(context.Context) ([]store.Summary, error) {
				return []store.Summary{{Key: store.Key{Repository: "acme/a", Number: 1}, Run: &store.Run{State: "generating", Expires: lease}}}, nil
			}), nil
		},
	}
	_, next, err := s.build(context.Background(), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := fetched.Add(2*time.Minute + time.Nanosecond); !next.Equal(want) {
		t.Fatalf("observation: next %v want %v", next, want)
	}
	fetched = now.Add(-5 * time.Minute)
	s.Discovery.(source)["acme/a"].Observations[0].FetchedAt = fetched
	if _, next, _ = s.build(context.Background(), now, nil); !next.Equal(lease) {
		t.Fatalf("lease: next %v want %v", next, lease)
	}
	lease = now.Add(-time.Second)
	if _, next, _ = s.build(context.Background(), now, nil); !next.Equal(poll) {
		t.Fatalf("poll: next %v want %v", next, poll)
	}
}

type readerFunc func(context.Context) ([]store.Summary, error)

func (f readerFunc) Snapshot(ctx context.Context) ([]store.Summary, error) { return f(ctx) }

// A run whose lease ends with no write reads as interrupted once the cached
// snapshot reaches the lease's end.
func TestCacheExpiresWithGenerationLease(t *testing.T) {
	ctx := context.Background()
	common := t.TempDir()
	s, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/a", Number: 1}
	lease := time.Now().Add(time.Second)
	if _, err := s.BeginRun(ctx, key, lease); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		Registry:  registry{{ID: "c", Repository: "acme/a", CommonDir: common, Available: true}},
		Discovery: source{"acme/a": {PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "h", State: "OPEN"}}, NextPoll: time.Now().Add(time.Hour)}},
	}
	c := NewCache(ctx, service)
	status := func() (string, string) {
		t.Helper()
		got, err := c.Snapshot(ctx)
		if err != nil || len(got.Entries) != 1 {
			t.Fatalf("%+v %v", got, err)
		}
		return got.Entries[0].Status, got.Entries[0].FailureReason
	}
	if state, _ := status(); state != "generating" {
		t.Fatal(state)
	}
	c.mu.Lock()
	expires := c.current.expires
	c.mu.Unlock()
	if !expires.Equal(lease.UTC()) {
		t.Fatalf("expires %v want lease end %v", expires, lease)
	}
	time.Sleep(time.Until(lease) + 10*time.Millisecond)
	if state, reason := status(); state != "failed" || reason != "interrupted" {
		t.Fatal(state, reason)
	}
	if n := c.buildCount(); n != 2 {
		t.Fatal("builds", n)
	}
}

func TestFingerprintFollowsStoreCommitsAndClones(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	worktree := filepath.Join(dir, "clone")
	common := filepath.Join(worktree, ".git")
	if err := os.MkdirAll(common, 0o700); err != nil {
		t.Fatal(err)
	}
	picked := []repos.Repository{{Repository: "acme/a", Clones: []repos.Clone{{ID: "c", Path: worktree, CommonDir: common, Repository: "acme/a"}}}}
	revision := "1"
	fingerprint := Fingerprint(func(context.Context) ([]repos.Repository, error) { return picked, nil }, func() string { return revision })
	check := fingerprints(t, fingerprint)
	key := store.Key{Repository: "acme/a", Number: 1}
	s, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginRun(ctx, key, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	check.moved("store created by a commit")
	info, err := os.Stat(filepath.Join(common, "baum", "reviews", "versions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	// An in-place commit is seen by its modification time, which moves in
	// coarse steps: commits within one step of the last check look alike
	// until MaxAge. Backdating the file first keeps the test independent of
	// the file system's timestamp granularity.
	backdate := func() {
		t.Helper()
		past := time.Now().Add(-time.Hour)
		if err := os.Chtimes(filepath.Join(common, "baum", "reviews", "versions.sqlite"), past, past); err != nil {
			t.Fatal(err)
		}
		check.rebase()
	}
	backdate()
	if err := s.HeartbeatRun(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, err := os.Stat(filepath.Join(common, "baum", "reviews", "versions.sqlite")); err != nil || again.Size() != info.Size() {
		t.Fatal("the lease renewal was expected to keep the store's size", err)
	}
	check.still("lease renewal")
	// A real write before or after a renewal is not hidden by it.
	other := store.Key{Repository: "acme/a", Number: 2}
	if _, err := s.BeginRun(ctx, other, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatRun(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check.moved("write followed by a lease renewal")
	if err := s.HeartbeatRun(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check.still("second lease renewal")
	if _, err := s.BeginRun(ctx, store.Key{Repository: "acme/a", Number: 3}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check.moved("write after a lease renewal")
	backdate()
	if err := s.FinishRun(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	check.moved("run finished")
	roots, err := store.Roots(common)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.Open("", roots[1], true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fallback.BeginRun(ctx, key, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check.moved("fallback store written")
	if err := os.WriteFile(filepath.Join(common, "config"), []byte("[remote]"), 0o600); err != nil {
		t.Fatal(err)
	}
	check.moved("git config written")
	revision = "2"
	check.moved("GitHub account changed")
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	check.moved("clone removed")
	picked = nil
	check.moved("clone unregistered")
}

type fingerprintCheck struct {
	t           *testing.T
	fingerprint func(context.Context) string
	last        string
}

func fingerprints(t *testing.T, fingerprint func(context.Context) string) *fingerprintCheck {
	c := &fingerprintCheck{t: t, fingerprint: fingerprint, last: fingerprint(context.Background())}
	c.still("nothing changed")
	return c
}

func (c *fingerprintCheck) moved(what string) {
	c.t.Helper()
	got := c.fingerprint(context.Background())
	if got == c.last {
		c.t.Fatal("fingerprint did not move:", what)
	}
	c.last = got
}

// rebase takes the current fingerprint as the baseline without asserting.
func (c *fingerprintCheck) rebase() { c.last = c.fingerprint(context.Background()) }

func (c *fingerprintCheck) still(what string) {
	c.t.Helper()
	if got := c.fingerprint(context.Background()); got != c.last {
		c.t.Fatal("fingerprint moved:", what)
	}
}

// The registry is read as rows: another process's registry write moves the
// fingerprint, and other application database writes, such as job progress,
// do not.
func TestFingerprintFollowsRegistryRowsOnly(t *testing.T) {
	ctx := context.Background()
	private := filepath.Join(t.TempDir(), "private")
	if err := privatefs.MkdirAll(private); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "app.sqlite")
	daemon, err := repos.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = daemon.Close() }()
	check := fingerprints(t, Fingerprint(daemon.RepositoryRows, nil))
	other, err := repos.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.Pick(ctx, "acme/a", nil); err != nil {
		t.Fatal(err)
	}
	check.moved("repository picked elsewhere")
	if err := appdb.Write(path, "state", "jobs", []byte(`{"jobs":[]}`)); err != nil {
		t.Fatal(err)
	}
	check.still("job state written")
	off := false
	if err := other.UpdateRepository(ctx, "acme/a", repos.Settings{Automatic: &off}); err != nil {
		t.Fatal(err)
	}
	check.moved("settings changed elsewhere")
}

func TestCacheRecoversBuildPanic(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := testCache(func(context.Context, time.Time) (Snapshot, time.Time, error) {
		if calls.Add(1) == 1 {
			<-release
			panic("boom")
		}
		return snap(1), time.Time{}, nil
	}, (&clock{now: time.Unix(1000, 0)}).Now)
	// Every reader waiting on the panicking build gets the error.
	first, second := readAsync(context.Background(), c), readAsync(context.Background(), c)
	c.joined(t, 2)
	close(release)
	for _, r := range []<-chan read{first, second} {
		if got := <-r; got.err == nil || !strings.Contains(got.err.Error(), "boom") {
			t.Fatal(got.err)
		}
	}
	if got, err := c.Snapshot(context.Background()); err != nil || mark(got) != 1 {
		t.Fatal(got, err)
	}
}

// A new cache, as after a daemon restart, shows a review written while no
// cache was running on its first read.
func TestCacheReopenedSeesReviewWrittenMeanwhile(t *testing.T) {
	ctx := context.Background()
	common := t.TempDir()
	s, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		Registry:  registry{{ID: "c", Repository: "acme/a", CommonDir: common, Available: true}},
		Discovery: source{"acme/a": {PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "head", State: "OPEN"}}, NextPoll: time.Now().Add(time.Hour)}},
	}
	before, err := NewCache(ctx, service).Snapshot(ctx)
	if err != nil || len(before.Entries) != 1 || before.Entries[0].Status != "no_review" {
		t.Fatalf("%+v %v", before, err)
	}
	doc := []byte(`{"review":{"repository":"acme/a","pull_request":1,"version":1}}`)
	if err := s.Put(ctx, store.Key{Repository: "acme/a", Number: 1}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: "head", Backend: "claude", Model: "model", GeneratedAt: "2026-10-15T12:00:00Z"}, Document: doc}); err != nil {
		t.Fatal(err)
	}
	after, err := NewCache(ctx, service).Snapshot(ctx)
	if err != nil || len(after.Entries) != 1 || after.Entries[0].Status != "current" {
		t.Fatalf("%+v %v", after, err)
	}
}

// Requests filter their own copy: the shared snapshot keeps historical rows.
func TestCacheHandlerLeavesSharedSnapshot(t *testing.T) {
	entries := []Entry{{PullRequest: discovery.PullRequest{Number: 1}, Historical: true}, {PullRequest: discovery.PullRequest{Number: 2}}}
	builds := 0
	c := testCache(func(context.Context, time.Time) (Snapshot, time.Time, error) {
		builds++
		return Snapshot{Entries: entries, Problems: []Problem{}}, time.Time{}, nil
	}, (&clock{now: time.Unix(1000, 0)}).Now)
	get := func(query string) string {
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/index"+query, nil))
		return w.Body.String()
	}
	if got := get(""); strings.Count(got, `"number"`) != 1 {
		t.Fatal(got)
	}
	if got := get("?history=1"); strings.Count(got, `"number"`) != 2 {
		t.Fatal(got)
	}
	if builds != 1 || !entries[0].Historical || entries[1].PullRequest.Number != 2 {
		t.Fatal("builds", builds, entries)
	}
}
