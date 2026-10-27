package reviewindex

import (
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
)

func TestWaitingCountsInboxReviewRequestedAndNeedsChanges(t *testing.T) {
	pr := func(number int, mutate func(*discovery.PullRequest)) Entry {
		p := discovery.PullRequest{Repository: "acme/a", Number: number, State: "OPEN"}
		mutate(&p)
		return Entry{PullRequest: p, Review: p.Review, Checks: p.Checks, Merge: p.Merge}
	}
	historical := pr(9, func(p *discovery.PullRequest) { p.ReviewRequestedFromMe = true })
	historical.Historical = true
	entries := []Entry{
		pr(1, func(p *discovery.PullRequest) { p.ReviewRequestedFromMe = true }),
		// The same pull request in a second clone counts once.
		pr(1, func(p *discovery.PullRequest) { p.ReviewRequestedFromMe = true }),
		pr(2, func(p *discovery.PullRequest) { p.AuthoredByMe, p.Review = true, "changes_requested" }),
		pr(3, func(p *discovery.PullRequest) { p.AuthoredByMe, p.Checks = true, "failing" }),
		pr(4, func(p *discovery.PullRequest) { p.AuthoredByMe, p.Merge = true, "conflict" }),
		// Ready, running, and others' pull requests do not wait on the user.
		pr(5, func(p *discovery.PullRequest) { p.AuthoredByMe, p.Merge = true, "ready" }),
		pr(6, func(p *discovery.PullRequest) { p.AuthoredByMe, p.Checks = true, "running" }),
		pr(7, func(p *discovery.PullRequest) { p.Checks = "failing" }),
		pr(8, func(p *discovery.PullRequest) { p.ReviewRequestedFromMe, p.State = true, "CLOSED" }),
		historical,
	}
	if got := waiting(entries); got != 4 {
		t.Fatalf("waiting = %d, want 4", got)
	}
	if waiting(nil) != 0 {
		t.Fatal("nothing waits on the user")
	}
}
