// Package steps holds one file per match step, each a pure linkage.Step.
// Ordering and selection are the pipeline's job, not this package's.
package steps

import (
	"time"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const TimeframeStepID = "timeframe"

// TimeframeStep keeps candidates whose run window overlaps the commit's
// CommittedAt. Policy:
//
//   - Both StartedAt and EndedAt missing: drop.
//   - StartedAt present: commit cannot precede it.
//   - Commit must be at or before the candidate's effective end plus the
//     tolerance — its EndedAt, or the upload that carried it when it recorded
//     none, so an unfinished session stops being a candidate for every later
//     commit.
//   - Neither an end nor an upload time: open-ended, keep. The candidate query
//     always supplies the upload time, so only package fixtures reach it.
//
// A CommittedAt that will not parse drops every candidate — fail closed
// rather than match on a timestamp nobody can read — but under a reason code
// of its own, since that is a bad commit record and not a run-timing verdict.
// This step is a soft filter and runs after commit_ref, so a run that
// verifiably created the commit is already linked before any of it applies.
type TimeframeStep struct{}

func (TimeframeStep) ID() string { return TimeframeStepID }

func (TimeframeStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	committedAt, err := time.Parse(time.RFC3339, commit.CommittedAt)
	if err != nil {
		return linkage.FilteredWithReason(nil, linkage.ReasonInvalidCommitTimestamp)
	}
	tolerance := cfg.TimeframeTolerance()
	kept := make([]linkage.RunCandidate, 0, len(candidates))
	timestamped := 0
	for _, c := range candidates {
		if c.StartedAt != nil || c.EndedAt != nil {
			timestamped++
		}
		if runInTimeframe(c, committedAt, tolerance) {
			kept = append(kept, c)
		}
	}
	// Distinguish "no run knows when it ran" from "no run ran then".
	reason := linkage.ReasonOutsideTimeframe
	if timestamped == 0 {
		reason = linkage.ReasonBothTimestampsMissing
	}
	return linkage.FilteredWithReason(kept, reason)
}

func runInTimeframe(candidate linkage.RunCandidate, committedAt time.Time, tolerance time.Duration) bool {
	if candidate.StartedAt == nil && candidate.EndedAt == nil {
		return false
	}
	if candidate.StartedAt != nil && committedAt.Before(*candidate.StartedAt) {
		return false
	}
	end := candidate.EffectiveEndedAt()
	if end == nil {
		return true
	}
	return !committedAt.After(end.Add(tolerance))
}
