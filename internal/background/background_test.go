package background

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

type registry []repos.Clone

func (r registry) List(context.Context) ([]repos.Clone, error) { return r, nil }

type fakeDiscovery struct {
	polls        int
	observations []discovery.Observation
	err          error
	next         time.Time
}

func (f *fakeDiscovery) Poll(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	f.polls++
	return discovery.Snapshot{Repository: repo.String(), NextPoll: f.next, PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "new"}}}, f.err
}
func (f *fakeDiscovery) Observe(_ context.Context, _ github.Repository, o discovery.Observation) error {
	f.observations = append(f.observations, o)
	return nil
}
func TestPeriodicDiscoverySavedHistoryAndDedup(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := []int{}
	clones := registry{{Repository: "acme/app", CommonDir: "one", Available: true}, {Repository: "acme/app", CommonDir: "one", Available: true}, {Repository: "acme/app", CommonDir: "missing"}}
	s := &Service{Registry: clones, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{1, 9}, nil }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		state := "OPEN"
		if n == 9 {
			state = "MERGED"
		}
		return discovery.Observation{Number: n, State: state, HeadSHA: "new", Checks: "success", FetchedAt: now}, nil
	}}
	s.Sync(context.Background())
	if d.polls != 1 || len(calls) != 2 || len(d.observations) != 2 || d.observations[1].State != "MERGED" {
		t.Fatalf("polls=%d calls=%v observations=%v", d.polls, calls, d.observations)
	}
	s.Sync(context.Background())
	if d.polls != 1 {
		t.Fatal("ignored deadline")
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if d.polls != 2 || len(calls) != 4 {
		t.Fatal("did not poll periodically")
	}
	// There is deliberately no job queue or backend capability in Service.
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/background", nil))
	if !strings.Contains(response.Body.String(), "last_success") || strings.Contains(response.Body.String(), "one") {
		t.Fatal(response.Body.String())
	}
}
func TestOfflineBackoffAndRecovery(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{err: errors.New("offline")}
	calls := 0
	s := &Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, nil
	}}
	s.Sync(context.Background())
	if s.state("acme/app").Error != "discovery_unavailable" || calls != 0 {
		t.Fatal("offline failure hidden")
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if s.state("acme/app").NextRefresh != now.Add(2*time.Minute) {
		t.Fatal("no exponential backoff")
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if d.polls != 2 {
		t.Fatal("polled during backoff")
	}
	now = now.Add(time.Minute)
	d.err = nil
	s.Sync(context.Background())
	if s.state("acme/app").Error != "" || calls != 1 {
		t.Fatal("did not recover")
	}
}
func TestMetadataRateLimitStopsOtherPRsAndRepositories(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := 0
	reset := now.Add(time.Hour)
	s := &Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{2}, nil }, Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, &github.RateLimitedError{ResumeAt: reset, Err: errors.New("limited")}
	}}
	s.Sync(context.Background())
	if calls != 1 || s.state("acme/b").NextRefresh.Before(reset) {
		t.Fatalf("metadata continued calls=%d state=%+v", calls, s.state("acme/b"))
	}
}
func TestRunCancellation(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDiscovery{}
	s := &Service{Registry: registry{{Repository: "acme/a", Available: true}}, Discovery: d, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(ctx context.Context, _ repos.Clone, _ int) (discovery.Observation, error) {
		close(started)
		<-ctx.Done()
		return discovery.Observation{}, ctx.Err()
	}}
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel refresh")
	}
}

func TestRateBudgetEventuallyReachesEveryPRAndRepository(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := 0
	seen := map[string]bool{}
	s := &Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{2, 3}, nil }, Refresh: func(_ context.Context, clone repos.Clone, n int) (discovery.Observation, error) {
		calls++
		if calls%2 == 0 {
			return discovery.Observation{}, &github.RateLimitedError{ResumeAt: now.Add(time.Minute), Err: errors.New("limited")}
		}
		seen[clone.Repository+string(rune('0'+n))] = true
		return discovery.Observation{Number: n, HeadSHA: "head"}, nil
	}}
	for i := 0; i < 14; i++ {
		s.Sync(context.Background())
		now = now.Add(time.Minute)
	}
	for _, repo := range []string{"acme/a", "acme/b"} {
		for _, n := range []string{"1", "2", "3"} {
			if !seen[repo+n] {
				t.Fatalf("starved %s PR%s, reached %v", repo, n, seen)
			}
		}
	}
}
