// Package linkage is a pure, in-memory commit↔run matcher. It reads AgentRun
// projections and synthetic commit fixtures only — no database, no git, no
// subprocesses. It does not persist the edges it produces.
package linkage

import (
	"fmt"
	"time"
)

// ReviewConfidence is the confidence at which a match stops needing a person to
// look at it. The resolver will not add a weaker edge to a commit that already
// has one this strong, and the session list reads the same value, so a commit
// and the sessions on it cannot disagree about what counts as settled.
const ReviewConfidence = 0.7

// DefaultMatchSteps is the step order run when Config.EnabledMatchSteps is
// unset. commit_ref runs first: it links a run that recorded creating this
// exact SHA, evidence no soft filter can improve on, so it must not be
// gated behind one that can drop the run first. files_overlap always runs
// last: it is the only ranking finalizer, everything before it is a filter
// or a hard link.
var DefaultMatchSteps = []string{
	"commit_ref",
	"timeframe",
	"coauthor_agent",
	"user_email",
	"start_commit",
	"branch",
	"files_overlap",
}

// timeframeStepID and commitRefStepID are the two admissions the SQL candidate
// prefilter reproduces. steps.TimeframeStepID and steps.CommitRefStepID are the
// same ids; the steps package imports this one, so the constants cannot be
// shared back, and steps.TestDefaultStepsOrderAndIDs pins the two lists
// together.
const (
	timeframeStepID = "timeframe"
	commitRefStepID = string(MethodCommitRef)
)

// perCandidateFilterSteps are the built-in steps that can only drop a
// candidate, and only on that candidate's own evidence. Removing candidates
// from their input therefore removes exactly those from their output and
// admits nothing new, which is what lets them run before timeframe without
// invalidating the candidate prefilter. A step that ranks or compares
// candidates against each other does not belong here even if it never
// finalizes.
var perCandidateFilterSteps = map[string]bool{
	"coauthor_agent": true,
	"user_email":     true,
	"branch":         true,
}

// DefaultAgentAliases maps a Co-Authored-By agent name to the vendor slug it
// identifies. Keys are matched case-insensitively by callers; store them
// lower-cased.
//
// The keys must be the vocabulary commit enrichment actually emits:
// gitrepository identifies agents by trailer *email*, then reports a fixed
// slug ("claude", "codex", "cursor", "copilot", ...), never a display name.
// Aliases for display names ("claude code", "anthropic", "openai") could
// therefore never be hit. Enrichment slugs no agent of ours can claim (aider,
// devin, jules, ...) are deliberately absent: no candidate can
// claim them, so coauthor_agent rules every candidate out, which is correct.
func DefaultAgentAliases() map[string]string {
	return map[string]string{
		"claude":  "claude_code",
		"codex":   "codex",
		"cursor":  "cursor",
		"copilot": "copilot",
	}
}

// TimingConfig holds the matcher policy whose units are durations rather than
// the seconds used by the external TOML representation.
type TimingConfig struct {
	// Tolerance widens a run's [started_at, ended_at] window on the end side.
	Tolerance time.Duration
	// GapConfidenceFloor is the share of its confidence a match keeps when the
	// commit sits a full tolerance past the session's effective end.
	GapConfidenceFloor float64
	// FullConfidenceWindow is the longest session window that keeps full
	// confidence. Zero or less disables the long-window discount.
	FullConfidenceWindow time.Duration
}

// Config holds every matcher tunable. It owns the defaults and validation;
// platform/internal/config only decodes the external representation into it.
type Config struct {
	ExcludeMergeCommits bool

	// Timing controls admission and the confidence discounts for late commits
	// and long-running sessions.
	Timing TimingConfig

	// Soft files_overlap finalizer gates. Hard-link steps (commit_ref,
	// start_commit) finalize earlier and ignore these. The score they gate is
	// weighted containment — edits count fully and reads count partially — see
	// steps.FilesOverlapStep.
	FilesOverlapMinScore        float64
	FilesOverlapMinIntersection int
	// The fallback admits lower containment only with more exact edits.
	FilesOverlapFallbackScore float64
	FilesOverlapFallbackEdits int
	// FilesOverlapMinScoreMargin defines the band in which a lower-scored
	// runner-up is recorded on the winning edge.
	FilesOverlapMinScoreMargin float64

	// FilesOverlapMinConfidence and FilesOverlapMaxConfidence clamp a
	// files_overlap match's confidence: even a perfect score is a soft
	// signal next to a hard link.
	FilesOverlapMinConfidence float64
	FilesOverlapMaxConfidence float64

	// FilesOverlapMaxLinksPerCommit bounds how many sessions one commit may be
	// credited to by this step. Zero or less is unbounded. Exact ties are
	// common on small commits and the score cannot separate them, so without a
	// ceiling one commit accretes every session that clears the floor.
	FilesOverlapMaxLinksPerCommit int

	// CommitRefConfidence is the confidence assigned to a commit_ref hard
	// link.
	CommitRefConfidence float64
	// TrailerConfidence is the confidence assigned to trailer attribution.
	TrailerConfidence float64
	// StartCommitConfidence is the confidence assigned to a start_commit
	// hard link.
	StartCommitConfidence float64

	AgentAliases      map[string]string
	EnabledMatchSteps []string
}

// DefaultConfig returns the matcher's built-in tunables for standalone or
// test use. Callers needing overrides should copy this and mutate the copy;
// Config carries no globals.
func DefaultConfig() Config {
	return Config{
		ExcludeMergeCommits: true,
		Timing: TimingConfig{
			Tolerance:            24 * time.Hour,
			GapConfidenceFloor:   0.5,
			FullConfidenceWindow: 24 * time.Hour,
		},
		FilesOverlapMinScore:          0.6,
		FilesOverlapMinIntersection:   2,
		FilesOverlapFallbackScore:     0.15,
		FilesOverlapFallbackEdits:     4,
		FilesOverlapMinScoreMargin:    0.05,
		FilesOverlapMinConfidence:     0.05,
		FilesOverlapMaxConfidence:     0.75,
		FilesOverlapMaxLinksPerCommit: 3,
		CommitRefConfidence:           0.95,
		TrailerConfidence:             0.9,
		StartCommitConfidence:         0.9,
		AgentAliases:                  DefaultAgentAliases(),
		EnabledMatchSteps:             append([]string(nil), DefaultMatchSteps...),
	}
}

// TimeframeTolerance is how far past a session's effective end a commit may
// still be matched to it. The timeframe step, the SQL candidate prefilter and
// the requeue arms all read it here, because a second spelling of the
// conversion is a second value they could disagree on.
func (c Config) TimeframeTolerance() time.Duration {
	return c.Timing.Tolerance
}

// Validate rejects timing policy that would invert a discount or shrink the
// candidate window backwards. A non-positive full-confidence window remains
// the documented way to disable the long-window discount.
func (c Config) Validate() error {
	if c.Timing.Tolerance < 0 {
		return fmt.Errorf("timeframe tolerance must not be negative: %s", c.Timing.Tolerance)
	}
	if c.Timing.GapConfidenceFloor < 0 || c.Timing.GapConfidenceFloor > 1 {
		return fmt.Errorf("files-overlap gap confidence floor must be between 0 and 1: %v",
			c.Timing.GapConfidenceFloor)
	}
	return nil
}

// CandidatePrefilterSound reports whether a candidate set restricted to
// commit_ref ∪ timeframe — the sessions whose window covers the commit, plus
// the sessions that recorded creating its SHA — is a superset of every edge
// this step order can finalize. The resolver consults it per resolution and
// loads the repository's candidates unfiltered when it is false: an ablation
// is a legitimate experiment, and one that silently loses matches the
// configured chain would have made is worse than a slow one.
//
// It holds when every enabled step that can finalize an edge, other than
// commit_ref, runs after timeframe. Each clause:
//
//   - After timeframe, a step only ever sees candidates timeframe kept, and
//     the prefilter admits all of those, so its input is exactly the input a
//     full scan would hand it.
//   - commit_ref is exempt at any position. It finalizes only on a candidate
//     that recorded creating this SHA, and the prefilter's ref arm loads every
//     one of those whether or not its window covers the commit — so the set it
//     picks its lowest RunID from is complete either way.
//   - coauthor_agent, user_email and branch are exempt at any position: they
//     never finalize, and they drop candidates one at a time on that
//     candidate's own evidence, so a smaller input gives a correspondingly
//     smaller output and admits nothing new.
//   - Any other id — a custom or future step — counts as finalizing, and
//     before timeframe it makes this false. A step this package cannot reason
//     about must cost a full candidate load, not a silent loss.
//
// timeframe itself need not be enabled when nothing but commit_ref and those
// filters is: such a chain can only finalize on the ref arm, which the
// prefilter always carries.
//
// What the prefilter still moves when this holds is the diagnostic on an
// unmatched edge, never the edge: reason and candidates_remaining are read off
// a set timeframe's rejects were never loaded into
// (specs/platform/linkage/resolver.md).
func (c Config) CandidatePrefilterSound() bool {
	timeframeEnabled := false
	for _, id := range c.EnabledMatchSteps {
		switch {
		case id == timeframeStepID:
			timeframeEnabled = true
		case id == commitRefStepID, perCandidateFilterSteps[id]:
		case !timeframeEnabled:
			return false
		}
	}
	return true
}
