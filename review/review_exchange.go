package review

import (
	"slices"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

const (
	// legacyExchangeNote is what versions stated before the host derived
	// these counts; a refresh drops it along with the nulls it explained.
	legacyExchangeNote  = "Review-exchange counts cover exported reviews; back-and-forth cycles, critical findings and changes after opening are unavailable in this input."
	exchangeUnknownNote = "Commit times or line counts are unavailable in this input, so the review exchange leaves back-and-forths or changes after opening unknown."
)

// setReviewExchange derives the review exchange's figures from the bundle and
// the document's agent reviews; the model writes only its summary. It runs
// after agentReviews, whose must-fix findings are the critical issues.
func setReviewExchange(doc *reviewDocument, b *bundlepkg.Bundle) {
	exchange := &doc.Summary.ReviewExchange
	exchange.Reviews = 0
	for _, c := range b.Conversation {
		if c.Kind == "review" {
			exchange.Reviews++
		}
	}
	critical := 0
	for _, review := range doc.AgentReviews {
		critical += review.Counts.MustFix
	}
	exchange.CriticalIssuesFound = &critical
	exchange.BackAndForths = backAndForths(b)
	exchange.ChangesAfterOpen = changesAfterOpen(b.PullRequest)

	notes := slices.DeleteFunc(slices.Clone(doc.Review.Generator.Truncated), func(note string) bool {
		return note == legacyExchangeNote || note == exchangeUnknownNote
	})
	if exchange.BackAndForths == nil || exchange.ChangesAfterOpen == nil {
		notes = append(notes, exchangeUnknownNote)
	}
	doc.Review.Generator.Truncated = notes
}

// backAndForths counts the rounds in which someone other than the author
// reviewed and the author then committed: reviews with no commit between
// them are one round, and a round the author has not yet answered is not
// counted.
func backAndForths(b *bundlepkg.Bundle) *int {
	type event struct {
		at     time.Time
		review bool
	}
	var events []event
	for _, c := range b.Conversation {
		if c.Kind == "review" && !c.BotReport && c.CreatedAt != nil && c.AuthorLogin != b.PullRequest.AuthorLogin {
			events = append(events, event{at: *c.CreatedAt, review: true})
		}
	}
	rounds := 0
	if len(events) == 0 {
		return &rounds
	}
	for _, c := range b.PullRequest.Commits {
		if c.CommittedAt == nil {
			return nil
		}
		events = append(events, event{at: *c.CommittedAt})
	}
	// A commit at the same instant as a review is read as answering it.
	slices.SortStableFunc(events, func(x, y event) int { return x.at.Compare(y.at) })
	pending := false
	for _, e := range events {
		switch {
		case e.review:
			pending = true
		case pending:
			rounds++
			pending = false
		}
	}
	return &rounds
}

// changesAfterOpen sums the commits made after the pull request was opened.
// It is unknown when the opening time, a commit's time or an after-open
// commit's line counts are missing.
func changesAfterOpen(pr bundlepkg.PullRequest) *documentChangesAfterOpen {
	if pr.OpenedAt == nil {
		return nil
	}
	changes := &documentChangesAfterOpen{}
	for _, c := range pr.Commits {
		if c.CommittedAt == nil {
			return nil
		}
		if !c.CommittedAt.After(*pr.OpenedAt) {
			continue
		}
		if c.Additions == nil || c.Deletions == nil {
			return nil
		}
		changes.Commits++
		changes.LinesAdded += *c.Additions
		changes.LinesRemoved += *c.Deletions
	}
	return changes
}
