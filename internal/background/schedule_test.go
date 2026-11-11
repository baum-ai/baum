package background

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// An open pull request without a saved review is left to discovery.
func TestOpenPullRequestWithoutAReviewIsNotRead(t *testing.T) {
	now := time.Now()
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(),
		Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{}, nil
		}}, now)
	for range 5 {
		s.Sync(context.Background())
		now = now.Add(time.Hour)
	}
	if calls != 0 {
		t.Fatalf("read %d times, want none", calls)
	}
}

// A saved review of a merged or closed pull request is retired: it is not
// read again, however long it waits, until the pull request is open again.
func TestClosedReviewIsRetiredUntilReopened(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{closed: true, observations: []discovery.Observation{{Number: 1, State: "MERGED", HeadSHA: "new", FetchedAt: now.Add(-48 * time.Hour)}}}
	docs := newDocuments(map[int]string{1: "merged", 2: "closed"})
	var calls []int
	s := &Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: docs.saved,
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls = append(calls, n)
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: now}, nil
		}}
	for range 10 {
		s.Sync(context.Background())
		now = now.Add(6 * time.Hour)
	}
	if len(calls) != 0 {
		t.Fatalf("calls=%v, want retired reviews left unread", calls)
	}
	// Reopened: discovery lists it open, and it is read as an open one.
	d.closed = false
	d.updated = now
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if !slices.Equal(calls, []int{1}) {
		t.Fatalf("calls=%v, want the reopened pull request read", calls)
	}
}

// A pull request merged while its saved document still says open, as one
// the app merged (discovery.Service.Merged) or one merged while no daemon
// ran, gets one final read, and none after it records the merge.
func TestMergedPullRequestGetsOneFinalRead(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{closed: true, observations: []discovery.Observation{{Number: 1, State: "MERGED", HeadSHA: "new", FetchedAt: now}}}
	docs := newDocuments(map[int]string{1: "open"})
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: docs.saved,
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			o := discovery.Observation{Number: n, State: "MERGED", HeadSHA: "new", FetchedAt: now}
			docs.record(o)
			return o, nil
		}}, now)
	for range 10 {
		s.Sync(context.Background())
		now = now.Add(6 * time.Hour)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want one final read", calls)
	}
}

// A final read that keeps failing stops after maxReadFailures: a closed pull
// request reports no change that would make it worth another try. A refresh
// the person asks for tries it once more.
func TestFailingFinalReadStops(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{closed: true}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(2),
		Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{}, errors.New("git objects missing")
		}}, now)
	for range 20 {
		s.Sync(context.Background())
		now = now.Add(time.Hour)
	}
	if calls != maxReadFailures {
		t.Fatalf("calls=%d, want %d", calls, maxReadFailures)
	}
	s.Asked("acme/app")
	s.Sync(context.Background())
	if calls != maxReadFailures+1 {
		t.Fatalf("calls=%d after a refresh asked for, want one more", calls)
	}
}

// Reads that failed while GitHub did not answer the probes are tried again
// once it does; a read stopped before the outage is not.
func TestReconnectRetriesOnlyReadsThatFailedOffline(t *testing.T) {
	now := time.Now()
	offline := errors.New("dial tcp: no such host")
	d := &fakeDiscovery{closed: true}
	var calls []int
	failing := map[int]bool{2: true}
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(2, 3),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls = append(calls, n)
			if failing[n] {
				return discovery.Observation{}, offline
			}
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "h", FetchedAt: now}, nil
		}}, now)
	for range 5 {
		s.Sync(context.Background())
		now = now.Add(time.Hour)
	}
	// 2 is stopped; 3 was read and waits for its age. Then the probes fail,
	// and so does 3's next read.
	d.probeErr = offline
	failing[3] = true
	now = now.Add(openReadAge)
	for range 5 {
		s.Sync(context.Background())
		now = now.Add(time.Hour)
	}
	before := len(calls)
	d.probeErr = nil
	failing[3] = false
	s.Sync(context.Background())
	if got := calls[before:]; !slices.Equal(got, []int{3}) {
		t.Fatalf("reads after reconnecting %v, want only the one that failed offline", got)
	}
}

// A closed draft's document still records a draft: once a read after its
// generation observed it closed, it is retired too.
func TestClosedDraftIsRetired(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{closed: true}
	calls := 0
	generated := now.Add(-time.Hour)
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now },
		Saved: func(context.Context, repos.Clone) ([]SavedReview, error) {
			return []SavedReview{{Number: 2, State: "draft", Generated: generated}}, nil
		},
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, State: "CLOSED", HeadSHA: "h", FetchedAt: now}, nil
		}}, now)
	for range 10 {
		s.Sync(context.Background())
		now = now.Add(6 * time.Hour)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want one final read", calls)
	}
	// A new process retires it without reading it.
	fresh := settled(&Service{Registry: s.Registry, Discovery: d, Now: s.Now, Saved: s.Saved, Refresh: s.Refresh}, now)
	fresh.Sync(context.Background())
	if calls != 1 {
		t.Fatalf("calls=%d, want none in a new process", calls)
	}
}

// A comment made while a read ran moves GitHub's update time past the one
// the read was due at, though not past when the read finished: it is read
// again.
func TestUpdateDuringAReadIsReadAgain(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{updated: now.Add(-time.Hour)}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			started := now
			now = now.Add(time.Minute)
			if calls == 1 {
				d.mu.Lock()
				d.updated = started.Add(10 * time.Second)
				d.mu.Unlock()
			}
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: started}, nil
		}}, now)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want the update made during the first read read once more", calls)
	}
}

// A refresh the person asks for reads the repository's open saved reviews
// once, whether or not they are due.
func TestAskedRefreshReadsOpenReviewsOnce(t *testing.T) {
	now := time.Now()
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(1),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Asked("acme/other")
	s.Sync(context.Background())
	if calls != 1 {
		t.Fatalf("calls=%d, want one read", calls)
	}
	s.Asked("Acme/App")
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want one more read for the refresh asked for", calls)
	}
}

// Resolving a review thread moves no update time; the unresolved count
// discovery reports makes the saved review due.
func TestUnresolvedCountChangeReadsTheReview(t *testing.T) {
	now := time.Now()
	two, one := 2, 1
	d := &fakeDiscovery{unresolved: &two}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	d.unresolved = &one
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want a read once a thread was resolved", calls)
	}
}

// A finished check moves no update time; the check rollup discovery reports
// makes the saved review due.
func TestCheckRollupChangeReadsTheReview(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{checks: "running"}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: saved(1),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	s.Sync(context.Background())
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 1 {
		t.Fatalf("calls=%d, want one read", calls)
	}
	d.checks = "passing"
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d, want a read once the checks finished", calls)
	}
}

// After a restart, the reads already due by age are spread over
// restartSpread rather than all made in the first pass, while one GitHub
// reports changed is read at once. Half the pull requests are open and due
// by age, half closed and due their final read.
func TestRestartSpreadsDueReads(t *testing.T) {
	now := time.Now()
	const count = 60
	var observations []discovery.Observation
	var numbers []int
	var more []discovery.PullRequest
	for n := 2; n < count+2; n++ {
		numbers = append(numbers, n)
		observations = append(observations, discovery.Observation{Number: n, State: "OPEN", HeadSHA: "h", FetchedAt: now.Add(-3 * openReadAge)})
		if n%2 == 0 {
			more = append(more, discovery.PullRequest{Number: n, HeadSHA: "h", UpdatedAt: now.Add(-4 * openReadAge)})
		}
	}
	// Pull request 1 was updated on GitHub since it was last read.
	observations = append(observations, discovery.Observation{Number: 1, State: "OPEN", HeadSHA: "new", FetchedAt: now.Add(-time.Hour)})
	numbers = append(numbers, 1)
	d := &fakeDiscovery{observations: observations, updated: now.Add(-time.Minute), more: more}
	reads := map[int]int{}
	var first []int
	s := &Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Saved: func(context.Context, repos.Clone) ([]SavedReview, error) {
		var out []SavedReview
		for _, n := range numbers {
			out = append(out, SavedReview{Number: n, State: "open", Head: "h"})
		}
		return out, nil
	}, Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
		reads[n]++
		return discovery.Observation{Number: n, State: "CLOSED", HeadSHA: "h", FetchedAt: now}, nil
	}}
	s.Sync(context.Background())
	for n := range reads {
		first = append(first, n)
	}
	if reads[1] != 1 {
		t.Fatalf("first pass read %v, want the updated pull request among them", first)
	}
	if len(first) > count/4 {
		t.Fatalf("first pass read %d of %d, want them spread", len(first), count+1)
	}
	most := 0
	for range int(restartSpread / time.Minute) {
		now = now.Add(time.Minute)
		before := len(reads)
		s.Sync(context.Background())
		most = max(most, len(reads)-before)
	}
	if len(reads) != count+1 {
		t.Fatalf("read %d of %d within %v", len(reads), count+1, restartSpread)
	}
	for n, times := range reads {
		if times != 1 {
			t.Fatalf("pull request %d read %d times", n, times)
		}
	}
	if most > count/4 {
		t.Fatalf("a pass read %d at once, want them spread", most)
	}
}

// The reads stop once they made refreshRequestsPerHour GitHub requests in
// the last hour, and go on as that hour's requests age out.
func TestReadsStopAtTheHourlyRequestBudget(t *testing.T) {
	now := time.Now()
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(1, 2, 3),
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	s.spend(refreshRequestsPerHour, s.identityEpoch())
	for range 59 {
		s.Sync(context.Background())
		now = now.Add(time.Minute)
	}
	if calls != 0 {
		t.Fatalf("calls=%d with the hour's budget spent, want none", calls)
	}
	now = now.Add(time.Minute)
	s.Sync(context.Background())
	if calls != 3 {
		t.Fatalf("calls=%d once the budget renewed, want all three", calls)
	}
}

// While discovery last read fewer than refreshReserve GraphQL points left,
// the reads wait for the window's reset and leave them to discovery and
// generation.
func TestReadsLeaveTheReserveToDiscovery(t *testing.T) {
	now := time.Now()
	remaining := refreshReserve - 1
	reset := now.Add(10 * time.Minute)
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(1),
		Budget: func(context.Context, github.Repository) (discovery.RateLimit, error) {
			return discovery.RateLimit{Remaining: &remaining, Reset: reset}, nil
		},
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			return discovery.Observation{Number: n, HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	s.Sync(context.Background())
	if calls != 0 {
		t.Fatalf("calls=%d below the reserve, want none", calls)
	}
	if st := s.state("acme/app"); st.Error != "" {
		t.Fatalf("state %+v, want no error for a held read", st)
	}
	// The window reset: what discovery read is out of date.
	now = reset.Add(time.Second)
	s.Sync(context.Background())
	if calls != 1 {
		t.Fatalf("calls=%d after the reset, want the read", calls)
	}
}

// RefreshNow reads what it is asked to whatever Sync's rules say: a retired
// review, one stopped after failures, and past the hour's budget. Its success
// clears the failures.
func TestRefreshNowIgnoresTheSchedule(t *testing.T) {
	now := time.Now()
	clone := repos.Clone{Repository: "acme/app", CommonDir: "a", Available: true}
	fail := true
	calls := 0
	docs := newDocuments(map[int]string{1: "open", 2: "merged"})
	s := settled(&Service{Registry: registry{clone}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: docs.saved,
		Refresh: func(_ context.Context, _ repos.Clone, n int) (discovery.Observation, error) {
			calls++
			if fail {
				return discovery.Observation{}, fmt.Errorf("timed out: %w", context.DeadlineExceeded)
			}
			return discovery.Observation{Number: n, State: "OPEN", HeadSHA: "new", FetchedAt: now}, nil
		}}, now)
	for range maxReadFailures + 2 {
		s.Sync(context.Background())
		now = now.Add(time.Hour)
	}
	if calls != maxReadFailures {
		t.Fatalf("calls=%d, want the read stopped after %d", calls, maxReadFailures)
	}
	s.spend(refreshRequestsPerHour, s.identityEpoch())
	fail = false
	if _, err := s.RefreshNow(context.Background(), clone, []int{1, 2}); err != nil || calls != maxReadFailures+2 {
		t.Fatalf("refresh now err=%v calls=%d", err, calls)
	}
	if r := s.lastRead(readKey(clone, 1)); r.failures != 0 {
		t.Fatalf("read %+v, want its failures cleared", r)
	}
}

// toServer sends every request to a test server.
type toServer struct{ target *url.URL }

func (t toServer) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = t.target.Scheme, t.target.Host, ""
	return http.DefaultTransport.RoundTrip(clone)
}

// The GitHub requests a read makes count toward the hour's budget.
func TestReadsCountTheirRequests(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":null}}`))
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	client := github.New(func(context.Context, string) (string, error) { return "token", nil }, github.Options{Transport: toServer{target}})
	now := time.Now()
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(1, 2),
		Refresh: func(ctx context.Context, clone repos.Clone, n int) (discovery.Observation, error) {
			repo, _ := github.ParseRepository(clone.Repository)
			_, err := client.Fetch(ctx, repo, n, github.FetchOptions{})
			return discovery.Observation{}, err
		}}, now)
	s.Sync(context.Background())
	if spent := s.spentLastHour(); spent != 2 {
		t.Fatalf("spent %d requests, want one for each read", spent)
	}
}

// A read cut short because the pass was cancelled is not a failure: it stays
// unread.
func TestCancelledReadIsNotRecorded(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	clone := repos.Clone{Repository: "acme/app", CommonDir: "a", Available: true}
	s := settled(&Service{Registry: registry{clone}, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now }, Saved: saved(1),
		Refresh: func(ctx context.Context, _ repos.Clone, _ int) (discovery.Observation, error) {
			cancel()
			return discovery.Observation{}, ctx.Err()
		}}, now)
	s.Sync(ctx)
	if r := s.lastRead(readKey(clone, 1)); !r.at.IsZero() || r.failures != 0 {
		t.Fatalf("read %+v, want none recorded", r)
	}
}
