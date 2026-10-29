package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

func TestAfterPushPollsUntilThePushedHeadsChecksAppear(t *testing.T) {
	repository := github.Repository{Host: "github.com", Owner: "acme", Name: "demo"}
	pr := func(head, checks string) Snapshot {
		return Snapshot{PullRequests: []PullRequest{{Number: 1, HeadSHA: head, Checks: checks, Merge: "ready"}, {Number: 2, HeadSHA: "other", Checks: "none"}}}
	}
	delays := []time.Duration{0, 0, 0, 0, 0, 0}
	for _, test := range []struct {
		name      string
		snapshots []Snapshot
		errs      []error
		polls     int
		changed   int
	}{
		// GitHub shows the old head first, then the new one before its
		// checks are queued, which reads as ready to merge.
		{name: "checks appear", snapshots: []Snapshot{pr("old", "passing"), pr("new", "none"), pr("new", "running")}, polls: 3, changed: 3},
		{name: "checks finish before a poll sees them run", snapshots: []Snapshot{pr("new", "failing")}, polls: 1, changed: 1},
		{name: "merged or closed", snapshots: []Snapshot{{}}, polls: 1, changed: 1},
		{name: "a failed poll is tried again", snapshots: []Snapshot{{}, pr("new", "running")}, errs: []error{errors.New("rate limited"), nil}, polls: 2, changed: 1},
		{name: "no checks stops with the delays", snapshots: []Snapshot{pr("new", "none")}, polls: len(delays), changed: len(delays)},
	} {
		t.Run(test.name, func(t *testing.T) {
			polls, changed := 0, 0
			poll := func(_ context.Context, got github.Repository) (Snapshot, error) {
				if got != repository {
					t.Fatalf("polled %v", got)
				}
				i := min(polls, len(test.snapshots)-1)
				polls++
				if i < len(test.errs) && test.errs[i] != nil {
					return Snapshot{}, test.errs[i]
				}
				return test.snapshots[i], nil
			}
			AfterPush(context.Background(), poll, repository, 1, "new", func() { changed++ }, delays)
			if polls != test.polls || changed != test.changed {
				t.Fatalf("polls, changed = %d, %d, want %d, %d", polls, changed, test.polls, test.changed)
			}
		})
	}
}

func TestAfterPushStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	polled := false
	AfterPush(ctx, func(context.Context, github.Repository) (Snapshot, error) {
		polled = true
		return Snapshot{}, nil
	}, github.Repository{}, 1, "new", func() {}, []time.Duration{time.Hour})
	if polled {
		t.Fatal("polled after the context ended")
	}
}
