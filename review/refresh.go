package review

import (
	"cmp"
	"slices"
	"strings"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

// Refresh changes evidence and derived summaries in place, preserving the
// reviewed revision, authored prose, section order and generation provenance.
func (v *DocumentValidator) Refresh(document []byte, b *bundlepkg.Bundle, now time.Time) ([]byte, error) {
	doc, err := v.decodeFullDocument(document)
	if err != nil {
		return nil, err
	}
	review, index := &doc.Review, &doc.Index
	// The bundle's checks were loaded for the reviewed head, and that is the
	// head they are indexed under: after a push the pull request's own head
	// has moved on, and stamping them with it would leave every reader of the
	// index looking for checks on a revision none of them ran on.
	checks, checkCounts := checkIndex(b, review.Head)
	sessions, vendors, models := sessionIndex(b)
	carryPrompts(sessions, index.Sessions)
	index.Checks, index.Sessions, index.Threads = checks, sessions, threadIndex(b)
	refreshConversation(index, b.Conversation)
	summary := &doc.Summary

	evidence := newRefreshEvidence(index, b, review.Head)
	for i := range doc.Sections {
		refreshSection(&doc.Sections[i], evidence)
	}
	authorship := &summary.Authorship
	authorship.ObservedSessions, authorship.ObservedVendors, authorship.ObservedModels = len(b.Sessions), sortedKeys(vendors), sortedKeys(models)
	doc.Stats.Checks = checkCounts
	doc.AgentReviews = agentReviews(b, doc.Sections, review.Head)
	review.Sources.ReviewThreads, review.Sources.AgentReviews = b.Sources["review_threads"], len(doc.AgentReviews)
	review.Generator.Truncated = threadNotes(review.Generator.Truncated, b)
	countFindings(doc)
	setReviewExchange(doc, b)
	refreshReviewMetadata(review, b, now)
	return v.encodeFullDocument(doc)
}

func refreshConversation(index *documentIndex, conversation []bundlepkg.ConversationItem) {
	// Human comment content is frozen until regeneration. Bot reports refresh
	// from synchronized rows and disappear when their source is deleted.
	reports := []documentReport{}
	for _, c := range conversation {
		if c.BotReport {
			reports = append(reports, documentReport{ID: c.ID, Tool: c.BotTool, Marker: "baum", Comment: jsonString(c.ID), Summary: c.Body})
		}
	}
	index.Reports = reports
	comments := []documentComment{}
	for _, comment := range index.Comments {
		if !comment.Bot {
			comments = append(comments, comment)
		}
	}
	for _, c := range conversation {
		if !c.Bot || c.CreatedAt == nil {
			continue
		}
		kind := c.Kind
		if kind == "comment" {
			kind = "issue"
		}
		comments = append(comments, documentComment{ID: jsonString(c.ID), Kind: kind, Author: c.AuthorLogin, Bot: true, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	index.Comments = comments
}

func refreshReviewMetadata(review *documentReview, b *bundlepkg.Bundle, now time.Time) {
	sources := &review.Sources
	sources.Checks, sources.Sessions = len(b.Checks), len(b.Sessions)
	state := b.PullRequest.State
	if b.PullRequest.Draft {
		state = "draft"
	}
	review.PRState = state
	review.MergedCommit = nil
	if b.PullRequest.MergedSHA != "" {
		review.MergedCommit = &b.PullRequest.MergedSHA
		review.PRState = "merged"
	}
	review.EvidenceRefreshedAt = now.UTC().Format(time.RFC3339Nano)
}

// Index the validated document once for all evidence-anchor lookups.
type refreshEvidence struct {
	bundle      *bundlepkg.Bundle
	currentHead bool
	checks      map[string]string
	reports     map[string]bool
	testHunks   map[string]bool
	// prompts are the refreshed index's prompts, carried ones included, by
	// session ID and turn.
	prompts map[string]map[int]bool
}

func newRefreshEvidence(index *documentIndex, b *bundlepkg.Bundle, reviewedHead string) refreshEvidence {
	evidence := refreshEvidence{
		bundle:      b,
		currentHead: b.PullRequest.HeadSHA == reviewedHead,
		checks:      map[string]string{},
		reports:     map[string]bool{},
		testHunks:   map[string]bool{},
		prompts:     map[string]map[int]bool{},
	}
	for _, session := range index.Sessions {
		if session.Restricted {
			continue
		}
		for _, prompt := range session.Prompts {
			if evidence.prompts[session.ID] == nil {
				evidence.prompts[session.ID] = map[int]bool{}
			}
			evidence.prompts[session.ID][prompt.Index] = true
		}
	}
	for _, check := range index.Checks {
		evidence.checks[check.ID] = check.Conclusion
	}
	for _, report := range index.Reports {
		evidence.reports[report.ID] = true
	}
	for _, hunk := range index.Hunks {
		evidence.testHunks[hunk.ID] = hunk.Role == "test"
	}
	return evidence
}

func refreshSection(section *documentSection, sources refreshEvidence) {
	derived := &section.Derived
	counts := map[string]int{}
	for i := range section.Evidence {
		evidence := &section.Evidence[i]
		if status := sources.status(evidence.Kind, evidence.Anchors); status != "" {
			evidence.Status = status
			if status == "verified" && evidence.Granularity == nil {
				evidence.Granularity = new("job")
			}
		}
		if evidence.Status != "verified" {
			evidence.Granularity = nil
		}
		if derived.State != "stale" {
			counts[evidence.Status]++
		}
	}
	derived.EvidenceSummary = counts
	derived.Reviewers = requestedReviewers(sources.bundle.PullRequest.RequestedReviewers)
}

// An empty status preserves the authored assessment. Missing sources override
// check results; otherwise failures, pending and skipped checks take precedence.
func (e refreshEvidence) status(kind string, anchors []documentAnchor) string {
	failed, pending, skipped, success, test := false, false, false, false, false
	for _, anchor := range anchors {
		switch anchor.Type {
		case "check":
			conclusion, exists := e.checks[anchor.identifier()]
			if !exists || !e.currentHead {
				return "asserted"
			}
			switch conclusion {
			case "success":
				success = true
			case "pending":
				pending = true
			case "skipped", "neutral":
				skipped = true
			default:
				failed = true
			}
		case "prompt", "session_event":
			if !resolveDocumentAnchor(anchor, e.bundle, "") && !e.indexedPrompt(anchor) {
				return "asserted"
			}
		case "bot_report":
			if !e.reports[anchor.identifier()] {
				return "asserted"
			}
			test = true
		case "hunk":
			test = test || e.testHunks[anchor.identifier()]
		}
	}
	switch {
	case failed:
		return "failed"
	case pending:
		return "pending"
	case skipped:
		return "skipped"
	case success && (kind != "test" || test):
		return "verified"
	case success:
		return "asserted"
	default:
		return ""
	}
}

// indexedPrompt reports whether a prompt anchor names a prompt the refreshed
// index lists, one carried from the stored index included.
func (e refreshEvidence) indexedPrompt(anchor documentAnchor) bool {
	return anchor.Type == "prompt" && anchor.Session != nil && anchor.Index != nil && e.prompts[*anchor.Session][*anchor.Index]
}

// carryPrompts adds to each readable refreshed session the prompt entries
// the stored index lists for the same session at turns the refresh bundle
// lacks, copied as stored, so the prompts a version was generated with, the
// ones its extracted decisions cited included, survive a refresh whose
// bundle has no decisions. Their text was bounded and redacted when the
// version was generated, and is never read again: a refresh cannot lengthen
// it. A turn both list keeps the refresh bundle's entry, except that a
// stored text the refreshed one starts with, which generation's byte
// trimming shortened, stays shortened. A restricted session, or one the
// refresh bundle no longer links, carries nothing. Prompts stay in turn
// order and the session's prompt count includes the carried ones.
func carryPrompts(refreshed, stored []documentSession) {
	storedPrompts := map[string][]documentPrompt{}
	for _, session := range stored {
		if _, seen := storedPrompts[session.ID]; !seen && !session.Restricted {
			storedPrompts[session.ID] = session.Prompts
		}
	}
	for i := range refreshed {
		session := &refreshed[i]
		previous := storedPrompts[session.ID]
		if session.Restricted || len(previous) == 0 {
			continue
		}
		listed := make(map[int]int, len(session.Prompts))
		for p, prompt := range session.Prompts {
			listed[prompt.Index] = p
		}
		for _, prompt := range previous {
			p, ok := listed[prompt.Index]
			if !ok {
				listed[prompt.Index] = len(session.Prompts)
				session.Prompts = append(session.Prompts, prompt)
				continue
			}
			current := &session.Prompts[p]
			if current.ID == prompt.ID && len(prompt.Text) < len(current.Text) && strings.HasPrefix(current.Text, prompt.Text) {
				current.Text = prompt.Text
			}
		}
		slices.SortStableFunc(session.Prompts, func(a, b documentPrompt) int { return cmp.Compare(a.Index, b.Index) })
		session.PromptCount = new(len(session.Prompts))
	}
}
