package background

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// registry lists its clones grouped by repository, plus bare picked
// repositories that have no clone.
type registry []repos.Clone

func (r registry) Repositories(context.Context) ([]repos.Repository, error) {
	byName := map[string]int{}
	out := []repos.Repository{}
	for _, c := range r {
		i, ok := byName[c.Repository]
		if !ok {
			i = len(out)
			byName[c.Repository] = i
			out = append(out, repos.Repository{Repository: c.Repository})
		}
		if c.ID != "" || c.CommonDir != "" || c.Available {
			out[i].Clones = append(out[i].Clones, c)
		}
	}
	return out, nil
}

type fakeDiscovery struct {
	polls        int
	forced       []string
	probes       int
	changed      map[string]bool
	observations []discovery.Observation
	err          error
	next         time.Time
	// updated is open pull request 1's last update on GitHub.
	updated time.Time
}

// Poll lists pull request 1 as open, with the latest observation of each
// pull request, as discovery's cache keeps them.
func (f *fakeDiscovery) Poll(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	f.polls++
	latest := map[int]discovery.Observation{}
	for _, o := range f.observations {
		latest[o.Number] = o
	}
	var observations []discovery.Observation
	for _, o := range latest {
		observations = append(observations, o)
	}
	return discovery.Snapshot{Repository: repo.String(), NextPoll: f.next, PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "new", UpdatedAt: f.updated}}, Observations: observations}, f.err
}
func (f *fakeDiscovery) PollNow(ctx context.Context, repo github.Repository) (discovery.Snapshot, error) {
	f.forced = append(f.forced, repo.String())
	return f.Poll(ctx, repo)
}
func (f *fakeDiscovery) Probe(context.Context, []github.Repository) (map[string]bool, error) {
	f.probes++
	return f.changed, nil
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
	if d.polls != 2 || len(calls) != 2 {
		t.Fatalf("polls=%d calls=%v, want a poll and no read of unchanged pull requests", d.polls, calls)
	}
	// An update on GitHub reads that pull request at the next poll.
	d.updated = now
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if len(calls) != 3 || calls[2] != 1 {
		t.Fatalf("calls=%v, want the updated pull request read", calls)
	}
	// Unchanged ones are read again once their evidence is old.
	now = now.Add(closedReadAge)
	s.Sync(context.Background())
	if len(calls) != 5 {
		t.Fatalf("calls=%v, want both read again after %v", calls, closedReadAge)
	}
	// There is deliberately no job queue or backend capability in Service.
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/background", nil))
	if !strings.Contains(response.Body.String(), "last_success") || strings.Contains(response.Body.String(), "one") {
		t.Fatal(response.Body.String())
	}
}
func TestRefreshConflictIsRetriedWithoutFailure(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	s := &Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{1}, nil }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		return discovery.Observation{Number: n, HeadSHA: "new"}, fmt.Errorf("%w; refresh again using the latest document", store.ErrConflict)
	}}
	s.Sync(context.Background())
	state := s.state("acme/app")
	if state.Error != "" || len(state.Problems) != 0 || state.Failures != 0 || state.NextRefresh != now.Add(time.Minute) || len(d.observations) != 1 {
		t.Fatalf("conflict treated as failure: %+v", state)
	}
}

// Successful discovery snapshots go to Discovered (the daemon's automatic
// scheduler); failed polls do not, so a stale inbox never looks like news.
func TestDiscoveredReceivesOnlySuccessfulSnapshots(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{err: errors.New("offline")}
	var seen []discovery.Snapshot
	s := &Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		return discovery.Observation{}, nil
	}, Discovered: func(_ context.Context, snapshot discovery.Snapshot) { seen = append(seen, snapshot) }}
	s.Sync(context.Background())
	if len(seen) != 0 {
		t.Fatalf("failed poll delivered %+v", seen)
	}
	d.err = nil
	now = now.Add(time.Hour)
	s.Sync(context.Background())
	if len(seen) != 1 || seen[0].Repository != "acme/app" || len(seen[0].PullRequests) != 1 {
		t.Fatalf("snapshot not delivered %+v", seen)
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
	for _, count := range []int{2, 3, 4} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			now := time.Now()
			d := &fakeDiscovery{}
			calls := 0
			seen := map[string]bool{}
			s := &Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) {
				var saved []int
				for n := 2; n <= count; n++ {
					saved = append(saved, n)
				}
				return saved, nil
			}, Refresh: func(_ context.Context, clone repos.Clone, n int) (discovery.Observation, error) {
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
				for n := 1; n <= count; n++ {
					if !seen[repo+string(rune('0'+n))] {
						t.Fatalf("starved %s PR%d, reached %v", repo, n, seen)
					}
				}
			}
		})
	}
}

// RefreshNow shares the host's rate pause with Sync in both directions.
func TestRefreshNowSharesTheRatePause(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	var calls []int
	limited := false
	reset := now.Add(time.Hour)
	changed := 0
	clone := repos.Clone{Repository: "acme/app", CommonDir: "one", Available: true}
	s := &Service{Registry: registry{clone}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Changed: func() { changed++ }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		if limited {
			return discovery.Observation{}, &github.RateLimitedError{ResumeAt: reset, Err: errors.New("limited")}
		}
		return discovery.Observation{Number: n, HeadSHA: "new"}, nil
	}}
	observations, err := s.RefreshNow(context.Background(), clone, []int{3, 4})
	if err != nil || len(observations) != 2 || len(d.observations) != 2 || changed != 1 {
		t.Fatalf("refresh now %v %v observed=%d changed=%d", observations, err, len(d.observations), changed)
	}
	limited = true
	calls = nil
	if _, err := s.RefreshNow(context.Background(), clone, []int{3, 4}); !errors.As(err, new(*github.RateLimitedError)) || len(calls) != 1 {
		t.Fatalf("rate limit did not stop: %v calls=%v", err, calls)
	}
	limited = false
	calls = nil
	if _, err := s.RefreshNow(context.Background(), clone, []int{3}); !errors.As(err, new(*github.RateLimitedError)) || len(calls) != 0 {
		t.Fatalf("paused host refreshed: %v calls=%v", err, calls)
	}
	s.Sync(context.Background())
	if state := s.state("acme/app"); state.Error != "rate_limited" || state.NextRefresh.Before(reset) || len(calls) != 0 {
		t.Fatalf("sync ignored the pause: %+v calls=%v", state, calls)
	}
}

func TestIdentityResetRejectsOldBackoffWrites(t *testing.T) {
	s := &Service{}
	old := s.identityEpoch()
	s.put(State{Repository: "acme/app", NextRefresh: time.Now().Add(time.Hour)}, old)
	s.setPause("github.com", time.Now().Add(time.Hour), old)
	s.ResetIdentity()
	s.put(State{Repository: "acme/app", NextRefresh: time.Now().Add(time.Hour)}, old)
	s.setPause("github.com", time.Now().Add(time.Hour), old)
	if !s.state("acme/app").NextRefresh.IsZero() || !s.pause("github.com").IsZero() {
		t.Fatal("old account backoff survived reset")
	}
}

func TestPickedRepositoryWithoutCloneIsPolledNotRefreshed(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	var discovered []string
	refreshed := 0
	s := &Service{Registry: registry{{Repository: "acme/bare"}, {Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now },
		Discovered: func(_ context.Context, snap discovery.Snapshot) { discovered = append(discovered, snap.Repository) },
		Saved:      func(context.Context, repos.Clone) ([]int, error) { return nil, nil },
		Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
			refreshed++
			return discovery.Observation{}, nil
		}}
	s.Sync(context.Background())
	if d.polls != 2 || len(discovered) != 2 {
		t.Fatalf("polls=%d discovered=%v, want both repositories polled", d.polls, discovered)
	}
	if refreshed != 1 {
		t.Fatalf("refreshed %d pull requests, want only the repository with a clone", refreshed)
	}
	if st := s.state("acme/bare"); st.Error != "" || st.LastSuccess.IsZero() {
		t.Fatalf("clone-less state = %+v", st)
	}
}

func TestProbedChangePollsBeforeItsSchedule(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	s := &Service{Registry: registry{{Repository: "acme/a", Available: true}, {Repository: "acme/b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		return discovery.Observation{Number: n}, nil
	}}
	s.Sync(context.Background())
	if d.probes != 1 || d.polls != 2 || len(d.forced) != 0 {
		t.Fatalf("first pass probes=%d polls=%d forced=%v", d.probes, d.polls, d.forced)
	}
	// Within the interval neither repository is due and no probe runs.
	now = now.Add(30 * time.Second)
	d.changed = map[string]bool{"acme/b": true}
	s.Sync(context.Background())
	if d.probes != 1 || d.polls != 2 {
		t.Fatalf("probed or polled early: probes=%d polls=%d", d.probes, d.polls)
	}
	// The next probe finds acme/b changed: it polls now, past discovery's
	// backoff, and acme/a keeps its scheduled poll.
	now = now.Add(30 * time.Second)
	s.put(State{Repository: "acme/a", NextRefresh: now.Add(time.Hour)}, s.identityEpoch())
	s.put(State{Repository: "acme/b", NextRefresh: now.Add(time.Hour)}, s.identityEpoch())
	s.Sync(context.Background())
	if d.probes != 2 || d.polls != 3 || len(d.forced) != 1 || d.forced[0] != "acme/b" {
		t.Fatalf("changed repository not polled now: probes=%d polls=%d forced=%v", d.probes, d.polls, d.forced)
	}
}

// A saved pull request observed open that left the open ones is read at the
// next pass to learn whether it merged or closed, and not again until old.
func TestPullRequestThatLeftTheOpenOnesIsReadOnce(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{observations: []discovery.Observation{{Number: 5, HeadSHA: "h", State: "OPEN", FetchedAt: now.Add(-time.Minute)}}}
	var calls []int
	s := &Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{5}, nil }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		return discovery.Observation{Number: n, HeadSHA: "h", State: "MERGED", FetchedAt: now}, nil
	}}
	s.Sync(context.Background())
	if len(calls) != 2 || !slices.Contains(calls, 5) {
		t.Fatalf("calls=%v, want the pull request that left the open ones read", calls)
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if len(calls) != 2 {
		t.Fatalf("calls=%v, want no read of a merged pull request", calls)
	}
}

// Every due repository is polled before any pull request is read, so one
// repository's reads never hold back another's poll.
func TestRepositoriesArePolledBeforeReads(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	var pollsAtFirstRead int
	s := &Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		if pollsAtFirstRead == 0 {
			pollsAtFirstRead = d.polls
		}
		return discovery.Observation{}, nil
	}}
	s.Sync(context.Background())
	if pollsAtFirstRead != 2 {
		t.Fatalf("first read after %d polls, want 2", pollsAtFirstRead)
	}
}

// A pass stops starting reads after refreshBudget; the rest go first next pass.
func TestReadsStopAtTheBudget(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	var calls []int
	s := &Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]int, error) { return []int{2, 3}, nil }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		now = now.Add(refreshBudget / 2)
		return discovery.Observation{}, nil
	}}
	s.Sync(context.Background())
	if len(calls) != 2 {
		t.Fatalf("calls=%v, want two reads within the budget", calls)
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if len(calls) != 3 || calls[2] != 3 {
		t.Fatalf("calls=%v, want the deferred read next", calls)
	}
}

// A failed read is retried with a growing backoff, not every pass.
func TestFailedReadBacksOff(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := 0
	s := &Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil }, Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, errors.New("git objects missing")
	}}
	for i, want := range []int{1, 2, 2, 3, 3, 3, 3, 4} {
		s.Sync(context.Background())
		if calls != want {
			t.Fatalf("pass %d: calls=%d, want %d", i, calls, want)
		}
		now = now.Add(time.Minute)
	}
}
