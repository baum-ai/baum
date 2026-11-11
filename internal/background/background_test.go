package background

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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
	// mu guards the fields Sync's concurrent polls reach.
	mu           sync.Mutex
	polls        int
	forced       []string
	probes       int
	changed      map[string]bool
	probeErr     error
	observations []discovery.Observation
	err          error
	next         time.Time
	// updated is open pull request 1's last update on GitHub.
	updated time.Time
	// settling reports a poll that found a new head.
	settling bool
	// closed leaves pull request 1 out of the open ones.
	closed bool
	// head and checks are pull request 1's; head defaults to "new".
	head, checks string
}

// Poll lists pull request 1 as open, with the latest observation of each
// pull request, as discovery's cache keeps them.
func (f *fakeDiscovery) Poll(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.poll(repo)
}
func (f *fakeDiscovery) poll(repo github.Repository) (discovery.Snapshot, error) {
	f.polls++
	latest := map[int]discovery.Observation{}
	for _, o := range f.observations {
		latest[o.Number] = o
	}
	var observations []discovery.Observation
	for _, o := range latest {
		observations = append(observations, o)
	}
	open := []discovery.PullRequest{{Number: 1, HeadSHA: cmp.Or(f.head, "new"), UpdatedAt: f.updated, Checks: f.checks}}
	if f.closed {
		open = nil
	}
	return discovery.Snapshot{Repository: repo.String(), NextPoll: f.next, Settling: f.settling, PullRequests: open, Observations: observations}, f.err
}
func (f *fakeDiscovery) PollNow(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forced = append(f.forced, repo.String())
	return f.poll(repo)
}
func (f *fakeDiscovery) Probe(context.Context, []github.Repository) (map[string]bool, error) {
	f.probes++
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return f.changed, nil
}
func (f *fakeDiscovery) Observe(_ context.Context, _ github.Repository, o discovery.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observations = append(f.observations, o)
	return nil
}

// saved lists saved reviews of open pull requests.
func saved(numbers ...int) func(context.Context, repos.Clone) ([]SavedReview, error) {
	return func(context.Context, repos.Clone) ([]SavedReview, error) {
		var out []SavedReview
		for _, n := range numbers {
			out = append(out, SavedReview{Number: n, State: "open"})
		}
		return out, nil
	}
}

// documents are saved reviews whose recorded state a read updates, as
// Local.Refresh records the pull request's state in the saved document.
type documents struct {
	mu     sync.Mutex
	states map[int]string
}

func newDocuments(states map[int]string) *documents { return &documents{states: states} }

func (d *documents) saved(context.Context, repos.Clone) ([]SavedReview, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []SavedReview
	for n, state := range d.states {
		out = append(out, SavedReview{Number: n, State: state})
	}
	slices.SortFunc(out, func(a, b SavedReview) int { return a.Number - b.Number })
	return out, nil
}

// record stores what a read observed in the document.
func (d *documents) record(o discovery.Observation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.states[o.Number]; ok && o.State != "" {
		d.states[o.Number] = strings.ToLower(o.State)
	}
}

// settled is a service whose process started long enough ago that the
// restart spread is over.
func settled(s *Service, now time.Time) *Service {
	s.started = now.Add(-restartSpread)
	return s
}

func TestPeriodicDiscoverySavedHistoryAndDedup(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := []int{}
	docs := newDocuments(map[int]string{1: "open", 9: "open"})
	clones := registry{{Repository: "acme/app", CommonDir: "one", Available: true}, {Repository: "acme/app", CommonDir: "one", Available: true}, {Repository: "acme/app", CommonDir: "missing"}}
	s := settled(&Service{Registry: clones, Discovery: d, Now: func() time.Time { return now }, Saved: docs.saved, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		state := "OPEN"
		if n == 9 {
			state = "MERGED"
		}
		o := discovery.Observation{Number: n, State: state, HeadSHA: "new", Checks: "success", FetchedAt: now}
		docs.record(o)
		return o, nil
	}}, now)
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
	// An unchanged open one is read again once its evidence is old; the
	// merged one, whose document records it, never is.
	now = now.Add(openReadAge)
	s.Sync(context.Background())
	if len(calls) != 4 || calls[3] != 1 {
		t.Fatalf("calls=%v, want the open one read again after %v", calls, openReadAge)
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
	s := settled(&Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1), Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		return discovery.Observation{Number: n, HeadSHA: "new"}, fmt.Errorf("%w; refresh again using the latest document", store.ErrConflict)
	}}, now)
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
	s := &Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
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
	s := settled(&Service{Registry: registry{{Repository: "acme/app", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, nil
	}}, now)
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
	s := settled(&Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(2), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, &github.RateLimitedError{ResumeAt: reset, Err: errors.New("limited")}
	}}, now)
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
	s := settled(&Service{Registry: registry{{Repository: "acme/a", Available: true}}, Discovery: d, Saved: saved(1), Refresh: func(ctx context.Context, _ repos.Clone, _ int) (discovery.Observation, error) {
		close(started)
		<-ctx.Done()
		return discovery.Observation{}, ctx.Err()
	}}, time.Now())
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
			var numbers []int
			for n := 1; n <= count; n++ {
				numbers = append(numbers, n)
			}
			s := settled(&Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(numbers...), Refresh: func(_ context.Context, clone repos.Clone, n int) (discovery.Observation, error) {
				calls++
				if calls%2 == 0 {
					return discovery.Observation{}, &github.RateLimitedError{ResumeAt: now.Add(time.Minute), Err: errors.New("limited")}
				}
				seen[clone.Repository+string(rune('0'+n))] = true
				return discovery.Observation{Number: n, HeadSHA: "head"}, nil
			}}, now)
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
	s := &Service{Registry: registry{clone}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(), Changed: func() { changed++ }, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
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
	s := settled(&Service{Registry: registry{{Repository: "acme/bare"}, {Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now },
		Discovered: func(_ context.Context, snap discovery.Snapshot) { discovered = append(discovered, snap.Repository) },
		Saved:      saved(1),
		Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
			refreshed++
			return discovery.Observation{}, nil
		}}, now)
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
	s := &Service{Registry: registry{{Repository: "acme/a", Available: true}, {Repository: "acme/b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(), Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
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

// Offline, a failed poll backs off for up to 15 minutes. Once a probe
// succeeds again, every repository whose poll failed polls at once, so the
// failure clears without waiting out that backoff.
func TestReconnectPollsFailedRepositoriesAtOnce(t *testing.T) {
	now := time.Now()
	offline := errors.New("dial tcp: lookup api.github.com: no such host")
	d := &fakeDiscovery{err: offline, probeErr: offline}
	s := &Service{Registry: registry{{Repository: "acme/a"}, {Repository: "acme/b"}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(), Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		return discovery.Observation{Number: n}, nil
	}}
	for range 5 {
		now = maxTime(now, s.state("acme/a").NextRefresh)
		s.Sync(context.Background())
	}
	if st := s.state("acme/a"); st.Error != "discovery_unavailable" || st.NextRefresh.Sub(now) < 8*time.Minute {
		t.Fatalf("offline state = %+v, want a long backoff", st)
	}
	// Back online, a minute later: nothing changed on GitHub, but both
	// repositories poll now and their failures clear.
	now = now.Add(time.Minute)
	d.err, d.probeErr = nil, nil
	polls := d.polls
	s.Sync(context.Background())
	if d.polls != polls+2 || len(d.forced) != 2 {
		t.Fatalf("polls=%d forced=%v, want both repositories polled at once", d.polls-polls, d.forced)
	}
	for _, name := range []string{"acme/a", "acme/b"} {
		if st := s.state(name); st.Error != "" || st.Failures != 0 {
			t.Fatalf("%s state = %+v, want the failure cleared", name, st)
		}
	}
	// Later successful probes do not force polls again.
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if len(d.forced) != 2 {
		t.Fatalf("forced=%v, want no further forced polls", d.forced)
	}
}

// A saved pull request observed open that left the open ones is read at the
// next pass to learn whether it merged or closed, and not again until old.
func TestPullRequestThatLeftTheOpenOnesIsReadOnce(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{observations: []discovery.Observation{{Number: 5, HeadSHA: "h", State: "OPEN", FetchedAt: now.Add(-time.Minute)}}}
	var calls []int
	docs := newDocuments(map[int]string{5: "open"})
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: docs.saved, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		o := discovery.Observation{Number: n, HeadSHA: "h", State: "MERGED", FetchedAt: now}
		docs.record(o)
		return o, nil
	}}, now)
	s.Sync(context.Background())
	if !slices.Equal(calls, []int{5}) {
		t.Fatalf("calls=%v, want only the saved pull request that left the open ones read", calls)
	}
	for range 3 {
		now = now.Add(24 * time.Hour)
		s.Sync(context.Background())
	}
	if len(calls) != 1 {
		t.Fatalf("calls=%v, want no read of a merged pull request", calls)
	}
}

// Every due repository is polled before any pull request is read, so one
// repository's reads never hold back another's poll.
func TestRepositoriesArePolledBeforeReads(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	var pollsAtFirstRead int
	s := settled(&Service{Registry: registry{{Repository: "acme/a", CommonDir: "a", Available: true}, {Repository: "acme/b", CommonDir: "b", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		if pollsAtFirstRead == 0 {
			pollsAtFirstRead = d.polls
		}
		return discovery.Observation{}, nil
	}}, now)
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
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1, 2, 3), Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls = append(calls, n)
		now = now.Add(refreshBudget / 2)
		return discovery.Observation{}, nil
	}}, now)
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

// A failed read is retried with a growing backoff, not every pass, and
// after maxReadFailures in a row only once the pull request changes: its
// updated time, its head, a day later, or a retry asked for.
func TestFailedReadBacksOffThenWaitsForAChange(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(1), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		calls++
		return discovery.Observation{}, errors.New("timed out")
	}}, now)
	pass := func(step time.Duration, want int) {
		t.Helper()
		now = now.Add(step)
		s.Sync(context.Background())
		if calls != want {
			t.Fatalf("calls=%d, want %d", calls, want)
		}
	}
	for _, want := range []int{1, 2, 2, 3, 3, 3, 3} {
		pass(time.Minute, want)
	}
	// Stopped: hours pass without a read.
	pass(openReadAge*3, 3)
	// GitHub reports an update: one read, which fails and stops again.
	d.updated = now
	pass(time.Minute, 4)
	pass(time.Minute, 4)
	pass(time.Hour, 4)
	// A new head: one read.
	d.head = "newer"
	pass(time.Minute, 5)
	pass(time.Hour, 5)
	// The person refreshes the repository: one read.
	s.Retry("acme/app")
	pass(time.Minute, 6)
	pass(time.Hour, 6)
	// Unchanged for a day: one more read.
	pass(stoppedRetry, 7)
	pass(time.Minute, 7)
}

// A poll that found a new head is due again at discovery's sooner deadline,
// and Run wakes for it; otherwise the interval holds.
func TestSettlingPollIsDueSooner(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{next: now.Add(15 * time.Second), settling: true}
	s := &Service{Registry: registry{{Repository: "acme/app"}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved()}
	s.Sync(context.Background())
	if next := s.state("acme/app").NextRefresh; !next.Equal(d.next) {
		t.Fatalf("next refresh in %v, want 15s", next.Sub(now))
	}
	if wait := s.wait(); wait != 15*time.Second {
		t.Fatalf("Run waits %v, want 15s", wait)
	}
	now = now.Add(15 * time.Second)
	d.settling = false
	d.next = now.Add(15 * time.Second)
	s.Sync(context.Background())
	if d.polls != 2 || !s.state("acme/app").NextRefresh.Equal(now.Add(time.Minute)) {
		t.Fatalf("polls=%d next refresh in %v", d.polls, s.state("acme/app").NextRefresh.Sub(now))
	}
	if wait := s.wait(); wait != 30*time.Second {
		t.Fatalf("Run waits %v, want 30s", wait)
	}
}

// A pull request this worker read while open, and that then left the open
// ones, is read once more to learn that it merged or closed.
func TestPullRequestReadOpenIsReadAgainWhenItLeaves(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{}
	state := "OPEN"
	calls := 0
	docs := newDocuments(map[int]string{1: "open"})
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: docs.saved, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		calls++
		o := discovery.Observation{Number: n, HeadSHA: "new", State: state, FetchedAt: now}
		docs.record(o)
		return o, nil
	}}, now)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	d.closed, state = true, "MERGED"
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want a read after it left the open ones", calls)
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	now = now.Add(24 * time.Hour)
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want no read of a merged pull request", calls)
	}
}

// blockingDiscovery holds each poll until pollConcurrency of them are under
// way at once, or a second passes.
type blockingDiscovery struct {
	fakeDiscovery
	inflight, most int
	full           chan struct{}
	// filled closes full once: a later poll may bring pollConcurrency under
	// way again after an earlier one finished.
	filled sync.Once
}

func (b *blockingDiscovery) Poll(ctx context.Context, repo github.Repository) (discovery.Snapshot, error) {
	b.mu.Lock()
	b.inflight++
	b.most = max(b.most, b.inflight)
	if b.inflight == pollConcurrency {
		b.filled.Do(func() { close(b.full) })
	}
	b.mu.Unlock()
	select {
	case <-b.full:
	case <-time.After(time.Second):
	}
	b.mu.Lock()
	b.inflight--
	b.mu.Unlock()
	return b.fakeDiscovery.Poll(ctx, repo)
}

// Repositories poll a few at once, and every one is still polled and its
// snapshot delivered.
func TestRepositoriesPollConcurrently(t *testing.T) {
	now := time.Now()
	d := &blockingDiscovery{full: make(chan struct{})}
	var seen []string
	s := &Service{Registry: registry{{Repository: "acme/a"}, {Repository: "acme/b"}, {Repository: "acme/c"}, {Repository: "acme/d"}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		return discovery.Observation{}, nil
	}, Discovered: func(_ context.Context, snapshot discovery.Snapshot) { seen = append(seen, snapshot.Repository) }}
	s.Sync(context.Background())
	if d.most != pollConcurrency || d.polls != 4 {
		t.Fatalf("at most %d polls at once of %d, want %d", d.most, d.polls, pollConcurrency)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"acme/a", "acme/b", "acme/c", "acme/d"}) {
		t.Fatalf("delivered %v", seen)
	}
	for _, name := range seen {
		if st := s.state(name); st.Error != "" || st.NextRefresh.IsZero() {
			t.Fatalf("%s state %+v", name, st)
		}
	}
}

// A poll that ran out of time is reported as a timeout, not as GitHub being
// unavailable or the account changing.
func TestPollTimeoutIsReportedAsATimeout(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{err: fmt.Errorf("poll: %w", discovery.ErrTimeout)}
	s := &Service{Registry: registry{{Repository: "acme/app"}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(), Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
		return discovery.Observation{}, nil
	}}
	s.Sync(context.Background())
	if st := s.state("acme/app"); st.Error != "discovery_timeout" || st.Failures != 1 {
		t.Fatalf("state %+v", st)
	}
}
