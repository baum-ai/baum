package review

import (
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// FactsSchema names a pull request's facts. They are not a review document:
// no version, no sections, nothing a model wrote, and never stored as a
// version.
const FactsSchema = "baum.facts/1"

// Facts are what a pull request shows before any review exists: the parts of
// a review document the host derives from the bundle alone (its change size,
// checks, sessions, conversation, threads, hunks and the agent reviews posted
// on it), filled by the same code that fills them when a document is
// completed or refreshed.
type Facts struct {
	Schema  string       `json:"schema"`
	Review  factsReview  `json:"review"`
	Summary factsSummary `json:"summary"`
	Stats   factsStats   `json:"stats"`
	// AgentReviews are the review tools' assessments; their findings name
	// no section, as there are none.
	AgentReviews []documentAgentReview `json:"agent_reviews"`
	// UnassignedHunks is every hunk, as no section covers any.
	UnassignedHunks []string      `json:"unassigned_hunks"`
	Index           documentIndex `json:"index"`
}

type factsReview struct {
	Repository   string  `json:"repository"`
	PullRequest  int     `json:"pull_request"`
	PRState      string  `json:"pr_state"`
	Head         string  `json:"head"`
	Base         string  `json:"base"`
	MergeBase    string  `json:"merge_base"`
	MergedCommit *string `json:"merged_commit"`
	// EvidenceRefreshedAt is when the facts were read.
	EvidenceRefreshedAt string                `json:"evidence_refreshed_at"`
	Sources             documentReviewSources `json:"sources"`
	// Truncated are the host's notes on what the bundle left out, as a
	// document's generator records them.
	Truncated []string `json:"truncated"`
}

type factsSummary struct {
	Title          string              `json:"title"`
	Author         documentAuthor      `json:"author"`
	Authorship     factsAuthorship     `json:"authorship"`
	ReviewExchange factsReviewExchange `json:"review_exchange"`
}

type factsAuthorship struct {
	ObservedSessions int      `json:"observed_sessions"`
	ObservedVendors  []string `json:"observed_vendors"`
	ObservedModels   []string `json:"observed_models,omitzero"`
}

// factsReviewExchange is a review exchange's derived figures, without the
// summary a model writes.
type factsReviewExchange struct {
	Reviews             int                       `json:"reviews"`
	BackAndForths       *int                      `json:"back_and_forths"`
	CriticalIssuesFound *int                      `json:"critical_issues_found"`
	ChangesAfterOpen    *documentChangesAfterOpen `json:"changes_after_open"`
}

// factsStats are a document's stats less those that need sections: no
// section count and no lines by risk tier.
type factsStats struct {
	Validation    *documentValidationStats   `json:"validation,omitempty"`
	Files         int                        `json:"files"`
	Hunks         int                        `json:"hunks"`
	Additions     int                        `json:"additions"`
	Deletions     int                        `json:"deletions"`
	RoleLOC       map[string]int             `json:"role_loc"`
	Checks        documentStatsChecks        `json:"checks"`
	AgentFindings documentStatsAgentFindings `json:"agent_findings"`
}

// BuildFacts derives a pull request's facts from a bundle, as of now. It
// never calls a model; the bundle is the assembled, redacted one
// (assemble.Request.Facts builds it without a checkout).
func BuildFacts(b *bundlepkg.Bundle, now time.Time) Facts {
	index, checkCounts, vendors, models := bundleIndex(b)
	files, additions, deletions := diffTotals(b)
	roleLOC := map[string]int{}
	unassigned := []string{}
	for _, h := range b.Diff {
		roleLOC[h.Role] += h.Additions + h.Deletions
		unassigned = append(unassigned, h.ID)
	}
	// The derivations below fill a document; one with no sections holds
	// exactly what they derive from the bundle.
	doc := reviewDocument{Index: index}
	doc.Review.Head = b.PullRequest.HeadSHA
	doc.Review.Sources = bundleSources(b)
	doc.Review.Generator.Truncated = threadNotes(append([]string{}, b.Truncated...), b)
	doc.AgentReviews = agentReviews(b, nil, doc.Review.Head)
	setValidation(&doc, b)
	doc.Review.Sources.AgentReviews = len(doc.AgentReviews)
	countFindings(&doc)
	setReviewExchange(&doc, b)

	state, merged := bundlePRState(b)
	exchange := doc.Summary.ReviewExchange
	return Facts{
		Schema: FactsSchema,
		Review: factsReview{
			Repository: b.Repository, PullRequest: b.PullRequest.Number, PRState: state,
			Head: b.PullRequest.HeadSHA, Base: b.PullRequest.BaseSHA, MergeBase: b.PullRequest.MergeBaseSHA, MergedCommit: merged,
			EvidenceRefreshedAt: now.UTC().Format(time.RFC3339Nano), Sources: doc.Review.Sources, Truncated: doc.Review.Generator.Truncated,
		},
		Summary: factsSummary{
			Title:  b.PullRequest.Title,
			Author: bundleAuthor(b),
			Authorship: factsAuthorship{
				ObservedSessions: len(b.Sessions), ObservedVendors: sortedKeys(vendors), ObservedModels: sortedKeys(models),
			},
			ReviewExchange: factsReviewExchange{
				Reviews: exchange.Reviews, BackAndForths: exchange.BackAndForths,
				CriticalIssuesFound: exchange.CriticalIssuesFound, ChangesAfterOpen: exchange.ChangesAfterOpen,
			},
		},
		Stats: factsStats{
			Validation: doc.Stats.Validation, Files: files, Hunks: len(b.Diff), Additions: additions, Deletions: deletions,
			RoleLOC: roleLOC, Checks: checkCounts, AgentFindings: doc.Stats.AgentFindings,
		},
		AgentReviews:    doc.AgentReviews,
		UnassignedHunks: unassigned,
		Index:           doc.Index,
	}
}
