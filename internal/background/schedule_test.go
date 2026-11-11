package background

import (
	"context"
	"errors"
	"fmt"
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
// request reports no change that would make it worth another try.
func TestFailingFinalReadStops(t *testing.T) {
	now := time.Now()
	d := &fakeDiscovery{closed: true}
	calls := 0
	s := settled(&Service{Registry: registry{{Repository: "acme/app", CommonDir: "a", Available: true}}, Discovery: d, Now: func() time.Time { return now }, Interval: time.Minute, Saved: saved(1),
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
	// GitHub answering again after it did not retries it once.
	s.Retry("")
	s.Sync(context.Background())
	if calls != maxReadFailures+1 {
		t.Fatalf("calls=%d after a retry, want one more", calls)
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
// reports changed is read at once.
func TestRestartSpreadsDueReads(t *testing.T) {
	now := time.Now()
	const count = 60
	var observations []discovery.Observation
	var numbers []int
	for n := 2; n < count+2; n++ {
		numbers = append(numbers, n)
		observations = append(observations, discovery.Observation{Number: n, State: "OPEN", HeadSHA: "h", FetchedAt: now.Add(-3 * openReadAge)})
	}
	// Pull request 1 was updated on GitHub since it was last read.
	observations = append(observations, discovery.Observation{Number: 1, State: "OPEN", HeadSHA: "new", FetchedAt: now.Add(-time.Hour)})
	numbers = append(numbers, 1)
	d := &fakeDiscovery{observations: observations, updated: now.Add(-time.Minute)}
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
