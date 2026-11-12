package analytics

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// pacedSearch is a fake GitHub search that records how many searches ran at
// once and when each started on a fake clock, which the Delivery's pacing
// advances instead of sleeping.
type pacedSearch struct {
	mu       sync.Mutex
	clock    time.Time
	began    time.Time
	inFlight int
	maxSeen  int
	starts   []time.Time
	repos    []string
	hold     time.Duration
	answer   func(name string) ([]github.AuthoredPullRequest, error)
}

// scale is how much faster the fake pacing clock runs than real time, so the
// tests cover minutes of pacing in milliseconds with real goroutines.
const scale = 200

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
	p.starts = append(p.starts, p.tick())
	p.repos = append(p.repos, repo.Owner+"/"+repo.Name)
	p.mu.Unlock()
	time.Sleep(p.hold)
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return p.answer(repo.Owner + "/" + repo.Name)
}

func newPaced(answer func(string) ([]github.AuthoredPullRequest, error)) (*Delivery, *pacedSearch) {
	search := &pacedSearch{clock: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), began: time.Now(), hold: 5 * time.Millisecond, answer: answer}
	return &Delivery{Search: search, tick: search.tick, sleep: search.sleep}, search
}

func waitFor(t *testing.T, d *Delivery, names []string, done func([]repositoryPulls) bool) []repositoryPulls {
	t.Helper()
	var got []repositoryPulls
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
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

func TestDeliveryBoundsConcurrencyAndPacesSearches(t *testing.T) {
	d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) {
		return []github.AuthoredPullRequest{{Number: 1}}, nil
	})
	names := repoNames(100)
	got := waitFor(t, d, names, settled)
	for _, r := range got {
		if !r.Loaded || r.Reason != "" {
			t.Fatalf("%s = %+v", r.Repository, r)
		}
	}
	search.mu.Lock()
	defer search.mu.Unlock()
	if len(search.starts) != 100 {
		t.Fatalf("searches = %d, want 100", len(search.starts))
	}
	if search.maxSeen > deliveryWorkers {
		t.Errorf("in flight = %d, want at most %d", search.maxSeen, deliveryWorkers)
	}
	// Never more than 30 search starts in any minute of the fake clock.
	for i := range search.starts {
		n := 0
		for _, at := range search.starts[i:] {
			if at.Sub(search.starts[i]) < time.Minute {
				n++
			}
		}
		if n > 30 {
			t.Fatalf("%d searches started within a minute of %v", n, search.starts[i])
		}
	}
}

func TestDeliveryPagesCountTowardsThePace(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) {
		return make([]github.AuthoredPullRequest, 5*searchPageSize), nil
	})
	d.Now = func() time.Time { return now }
	names := repoNames(4)
	waitFor(t, d, names, settled)
	// The TTL passes: the refresh knows each search took five requests.
	now = now.Add(2 * DeliveryTTL)
	search.mu.Lock()
	first := len(search.starts)
	search.mu.Unlock()
	waitFor(t, d, names, func(rs []repositoryPulls) bool {
		search.mu.Lock()
		defer search.mu.Unlock()
		return len(search.starts) == first+4
	})
	search.mu.Lock()
	defer search.mu.Unlock()
	span := search.starts[first+3].Sub(search.starts[first])
	if want := 12 * searchSpacing; span < want {
		t.Errorf("refresh searches spread over %v, want at least %v", span, want)
	}
}

func TestDeliveryRateLimitBacksOffAndKeepsTheOthers(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	resume := now.Add(20 * time.Minute)
	d, search := newPaced(func(name string) ([]github.AuthoredPullRequest, error) {
		if name == "acme/repo003" {
			return nil, &github.RateLimitedError{ResumeAt: resume}
		}
		return []github.AuthoredPullRequest{{Number: 7}}, nil
	})
	d.Now = func() time.Time { return now }
	var paused []time.Time
	d.Limited = func(host string, until time.Time) { paused = append(paused, until) }
	names := repoNames(20)
	got := waitFor(t, d, names, settled)

	search.mu.Lock()
	ran := len(search.starts)
	search.mu.Unlock()
	if ran >= 20 {
		t.Errorf("%d searches ran; the rate limit should hold the queue back", ran)
	}
	loaded, limited := 0, 0
	for _, r := range got {
		switch {
		case r.Loaded && r.Reason == "":
			loaded++
		case r.Reason != "":
			limited++
		}
	}
	if loaded < 1 || limited < 1 || loaded+limited != 20 {
		t.Fatalf("loaded %d, limited %d", loaded, limited)
	}
	if len(paused) != 1 || !paused[0].Equal(resume) {
		t.Errorf("Limited calls = %v, want one until %v", paused, resume)
	}
	// Nothing is searched again before the reset...
	now = now.Add(10 * time.Minute)
	d.Pulls(names)
	time.Sleep(50 * time.Millisecond)
	search.mu.Lock()
	again := len(search.starts)
	search.mu.Unlock()
	if again != ran {
		t.Errorf("searched again during the pause: %d -> %d", ran, again)
	}
	// ...and afterwards the held-back repositories are searched.
	now = resume.Add(time.Second)
	d.Limited = nil
	search.answer = func(string) ([]github.AuthoredPullRequest, error) {
		return []github.AuthoredPullRequest{{Number: 7}}, nil
	}
	got = waitFor(t, d, names, func(rs []repositoryPulls) bool {
		for _, r := range rs {
			if !r.Loaded || r.Reason != "" {
				return false
			}
		}
		return true
	})
	if len(got) != 20 {
		t.Fatalf("got %d repositories", len(got))
	}
}

func TestDeliveryHonoursThePausedHost(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	d, search := newPaced(func(string) ([]github.AuthoredPullRequest, error) { return nil, nil })
	d.Now = func() time.Time { return now }
	d.Paused = func(string) time.Time { return now.Add(time.Hour) }
	got := waitFor(t, d, repoNames(3), settled)
	for _, r := range got {
		if r.Loaded || r.Reason == "" {
			t.Errorf("%s = %+v, want the limit reason", r.Repository, r)
		}
	}
	search.mu.Lock()
	defer search.mu.Unlock()
	if len(search.starts) != 0 {
		t.Errorf("%d searches ran during a pause", len(search.starts))
	}
}
