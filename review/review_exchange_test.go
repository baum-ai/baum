package review

import (
	"slices"
	"testing"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func exchangeAt(minutes int) *time.Time {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
	return &at
}

func exchangeBundle() *bundlepkg.Bundle {
	return &bundlepkg.Bundle{
		PullRequest: bundlepkg.PullRequest{
			AuthorLogin: "author",
			OpenedAt:    exchangeAt(10),
			Commits: []bundlepkg.Commit{
				{SHA: "c1", CommittedAt: exchangeAt(0)},
				{SHA: "c2", CommittedAt: exchangeAt(30), Additions: new(12), Deletions: new(3)},
				{SHA: "c3", CommittedAt: exchangeAt(50), Additions: new(4), Deletions: new(0)},
			},
		},
		Conversation: []bundlepkg.ConversationItem{
			{Kind: "review", AuthorLogin: "bot", CreatedAt: exchangeAt(20)},
			{Kind: "review", AuthorLogin: "human", CreatedAt: exchangeAt(25)},
			{Kind: "review", AuthorLogin: "author", CreatedAt: exchangeAt(40)},
			{Kind: "review", AuthorLogin: "human", CreatedAt: exchangeAt(45)},
			{Kind: "review", AuthorLogin: "human", CreatedAt: exchangeAt(60)},
			{Kind: "comment", AuthorLogin: "human", CreatedAt: exchangeAt(35)},
		},
	}
}

func TestSetReviewExchangeDerivesEveryFigure(t *testing.T) {
	doc := &Document{}
	doc.Review.Generator.Truncated = []string{legacyExchangeNote, "kept"}
	doc.AgentReviews = []documentAgentReview{
		{Counts: documentAgentReviewCounts{MustFix: 2, ShouldFix: 1}},
		{Counts: documentAgentReviewCounts{MustFix: 1, Consider: 4}},
	}
	setReviewExchange(doc, exchangeBundle())

	exchange := doc.Summary.ReviewExchange
	if exchange.Reviews != 5 {
		t.Errorf("reviews = %d, want 5", exchange.Reviews)
	}
	// Reviews at 20 and 25 are answered by c2, the one at 45 by c3 (the
	// author's own at 40 is not a round); the one at 60 is unanswered.
	if exchange.BackAndForths == nil || *exchange.BackAndForths != 2 {
		t.Errorf("back_and_forths = %v, want 2", exchange.BackAndForths)
	}
	if exchange.CriticalIssuesFound == nil || *exchange.CriticalIssuesFound != 3 {
		t.Errorf("critical_issues_found = %v, want 3", exchange.CriticalIssuesFound)
	}
	want := ChangesAfterOpen{Commits: 2, LinesAdded: 16, LinesRemoved: 3}
	if exchange.ChangesAfterOpen == nil || *exchange.ChangesAfterOpen != want {
		t.Errorf("changes_after_open = %+v, want %+v", exchange.ChangesAfterOpen, want)
	}
	if got := doc.Review.Generator.Truncated; !slices.Equal(got, []string{"kept"}) {
		t.Errorf("notes = %q, want only the unrelated note", got)
	}
}

func TestSetReviewExchangeLeavesUnknownCommitFiguresNull(t *testing.T) {
	b := exchangeBundle()
	b.PullRequest.Commits[2].Additions = nil
	doc := &Document{}
	setReviewExchange(doc, b)
	if doc.Summary.ReviewExchange.ChangesAfterOpen != nil {
		t.Errorf("changes_after_open = %+v, want null for an uncompared commit", doc.Summary.ReviewExchange.ChangesAfterOpen)
	}
	if got := doc.Summary.ReviewExchange.BackAndForths; got == nil || *got != 2 {
		t.Errorf("back_and_forths = %v, want 2: line counts do not bear on it", got)
	}
	if !slices.Equal(doc.Review.Generator.Truncated, []string{exchangeUnknownNote}) {
		t.Errorf("notes = %q, want the unknown note once", doc.Review.Generator.Truncated)
	}
	// A refresh that still lacks the counts states the note once.
	setReviewExchange(doc, b)
	if len(doc.Review.Generator.Truncated) != 1 {
		t.Errorf("notes after a second pass = %q", doc.Review.Generator.Truncated)
	}

	b.PullRequest.Commits[0].CommittedAt = nil
	setReviewExchange(doc, b)
	if doc.Summary.ReviewExchange.BackAndForths != nil {
		t.Errorf("back_and_forths = %v, want null without every commit time", *doc.Summary.ReviewExchange.BackAndForths)
	}

	b.Conversation = nil
	setReviewExchange(doc, b)
	if got := doc.Summary.ReviewExchange.BackAndForths; got == nil || *got != 0 {
		t.Errorf("back_and_forths = %v, want 0 without reviews", got)
	}
}
