package notices

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/jobs"
)

func own(number int, review, checks, merge string) discovery.PullRequest {
	return discovery.PullRequest{Number: number, Title: "Mine", AuthoredByMe: true, TeamMembershipKnown: true, Review: review, Checks: checks, Merge: merge}
}

func TestObserveOwnPullRequestTransitions(t *testing.T) {
	healthy := own(12, "approved", "running", "blocked")
	for name, tt := range map[string]struct {
		after discovery.PullRequest
		want  string
	}{
		"checks failed":     {own(12, "approved", "failing", "blocked"), KindChecksFailed},
		"conflict":          {own(12, "approved", "running", "conflict"), KindConflict},
		"changes requested": {own(12, "changes_requested", "running", "blocked"), KindChangesRequested},
		"ready to merge":    {own(12, "approved", "passing", "ready"), KindReadyToMerge},
		"unchanged":         {healthy, ""},
		"checks recovered":  {own(12, "approved", "passing", "blocked"), ""},
		"unknown states":    {own(12, "", "", ""), ""},
	} {
		t.Run(name, func(t *testing.T) {
			s, got := service()
			ctx := context.Background()
			s.Observe(ctx, snapshot(healthy))
			if len(*got) != 0 {
				t.Fatalf("the first snapshot is a baseline: %v", *got)
			}
			s.Observe(ctx, snapshot(tt.after))
			if tt.want == "" {
				if len(*got) != 0 {
					t.Fatalf("%v", *got)
				}
				return
			}
			want := Notice{Kind: tt.want, Repository: "Acme/Widgets", PR: 12, Clone: "c1", Title: "Mine"}
			if len(*got) != 1 || (*got)[0] != want {
				t.Fatalf("%+v want %+v", *got, want)
			}
			// The same state in the next snapshot is not news again.
			s.Observe(ctx, snapshot(tt.after))
			if len(*got) != 1 {
				t.Fatalf("%v", *got)
			}
		})
	}
}

func TestObserveOwnPullRequestsOnly(t *testing.T) {
	s, got := service()
	ctx := context.Background()
	theirs := discovery.PullRequest{Number: 3, Checks: "running", TeamMembershipKnown: true}
	s.Observe(ctx, snapshot(theirs))
	theirs.Checks = "failing"
	s.Observe(ctx, snapshot(theirs))
	if len(*got) != 0 {
		t.Fatalf("someone else's pull request: %v", *got)
	}
}

func TestObserveOwnPullRequestFirstSeenClosedAndUnknownStates(t *testing.T) {
	s, got := service()
	ctx := context.Background()
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked")))
	// A pull request first seen in a later snapshot is a baseline, even failing.
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked"), own(2, "approved", "failing", "blocked")))
	if len(*got) != 0 {
		t.Fatalf("%v", *got)
	}
	// Closed: it leaves the snapshot silently, and comes back as a baseline.
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked")))
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked"), own(2, "approved", "failing", "blocked")))
	if len(*got) != 0 {
		t.Fatalf("%v", *got)
	}
	// A state GitHub did not report keeps the one before it: failing, unknown,
	// failing is not a new failure.
	s.Observe(ctx, snapshot(own(2, "approved", "", "blocked")))
	s.Observe(ctx, snapshot(own(2, "approved", "failing", "blocked")))
	if len(*got) != 0 {
		t.Fatalf("%v", *got)
	}
}

func TestObserveOwnPullRequestSeveralChangesGiveOneNotice(t *testing.T) {
	s, got := service()
	ctx := context.Background()
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked")))
	s.Observe(ctx, snapshot(own(1, "changes_requested", "failing", "conflict")))
	if len(*got) != 1 || (*got)[0].Kind != KindChangesRequested {
		t.Fatalf("%v", *got)
	}
}

func TestResetIdentityForgetsOwnPullRequests(t *testing.T) {
	s, got := service()
	ctx := context.Background()
	s.Observe(ctx, snapshot(own(1, "approved", "running", "blocked")))
	s.ResetIdentity()
	// After the reset the next snapshot is a baseline again.
	s.Observe(ctx, snapshot(own(1, "approved", "failing", "blocked")))
	if len(*got) != 0 {
		t.Fatalf("%v", *got)
	}
	s.Observe(ctx, snapshot(own(1, "approved", "failing", "conflict")))
	if len(*got) != 1 || (*got)[0].Kind != KindConflict {
		t.Fatalf("%v", *got)
	}
}

func fixJob(state, reason, commit string) jobs.Job {
	j := job(state, reason, false)
	j.Kind, j.Commit = jobs.KindFix, commit
	return j
}

func TestFixNotices(t *testing.T) {
	for name, tt := range map[string]struct {
		job  jobs.Job
		want *Notice
	}{
		"pushed":     {fixJob("completed", "", "0123456789abcdef0123456789abcdef01234567"), &Notice{Kind: KindFixPushed, Commit: "0123456"}},
		"failed":     {fixJob("failed", "conflicts_remain", ""), &Notice{Kind: KindFixFailed, Reason: "conflicts_remain"}},
		"free-form":  {fixJob("failed", "oops: free-form detail", ""), &Notice{Kind: KindFixFailed, Reason: "internal"}},
		"no commit":  {fixJob("completed", "", ""), nil},
		"not a hash": {fixJob("completed", "", "<script>"), nil},
		"cancelled":  {fixJob("cancelled", "", ""), nil},
	} {
		t.Run(name, func(t *testing.T) {
			s, got := service(entry("Fix it", `{"status":"complete","score":5}`))
			s.JobDone(context.Background(), tt.job)
			if tt.want == nil {
				if len(*got) != 0 {
					t.Fatalf("%v", *got)
				}
				return
			}
			tt.want.Repository, tt.want.PR, tt.want.Clone, tt.want.Title = "Acme/Widgets", 7, "c1", "Fix it"
			if len(*got) != 1 || (*got)[0] != *tt.want {
				t.Fatalf("%+v want %+v", *got, *tt.want)
			}
		})
	}
}
