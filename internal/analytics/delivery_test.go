package analytics

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// scale is how much faster the fake pacing clock runs than real time, so the
// tests cover minutes of pacing in milliseconds with real goroutines.
const scale = 200

// pacedSearch is a fake GitHub search. Each search makes `pages` requests,
// each of which first goes through the pacer the Delivery put on the
// context, as the real client does; the fake records when requests were sent
// on the sped-up clock, and how many searches ran at once.
type pacedSearch struct {
	mu       sync.Mutex
	clock    time.Time
	began    time.Time
	inFlight int
	maxSeen  int
	requests []time.Time
	searches int
	pages    int
	hold     time.Duration
	answer   func(name string) ([]github.AuthoredPullRequest, error)
}

func (p *pacedSearch) tick() time.Time {
	return p.clock.Add(scale * time.Since(p.began))
}

func (p *pacedSearch) sleep(ctx context.Context, until time.Time) error {
	timer := time.NewTimer(until.Sub(p.tick()) / scale)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *pacedSearch) AuthoredPullRequests(ctx context.Context, repo github.Repository, _ time.Time) ([]github.AuthoredPullRequest, error) {
	p.mu.Lock()
	p.inFlight++
	p.maxSeen = max(p.maxSeen, p.inFlight)
	p.searches++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	for range max(p.pages, 1) {
		if err := github.PaceRequest(ctx); err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.requests = append(p.requests, p.tick())
		p.mu.Unlock()
		time.Sleep(p.hold)
	}
	return p.answer(repo.Owner + "/" + repo.Name)
}

func (p *pacedSearch) count() (searches, requests int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.searches, len(p.requests)
}

func newPaced(answer func(string) ([]github.AuthoredPullRequest, error)) (*Delivery, *pacedSearch) {
	search := &pacedSearch{clock: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), began: time.Now(), hold: 2 * time.Millisecond, answer: answer}
	return &Delivery{Search: search, tick: search.tick, sleep: search.sleep}, search
}

// testClock is a settable Now, read by worker goroutines.
type testClock struct{ ns atomic.Int64 }

func newTestClock(t time.Time) *testClock {
	c := &testClock{}
	c.ns.Store(t.UnixNano())
	return c
}
func (c *testClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func waitFor(t *testing.T, d *Delivery, names []string, done func([]repositoryPulls) bool) []repositoryPulls {
	t.Helper()
	var got []repositoryPulls
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if got = d.Pulls(names); done(got) {
			return got
		}
	}
	t.Fatalf("not settled: %+v", got)
	return nil
}

func repoNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("acme/repo%03d", i)
	}
	return names
}

func settled(rs []repositoryPulls) bool {
	for _, r := range rs {
		if !r.Loaded && r.Reason == "" {
			return false
		}
	}
	return true
}

func allLoaded(rs []repositoryPulls) bool {
	for _, r := range rs {
		if !r.Loaded || r.Reason != "" {
			return false
		}
	}
	return true
}

func TestDeliveryBoundsConcurrencyAndRequestsPerMinute(t *testing.T) {
	d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) {
		return []github.AuthoredPullRequest{{Number: 1}}, nil
	})
	// Every search is five pages, from a cold start.
	search.pages = 5
	got := waitFor(t, d, repoNames(20), allLoaded)
	if len(got) != 20 {
		t.Fatalf("got %d repositories", len(got))
	}
	search.mu.Lock()
	defer search.mu.Unlock()
	if search.searches != 20 || len(search.requests) != 100 {
		t.Fatalf("searches = %d, requests = %d", search.searches, len(search.requests))
	}
	if search.maxSeen > deliveryWorkers {
		t.Errorf("in flight = %d, want at most %d", search.maxSeen, deliveryWorkers)
	}
	// Never more than 30 requests in any minute of the fake clock.
	for i := range search.requests {
		n := 0
		for _, at := range search.requests[i:] {
			if at.Sub(search.requests[i]) < time.Minute {
				n++
			}
		}
		if n > 30 {
			t.Fatalf("%d requests within a minute of %v", n, search.requests[i])
		}
	}
}

func TestDeliveryShowsRepositoriesAsTheyArrive(t *testing.T) {
	d, _ := newPaced(func(string) ([]github.AuthoredPullRequest, error) {
		return []github.AuthoredPullRequest{{Number: 1}}, nil
	})
	names := repoNames(40)
	partial := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		got := d.Pulls(names)
		loaded := 0
		for _, r := range got {
			if r.Loaded {
				loaded++
			}
		}
		partial = partial || (loaded > 0 && loaded < len(names))
		if loaded == len(names) {
			break
		}
	}
	if !partial {
		t.Error("never saw some repositories loaded and others still waiting")
	}
}

func TestDeliveryRateLimitBacksOffAndKeepsTheOthers(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
	resume := clock.now().Add(20 * time.Minute)
	var limit atomic.Bool
	d, search := newPaced(func(name string) ([]github.AuthoredPullRequest, error) {
		if name == "acme/repo003" && limit.Load() {
			return nil, &github.RateLimitedError{ResumeAt: resume}
		}
		return []github.AuthoredPullRequest{{Number: 7}}, nil
	})
	d.Now = clock.now
	var mu sync.Mutex
	var paused []time.Time
	d.Limited = func(_ string, until time.Time) {
		mu.Lock()
		paused = append(paused, until)
		mu.Unlock()
	}
	names := repoNames(20)
	// First everything loads once, then the TTL passes and a limit hits.
	waitFor(t, d, names, allLoaded)
	limit.Store(true)
	clock.advance(2 * DeliveryTTL)
	resume = clock.now().Add(20 * time.Minute)
	waitFor(t, d, names, func(rs []repositoryPulls) bool {
		for _, r := range rs {
			if r.Reason != "" {
				return true
			}
		}
		return false
	})
	// Let the queue drain: each repository is loaded or limited.
	time.Sleep(100 * time.Millisecond)
	got := d.Pulls(names)
	searches, _ := search.count()
	if searches >= 40 {
		t.Errorf("%d searches ran in all; the limit should hold the queue back", searches)
	}
	for _, r := range got {
		if !r.Loaded || len(r.Pulls) != 1 {
			t.Errorf("%s lost its earlier answer: %+v", r.Repository, r)
		}
	}
	limitedCount := 0
	for _, r := range got {
		if r.Reason != "" {
			limitedCount++
		}
	}
	if limitedCount == 0 {
		t.Fatal("no repository carries the limit reason")
	}
	mu.Lock()
	// Searches already in flight when the limit hit may each report it.
	if len(paused) == 0 {
		t.Error("the refresh was not told of the limit")
	}
	for _, until := range paused {
		if !until.Equal(resume) {
			t.Errorf("Limited until %v, want %v", until, resume)
		}
	}
	mu.Unlock()
	// Nothing is searched before the reset...
	clock.advance(10 * time.Minute)
	before, _ := search.count()
	d.Pulls(names)
	time.Sleep(50 * time.Millisecond)
	if after, _ := search.count(); after != before {
		t.Errorf("searched during the pause: %d -> %d", before, after)
	}
	// ...and afterwards the held-back repositories are searched and clear.
	limit.Store(false)
	clock.advance(11 * time.Minute)
	waitFor(t, d, names, allLoaded)
}

func TestDeliveryHonoursThePausedHost(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
	d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) { return nil, nil })
	d.Now = clock.now
	pausedUntil := clock.now().Add(time.Hour)
	d.Paused = func(string) time.Time { return pausedUntil }
	var limitedCalls atomic.Int32
	d.Limited = func(_ string, until time.Time) {
		if !until.Equal(pausedUntil) {
			t.Errorf("Limited until %v, want the refresh's pause %v", until, pausedUntil)
		}
		limitedCalls.Add(1)
	}
	got := waitFor(t, d, repoNames(3), settled)
	for _, r := range got {
		if r.Loaded || r.Reason == "" {
			t.Errorf("%s = %+v, want the limit reason", r.Repository, r)
		}
	}
	if _, requests := search.count(); requests != 0 {
		t.Errorf("%d requests during a pause", requests)
	}
}

func TestDeliveryResetDropsTheQueueAndALateLimit(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
	release := make(chan struct{})
	var started atomic.Int32
	d, _ := newPaced(func(name string) ([]github.AuthoredPullRequest, error) {
		if started.Add(1) > deliveryWorkers {
			return nil, nil
		}
		<-release
		return nil, &github.RateLimitedError{ResumeAt: clock.now().Add(time.Hour)}
	})
	d.Now = clock.now
	var limited atomic.Int32
	d.Limited = func(string, time.Time) { limited.Add(1) }
	names := repoNames(10)
	d.Pulls(names)
	for started.Load() < deliveryWorkers {
		time.Sleep(time.Millisecond)
	}
	d.Reset()
	// The old account's searches finish with a limit after the reset.
	close(release)
	time.Sleep(50 * time.Millisecond)
	if limited.Load() != 0 {
		t.Errorf("a late limit from before the reset paused the refresh")
	}
	for _, r := range d.Pulls(names[:1]) {
		if r.Reason != "" {
			t.Errorf("the new account inherited %+v", r)
		}
	}
}

// TestDeliveryResetRacesARateLimitedSearch interleaves Reset, Pulls and
// searches that end rate limited: whatever the order, the repositories
// searched for the new account never inherit the old account's limit.
func TestDeliveryResetRacesARateLimitedSearch(t *testing.T) {
	for range 30 {
		clock := newTestClock(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
		var old atomic.Bool
		old.Store(true)
		release := make(chan struct{})
		d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) {
			if old.Load() {
				<-release
				return nil, &github.RateLimitedError{ResumeAt: clock.now().Add(time.Hour)}
			}
			return []github.AuthoredPullRequest{{Number: 1}}, nil
		})
		d.Now = clock.now
		names := repoNames(8)
		d.Pulls(names)
		for {
			if searches, _ := search.count(); searches >= deliveryWorkers {
				break
			}
			time.Sleep(time.Millisecond)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); close(release) }()
		go func() {
			defer wg.Done()
			d.Reset()
			old.Store(false)
			d.Pulls(names)
		}()
		wg.Wait()
		if got := waitFor(t, d, names, allLoaded); len(got) != len(names) {
			t.Fatalf("got %d repositories", len(got))
		}
	}
}
