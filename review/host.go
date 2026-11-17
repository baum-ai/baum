package review

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
	reviewschema "github.com/baum-ai/baum/schema"
)

// HostSection is what another document shape (baum.review/2) gives
// HostParts of one of its sections: the outline, the level and its reasons
// in place of an assessment, the details state and the risks with their
// lifecycle state already reconciled across versions.
type HostSection struct {
	ID    string
	Kind  string
	Hunks []string
	// Level is the section's assessed level (1-5), nil for none.
	Level *int
	// Reasons are the level's reasons, strongest first.
	Reasons []string
	// Details is "", "pending" or "failed".
	Details string
	Risks   []HostRisk
}

// HostRisk is one risk of a HostSection. Anchors are baum.review/1 anchors.
type HostRisk struct {
	ID      string
	Tier    string
	Text    string
	State   string
	Anchors []json.RawMessage
}

// HostInput is what HostParts reads besides the bundle and the sections.
type HostInput struct {
	// Version is the version being written; PreviousVersion the one it
	// follows, 0 for none. PreviousHunks are the previous version's
	// sections' hunks by section ID, for each section's state.
	Version         int
	PreviousVersion int
	PreviousHunks   map[string][]string
	// Generator is the generator's model and prompt version; SchemaRevision
	// is the revision of the schema the document is written against.
	Model, PromptVersion, SchemaRevision string
	Now                                  time.Time
}

// Host is the host-owned parts of a review document, encoded as
// baum.review/1 writes them: the review header, the index, the stats, the
// risk score (rubric risk-v1) and each section's derived facts by ID.
type Host struct {
	Review    json.RawMessage
	Index     json.RawMessage
	Stats     json.RawMessage
	RiskScore json.RawMessage
	Derived   map[string]json.RawMessage
}

// HostParts derives a document's host-owned parts from the bundle and the
// document's sections exactly as baum.review/1's completion does: the header
// (repository, pull request, heads, version, generator, sources), the index
// of hunks, checks, comments, threads, sessions and attachments, the stats
// (line counts by role and by risk tier), the risk score over the sections'
// levels and active risks, and each section's derived state across
// versions, line counts, reviewers and review score. It never calls a model.
func HostParts(b *bundlepkg.Bundle, sections []HostSection, in HostInput) (Host, error) {
	if b == nil {
		return Host{}, errors.New("review: no bundle")
	}
	doc := reviewDocument{Schema: reviewschema.Name}
	for _, s := range sections {
		section := documentSection{sectionContent: sectionContent{ID: s.ID, Kind: s.Kind, Hunks: slices.Clone(s.Hunks), Details: s.Details}}
		if section.Hunks == nil {
			section.Hunks = []string{}
		}
		if s.Level != nil && s.Kind != "mechanical" {
			assessment := &sectionAssessment{Level: *s.Level, Factors: []string{}}
			for _, reason := range s.Reasons {
				assessment.Reasons = append(assessment.Reasons, sectionAssessmentReason{Text: reason})
			}
			section.Assessment = assessment
		}
		for _, r := range s.Risks {
			risk := documentRisk{authoredRisk: authoredRisk{ID: r.ID, Tier: r.Tier, Text: r.Text}, State: r.State}
			for _, raw := range r.Anchors {
				var anchor documentAnchor
				if json.Unmarshal(raw, &anchor) == nil {
					risk.Anchors = append(risk.Anchors, anchor)
				}
			}
			section.Risks = append(section.Risks, risk)
		}
		doc.Sections = append(doc.Sections, section)
	}
	hunks := map[string]bundlepkg.Hunk{}
	hunksByPath := map[string][]string{}
	for _, h := range b.Diff {
		hunks[h.ID] = h
		hunksByPath[h.Path] = append(hunksByPath[h.Path], h.ID)
	}
	index, checkCounts, _, _ := bundleIndex(b)
	files, additions, deletions := diffTotals(b)
	notes := threadNotes(append([]string{}, b.Truncated...), b)
	var previousVersion *int
	if in.PreviousVersion > 0 {
		previousVersion = &in.PreviousVersion
	}
	state, merged := bundlePRState(b)
	var reviewMD *string
	reviewMDChanged := b.ReviewConfig != nil && b.ReviewConfig.ChangedInPullRequest
	if b.ReviewConfig != nil && b.ReviewConfig.Present {
		reviewMD = &b.ReviewConfig.Digest
	}
	now := in.Now.UTC().Format(time.RFC3339Nano)
	doc.Review = documentReview{
		Repository: b.Repository, PullRequest: b.PullRequest.Number, PRState: state,
		Head: b.PullRequest.HeadSHA, Base: b.PullRequest.BaseSHA, MergeBase: b.PullRequest.MergeBaseSHA,
		MergedCommit: merged, Version: in.Version, PreviousVersion: previousVersion,
		GenerationState: "current", GeneratedAt: now, EvidenceRefreshedAt: now,
		Relations: json.RawMessage("[]"),
		Generator: documentReviewGenerator{Model: in.Model, ReviewMD: reviewMD, ReviewMDChanged: reviewMDChanged, Truncated: notes, SchemaRevision: in.SchemaRevision, PromptVersion: in.PromptVersion},
		Sources:   bundleSources(b),
	}

	tiers := []string{"mechanical", "unrated", "low", "medium", "high", "critical"}
	rank := func(tier string) int { return slices.Index(tiers, tier) }
	riskLOC := map[string]int{}
	for _, tier := range tiers {
		riskLOC[tier] = 0
	}
	hunkTiers := map[string]string{}
	for i := range doc.Sections {
		s := &doc.Sections[i]
		if len(s.Hunks) == 0 {
			continue
		}
		for _, risk := range s.Risks {
			if risk.State == "mitigated" {
				continue
			}
			for _, anchor := range risk.Anchors {
				var ids []string
				switch anchor.Type {
				case "hunk":
					ids = []string{anchor.identifier()}
				case "file":
					ids = hunksByPath[valueOrEmpty(anchor.Path)]
				}
				for _, id := range ids {
					if rank(risk.Tier) > rank(hunkTiers[id]) {
						hunkTiers[id] = risk.Tier
					}
				}
			}
		}
	}
	const (
		uncovered = iota
		coveredMechanical
		coveredLogical
	)
	covered := map[string]int{}
	for i := range doc.Sections {
		s := &doc.Sections[i]
		sectionState := "new"
		if old, ok := in.PreviousHunks[s.ID]; ok {
			sectionState = "changed"
			oldIDs, newIDs := slices.Clone(old), slices.Clone(s.Hunks)
			sort.Strings(oldIDs)
			sort.Strings(newIDs)
			if slices.Equal(oldIDs, newIDs) {
				sectionState = "unchanged"
			}
		}
		if len(s.Hunks) == 0 {
			sectionState = "stale"
		}
		sectionTier := "unrated"
		if s.Kind == "mechanical" {
			sectionTier = "mechanical"
		}
		for _, risk := range s.Risks {
			if risk.State != "mitigated" && sectionState != "stale" && rank(risk.Tier) > rank(sectionTier) {
				sectionTier = risk.Tier
			}
		}
		sectionAdd, sectionDel := 0, 0
		sectionRoles := map[string]int{}
		for _, id := range s.Hunks {
			h := hunks[id]
			sectionAdd += h.Additions
			sectionDel += h.Deletions
			sectionRoles[h.Role] += h.Additions + h.Deletions
			if s.Kind == "mechanical" {
				covered[id] = max(covered[id], coveredMechanical)
			} else {
				covered[id] = coveredLogical
			}
		}
		s.Derived = documentDerived{State: sectionState, RiskTier: sectionTier, Additions: sectionAdd, Deletions: sectionDel, RoleLOC: sectionRoles, Reviewers: requestedReviewers(b.PullRequest.RequestedReviewers), EvidenceSummary: map[string]int{}}
		if score, reason := sectionReviewScore(s); score > 0 {
			s.Derived.ReviewScore, s.Derived.ReviewReason = &score, reason
		}
	}
	roleLOC := map[string]int{}
	unassigned := []string{}
	for _, h := range b.Diff {
		lines := h.Additions + h.Deletions
		roleLOC[h.Role] += lines
		tier := hunkTiers[h.ID]
		switch {
		case covered[h.ID] == uncovered:
			unassigned = append(unassigned, h.ID)
			tier = "unrated"
		case covered[h.ID] == coveredMechanical:
			tier = "mechanical"
		case tier == "":
			tier = "unrated"
		}
		riskLOC[tier] += lines
	}
	doc.Stats = documentStats{Sections: len(doc.Sections), Files: files, Hunks: len(b.Diff), Additions: additions, Deletions: deletions, RoleLOC: roleLOC, RiskLOC: riskLOC, Checks: checkCounts}
	doc.Summary.RiskScore = riskScore(doc.Sections, b.Diff)
	doc.Index, doc.AgentReviews, doc.UnassignedHunks = index, agentReviews(b, doc.Sections, doc.Review.Head), unassigned
	setValidation(&doc, b)
	doc.Review.Sources.AgentReviews = len(doc.AgentReviews)
	countFindings(&doc)

	var out Host
	var err error
	encode := func(v any) json.RawMessage {
		if err != nil {
			return nil
		}
		var encoded []byte
		encoded, err = json.Marshal(v)
		return encoded
	}
	out.Review, out.Index, out.Stats, out.RiskScore = encode(doc.Review), encode(doc.Index), encode(doc.Stats), encode(doc.Summary.RiskScore)
	out.Derived = map[string]json.RawMessage{}
	for _, s := range doc.Sections {
		out.Derived[s.ID] = encode(s.Derived)
	}
	return out, err
}

// HunkLine is one line of an indexed hunk's diff: its kind (context, add or
// remove), its text and its line numbers on each side (0 for none).
type HunkLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old_line,omitempty"`
	New  int    `json:"new_line,omitempty"`
}

// HunkLines are a bundle hunk's diff lines as the index records them, with
// its header, or false when its patch is missing or does not parse whole.
func HunkLines(h bundlepkg.Hunk) (string, []HunkLine, bool) {
	diff := indexedHunkDiff(h)
	if diff == nil {
		return "", nil, false
	}
	lines := make([]HunkLine, 0, len(diff.Lines))
	for _, l := range diff.Lines {
		lines = append(lines, HunkLine(l))
	}
	return diff.Header, lines, true
}

// MinutesForWords is the reading time of words at the review's reading
// speed, as ReviewMinutes rounds it: whole minutes, rounded up, within 1 to
// 240.
func MinutesForWords(words int) int {
	minutes := float64(words) / readingWordsPerMinute
	return int(min(max(math.Ceil(minutes), minReviewMinutes), maxReviewMinutes))
}
