package notices

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

type registry []repos.Clone

func (r registry) List(context.Context) ([]repos.Clone, error) { return r, nil }

func service(entries ...reviewindex.Entry) (*Service, *[]Notice) {
	var got []Notice
	return &Service{
		Registry: registry{{ID: "c1", Repository: "Acme/Widgets", Available: true}},
		Index: func(context.Context) (reviewindex.Snapshot, error) {
			return reviewindex.Snapshot{Entries: entries}, nil
		},
		Publish: func(n Notice) { got = append(got, n) },
	}, &got
}

func entry(title, risk string) reviewindex.Entry {
	return reviewindex.Entry{
		PullRequest: discovery.PullRequest{Repository: "Acme/Widgets", Number: 7, Title: title},
		CloneID:     "c1",
		RiskScore:   json.RawMessage(risk),
	}
}

func job(state, reason string, automatic bool) jobs.Job {
	return jobs.Job{State: state, Phase: "finished", Reason: reason, Automatic: automatic,
		Prepared: jobs.Prepared{Item: jobs.Item{Clone: "c1", PR: 7}, Repository: "Acme/Widgets"}}
}

func TestJobDoneNotices(t *testing.T) {
	five := 5
	for name, tt := range map[string]struct {
		job  jobs.Job
		want Notice
	}{
		"finished":         {job("completed", "", false), Notice{Kind: KindFinished, Risk: &five, RiskStatus: "complete"}},
		"automatic":        {job("completed", "", true), Notice{Kind: KindAutomatic, Risk: &five, RiskStatus: "complete"}},
		"failed":           {job("failed", "timeout", false), Notice{Kind: KindFailed, Reason: "timeout"}},
		"failed automatic": {job("failed", "timeout", true), Notice{Kind: KindFailed, Reason: "timeout"}},
		"free-form reason": {job("failed", "boom: secret detail", false), Notice{Kind: KindFailed, Reason: "internal"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, got := service(entry("Fix <b>the</b> thing", `{"status":"complete","score":5}`))
			s.JobDone(context.Background(), tt.job)
			tt.want.Repository, tt.want.PR, tt.want.Clone, tt.want.Title = "Acme/Widgets", 7, "c1", "Fix <b>the</b> thing"
			if len(*got) != 1 {
				t.Fatalf("%v", *got)
			}
			g := (*got)[0]
			if (g.Risk == nil) != (tt.want.Risk == nil) || (g.Risk != nil && *g.Risk != *tt.want.Risk) {
				t.Fatalf("risk %v want %v", g.Risk, tt.want.Risk)
			}
			g.Risk, tt.want.Risk = nil, nil
			if g != tt.want {
				t.Fatalf("%+v want %+v", g, tt.want)
			}
		})
	}
}

func TestJobDoneIgnoresOtherStatesAndUnscoredReviews(t *testing.T) {
	s, got := service(entry("T", `null`))
	for _, state := range []string{"queued", "running", "cancelled", "interrupted"} {
		s.JobDone(context.Background(), job(state, "interrupted", false))
	}
	running := job("completed", "", false)
	running.Phase = "generating"
	s.JobDone(context.Background(), running)
	if len(*got) != 0 {
		t.Fatalf("%v", *got)
	}
	s.JobDone(context.Background(), job("completed", "", false))
	if len(*got) != 1 || (*got)[0].Risk != nil || (*got)[0].Title != "T" {
		t.Fatalf("%+v", *got)
	}
}

func TestJobDoneWithoutIndexStillNames(t *testing.T) {
	s, got := service()
	s.JobDone(context.Background(), job("completed", "", false))
	if len(*got) != 1 || (*got)[0].PR != 7 || (*got)[0].Title != "" {
		t.Fatalf("%+v", *got)
	}
}

func TestTitleIsBounded(t *testing.T) {
	s, got := service(entry(strings.Repeat("ab ", 400), `null`))
	s.JobDone(context.Background(), job("completed", "", false))
	if n := len([]rune((*got)[0].Title)); n != maxTitle {
		t.Fatalf("title has %d runes", n)
	}
}

func snapshot(prs ...discovery.PullRequest) discovery.Snapshot {
	return discovery.Snapshot{Repository: "Acme/Widgets", PullRequests: prs}
}

func TestObserveNoticesNewRequestsOnly(t *testing.T) {
	s, got := service()
	requested := discovery.PullRequest{Number: 7, Title: "Mine to review", ReviewRequestedFromMe: true, TeamMembershipKnown: true}
	other := discovery.PullRequest{Number: 8, Title: "Not for me", TeamMembershipKnown: true}
	ctx := context.Background()

	s.Observe(ctx, snapshot(requested, other))
	if len(*got) != 0 {
		t.Fatalf("the first snapshot is a baseline: %v", *got)
	}
	s.Observe(ctx, snapshot(requested, other))
	if len(*got) != 0 {
		t.Fatalf("an existing request again: %v", *got)
	}
	other.ReviewRequestedFromMe = true
	own := discovery.PullRequest{Number: 9, ReviewRequestedFromMe: true, AuthoredByMe: true}
	s.Observe(ctx, snapshot(requested, other, own))
	want := Notice{Kind: KindRequested, Repository: "Acme/Widgets", PR: 8, Clone: "c1", Title: "Not for me"}
	if len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("%+v want %+v", *got, want)
	}
	// Removed and requested again is news again.
	s.Observe(ctx, snapshot())
	s.Observe(ctx, snapshot(requested))
	if len(*got) != 2 || (*got)[1].PR != 7 {
		t.Fatalf("%+v", *got)
	}
}

func TestObserveIgnoresASnapshotWithoutTeamMembership(t *testing.T) {
	s, got := service()
	ctx := context.Background()
	viaTeam := discovery.PullRequest{Number: 7, Title: "Requested through a team", ReviewRequestedFromMe: true, TeamMembershipKnown: true}
	unseen := discovery.PullRequest{Number: 8, Title: "Old team request, unknown", TeamMembershipKnown: false}
	s.Observe(ctx, snapshot(viaTeam))
	// GitHub's team list failed: the flag reads false for both, which proves nothing.
	viaTeam.ReviewRequestedFromMe, viaTeam.TeamMembershipKnown = false, false
	s.Observe(ctx, snapshot(viaTeam, unseen))
	// The next complete snapshot shows both as they are: nothing is new.
	viaTeam.ReviewRequestedFromMe, viaTeam.TeamMembershipKnown = true, true
	unseen.ReviewRequestedFromMe, unseen.TeamMembershipKnown = true, true
	s.Observe(ctx, snapshot(viaTeam, unseen))
	if len(*got) != 0 {
		t.Fatalf("a team request the outage hid was announced: %+v", *got)
	}
}

func TestClipStripsFormatAndControlCharacters(t *testing.T) {
	if got := clip("safe\u202egnp.exe\u200b\x1b[0m\n x"); got != "safegnp.exe [0m x" {
		t.Fatalf("%q", got)
	}
}
