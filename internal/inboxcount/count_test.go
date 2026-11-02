package inboxcount

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
)

var now = time.Unix(1_800_000_000, 0)

func entry(number int, mutate func(*reviewindex.Entry)) reviewindex.Entry {
	e := reviewindex.Entry{PullRequest: discovery.PullRequest{Repository: "acme/a", Number: number, State: "OPEN", HeadSHA: "head"}}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

func mine(number int, mutate func(*reviewindex.Entry)) reviewindex.Entry {
	return entry(number, func(e *reviewindex.Entry) {
		e.PullRequest.AuthoredByMe = true
		if mutate != nil {
			mutate(e)
		}
	})
}

func one(t *testing.T, want int, e reviewindex.Entry) {
	t.Helper()
	if got := Count([]reviewindex.Entry{e}, nil, nil, Snoozes{}, now); got != want {
		t.Fatalf("Count(%+v) = %d, want %d", e, got, want)
	}
}

func TestCountsWhatTheInboxLists(t *testing.T) {
	yes := true
	unresolved := 2
	// Asked for a review, with or without a clone to review it in.
	one(t, 1, entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe = true }))
	one(t, 1, entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe, e.NeedsClone = true, true }))
	// Their own, to fix.
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Review = "changes_requested" }))
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Merge = "conflict" }))
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Checks = "failing" }))
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Unresolved = &unresolved }))
	// A draft with failing checks still needs fixing.
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Checks, e.PullRequest.Draft = "failing", true }))
	// To merge, but not a draft or while checks run.
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.Merge = "ready" }))
	one(t, 0, mine(1, func(e *reviewindex.Entry) { e.Merge, e.PullRequest.Draft = "ready", true }))
	one(t, 0, mine(1, func(e *reviewindex.Entry) { e.Merge, e.Checks = "ready", "running" }))
	// To update: behind, pushable, not a draft or a fork.
	one(t, 1, mine(1, func(e *reviewindex.Entry) { e.PullRequest.Behind, e.PullRequest.ViewerCanPush = true, &yes }))
	one(t, 0, mine(1, func(e *reviewindex.Entry) { e.PullRequest.Behind = true }))
	one(t, 0, mine(1, func(e *reviewindex.Entry) {
		e.PullRequest.Behind, e.PullRequest.ViewerCanPush, e.PullRequest.Fork = true, &yes, true
	}))
	// Nothing to do, someone else's, closed or only saved.
	one(t, 0, mine(1, nil))
	one(t, 0, entry(1, func(e *reviewindex.Entry) { e.Checks = "failing" }))
	one(t, 0, entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe, e.PullRequest.State = true, "MERGED" }))
	one(t, 0, entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe, e.Historical = true, true }))
	// An unknown state is open, as in the app.
	one(t, 1, entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe, e.PullRequest.State = true, "" }))
}

func TestAPullRequestCountsOnceAcrossClones(t *testing.T) {
	failing := func(e *reviewindex.Entry) { e.Checks = "failing" }
	entries := []reviewindex.Entry{
		mine(1, failing),
		mine(1, func(e *reviewindex.Entry) {
			e.CloneID, e.PullRequest.Repository, e.Checks = "other", "Acme/A", "failing"
		}),
		mine(2, failing),
	}
	if got := Count(entries, nil, nil, Snoozes{}, now); got != 2 {
		t.Fatal(got)
	}
	// The clone with a saved review speaks for the pull request.
	entries = []reviewindex.Entry{
		mine(3, nil),
		mine(3, func(e *reviewindex.Entry) { e.VersionCount, e.Merge = 1, "ready" }),
	}
	if got := Count(entries, nil, nil, Snoozes{}, now); got != 1 {
		t.Fatal(got)
	}
}

func TestAnActiveFixLeavesNothingToDo(t *testing.T) {
	e := mine(1, func(e *reviewindex.Entry) { e.Checks = "failing" })
	fix := func(state, group, repository string) jobs.Job {
		j := jobs.Job{State: state}
		j.Kind, j.Group, j.Repository, j.PR = "fix", group, repository, 1
		return j
	}
	for _, tc := range []struct {
		job  jobs.Job
		want int
	}{
		{fix("running", "", "ACME/a"), 0},
		{fix("queued", "", "acme/a"), 0},
		{fix("failed", "", "acme/a"), 1},
		{fix("running", "g", "acme/a"), 1},
		{fix("running", "", "acme/b"), 1},
	} {
		if got := Count([]reviewindex.Entry{e}, []jobs.Job{tc.job}, nil, Snoozes{}, now); got != tc.want {
			t.Fatalf("%+v: %d, want %d", tc.job, got, tc.want)
		}
	}
	// A review requested from the user still counts.
	asked := entry(2, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe = true })
	j := fix("running", "", "acme/a")
	j.PR = 2
	if got := Count([]reviewindex.Entry{asked}, []jobs.Job{j}, nil, Snoozes{}, now); got != 1 {
		t.Fatal(got)
	}
}

func TestAStackedPullRequestWaitsToMerge(t *testing.T) {
	ready := func(number int, head, base string) reviewindex.Entry {
		return mine(number, func(e *reviewindex.Entry) {
			e.Merge, e.PullRequest.HeadRef, e.PullRequest.BaseRef = "ready", head, base
		})
	}
	entries := []reviewindex.Entry{ready(1, "one", "main"), ready(2, "two", "one"), ready(3, "three", "two")}
	stack := reviewgroups.Group{Repository: "acme/a", Members: []int{1, 2, 3}, Stack: true}
	// Only the bottom of the stack can merge.
	if got := Count(entries, nil, []reviewgroups.Group{stack}, Snoozes{}, now); got != 1 {
		t.Fatal(got)
	}
	// A group a person made is not a stack.
	made := stack
	made.Stack = false
	if got := Count(entries, nil, []reviewgroups.Group{made}, Snoozes{}, now); got != 3 {
		t.Fatal(got)
	}
	// Once the bottom is gone, the next one up can merge.
	closed := ready(1, "one", "main")
	closed.PullRequest.State = "MERGED"
	if got := Count([]reviewindex.Entry{closed, entries[1], entries[2]}, nil, []reviewgroups.Group{stack}, Snoozes{}, now); got != 1 {
		t.Fatal(got)
	}
	// A stacked pull request with something to fix still counts.
	failing := ready(2, "two", "one")
	failing.Checks = "failing"
	if got := Count([]reviewindex.Entry{entries[0], failing}, nil, []reviewgroups.Group{stack}, Snoozes{}, now); got != 2 {
		t.Fatal(got)
	}
}

func TestSnoozedPullRequestsDoNotCount(t *testing.T) {
	entries := []reviewindex.Entry{
		mine(1, func(e *reviewindex.Entry) { e.Checks = "failing" }),
		entry(2, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe = true }),
	}
	count := func(s Snoozes) int { return Count(entries, nil, nil, s, now) }
	if got := count(Snoozes{Snoozed: map[string]int64{"acme/a#1": now.Add(time.Minute).UnixMilli()}}); got != 1 {
		t.Fatal("snoozed", got)
	}
	if got := count(Snoozes{Snoozed: map[string]int64{"acme/a#1": now.UnixMilli()}}); got != 2 {
		t.Fatal("snooze ended", got)
	}
	if got := count(Snoozes{Held: map[string]string{"acme/a#2": "head"}}); got != 1 {
		t.Fatal("held", got)
	}
	if got := count(Snoozes{Held: map[string]string{"acme/a#2": "older"}}); got != 2 {
		t.Fatal("held on another head", got)
	}
}

func TestParseSnoozes(t *testing.T) {
	for _, raw := range []string{"", "null"} {
		if s, err := ParseSnoozes(json.RawMessage(raw)); err != nil || s.Snoozed != nil || s.Held != nil {
			t.Fatal(raw, s, err)
		}
	}
	s, err := ParseSnoozes(json.RawMessage(`{"day":"2026-11-2","seen":{},"snoozed":{"a/b#1":5},"held":{"a/b#2":"h"}}`))
	if err != nil || s.Snoozed["a/b#1"] != 5 || s.Held["a/b#2"] != "h" {
		t.Fatal(s, err)
	}
	if _, err := ParseSnoozes(json.RawMessage(`[]`)); err == nil {
		t.Fatal("an array is not inbox state")
	}
}

func TestHandlerAnswersTheCountAndPassesTheRest(t *testing.T) {
	var failure error
	service := &Service{
		Index: func(context.Context) (reviewindex.Snapshot, error) {
			return reviewindex.Snapshot{Entries: []reviewindex.Entry{
				entry(1, func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe = true }),
				mine(2, func(e *reviewindex.Entry) { e.Merge = "ready" }),
				mine(3, func(e *reviewindex.Entry) { e.Checks = "failing" }),
			}}, failure
		},
		Jobs:   func() []jobs.Job { return nil },
		Groups: func(context.Context) ([]reviewgroups.Group, error) { return nil, nil },
		Snoozes: func() (json.RawMessage, error) {
			return json.RawMessage(`{"day":"2026-11-2","seen":{},"snoozed":{"acme/a#3":9999999999999},"held":{}}`), nil
		},
		Now: func() time.Time { return now },
	}
	index := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("index")) })
	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		service.Handler(index).ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		return w
	}
	if w := get("/api/index?view=waiting"); strings.TrimSpace(w.Body.String()) != `{"waiting":2}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String())
	}
	for _, target := range []string{"/api/index", "/api/index?history=1"} {
		if got := get(target).Body.String(); got != "index" {
			t.Fatal(target, got)
		}
	}
	failure = errors.New("broken")
	if w := get("/api/index?view=waiting"); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "broken") {
		t.Fatal(w.Code, w.Body.String())
	}
}
