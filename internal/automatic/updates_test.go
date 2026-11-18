package automatic

import (
	"cmp"
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

// On open, a pull request is reviewed automatically once: a new pull request
// and one without a saved review are reviewed, but a new head of one that
// has a saved review is not. Always and on change queue the new head (on
// change decides in the job whether to write a review).
func TestUpdatesOnOpenReviewsAPullRequestOnce(t *testing.T) {
	all := []string{"org/repo#1@a2:new_head", "org/repo#2@b1:new_head", "org/repo#3@c2:new_head"}
	for _, tc := range []struct {
		updates string
		want    []string
	}{
		{"", all},
		{repos.UpdatesOnChange, all},
		{repos.UpdatesOnOpen, []string{"org/repo#2@b1:new_head", "org/repo#3@c2:new_head"}},
	} {
		t.Run(cmp.Or(tc.updates, "always"), func(t *testing.T) {
			h := newHarness(t)
			_, _ = h.registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
				p.Updates = tc.updates
				return p
			})
			h.observe(pr{number: 1, head: "a1", requested: true}, pr{number: 3, head: "c1", requested: true})
			// #1 was reviewed at its earlier head; #2 is new; #3 has no review.
			h.reviewed[1] = "a1"
			h.observe(pr{number: 1, head: "a2", requested: true}, pr{number: 2, head: "b1", authored: true}, pr{number: 3, head: "c2", requested: true})
			h.pass()
			h.want(tc.want...)
			// A skipped head is used up: later passes do not retry it.
			h.waitIdle()
			h.pass()
			h.want(tc.want...)
		})
	}
}

func TestHandlerSavesUpdates(t *testing.T) {
	h := newHarness(t)
	o, _ := h.scheduler.Overview(context.Background())
	if o.Updates != repos.UpdatesAlways {
		t.Fatalf("default updates %q", o.Updates)
	}
	for _, tc := range []struct {
		body, want string
		code       int
	}{
		{`{"updates":"on_change"}`, repos.UpdatesOnChange, 200},
		{`{"updates":"on_open"}`, repos.UpdatesOnOpen, 200},
		{`{"updates":"sometimes"}`, repos.UpdatesOnOpen, 400},
		{`{"updates":"always"}`, repos.UpdatesAlways, 200},
	} {
		if code := put(h.scheduler, tc.body); code != tc.code {
			t.Fatalf("%s: %d", tc.body, code)
		}
		o, _ = h.scheduler.Overview(context.Background())
		if o.Updates != tc.want {
			t.Fatalf("%s: updates %q, want %q", tc.body, o.Updates, tc.want)
		}
	}
	prefs, _ := h.registry.Preferences(context.Background())
	if prefs.Updates != "" {
		t.Fatalf("always is stored as empty, got %q", prefs.Updates)
	}
}
