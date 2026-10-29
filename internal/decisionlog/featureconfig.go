package decisionlog

import (
	"time"

	"github.com/baum-ai/baum/features/assign"
	"github.com/baum-ai/baum/features/consolidate"
	"github.com/baum-ai/baum/features/summary"
)

// The feature log's choices, all in one place (README.md, "Feature log").
// They follow the hosted feature log's unless a comment says otherwise.
const (
	// AssignVersion, SummaryVersion and ConsolidateVersion are the published
	// prompts the feature log calls.
	AssignVersion      = assign.PublicVersion
	SummaryVersion     = summary.PublicVersion
	ConsolidateVersion = consolidate.PublicVersion

	// FeatureTick is how often the feature worker runs its stages.
	FeatureTick = time.Minute
	// FeatureCandidates is how many candidate features an assignment offers
	// (K); FeaturePathSlots and FeatureRecentSlots split them between the
	// candidates ranked by shared paths and the most recent.
	FeatureCandidates  = 16
	FeaturePathSlots   = FeatureCandidates * 5 / 8
	FeatureRecentSlots = FeatureCandidates - FeaturePathSlots
	// FeatureCandidateWindow is how recently a candidate feature must have
	// changed.
	FeatureCandidateWindow = 90 * 24 * time.Hour
	// FeatureLookback is how far back commits are linked to sessions, and
	// pull requests and sessions are picked up for assignment. Hosted looks
	// back a day; a laptop may be off for days, so locally it is a week.
	FeatureLookback = 7 * 24 * time.Hour
	// FeatureQuiet is how long a session must have been unchanged before it
	// is placed, how long a pull request without known paths waits for them
	// and how long a feature's members must have been unchanged before its
	// text is written; it is also the debounce of the session window's
	// re-link.
	FeatureQuiet = 15 * time.Minute
	// FeaturePathTries is how many failed reads of a pull request's commits
	// at one head leave its paths unknown until the daily re-link.
	FeaturePathTries = 3
	// FeatureRelinkEvery is how often every commit of the lookback is linked
	// again.
	FeatureRelinkEvery = 24 * time.Hour
	// FeatureReassignShare and FeatureReassignPaths are the large-update rule
	// (features.LargeUpdate) for assigning a pull request again.
	FeatureReassignShare = 0.5
	FeatureReassignPaths = 3
	// FeatureAssignRunLimit and FeatureSummaryRunLimit bound the items one
	// run of placement and the features one run of the text stage take.
	FeatureAssignRunLimit  = 100
	FeatureSummaryRunLimit = 50
	// FeatureSummaryConcurrency is how many features the text stage writes
	// at once, as a hosted rebuild does: each feature's text and pull
	// request summaries are calls of their own.
	FeatureSummaryConcurrency = 8
	// FeatureBackoff is how long an item or feature whose call failed
	// waits; after FeatureMaxFailures counted failures for one trigger it
	// waits for the next.
	FeatureBackoff     = 10 * time.Minute
	FeatureMaxFailures = 3
	// FeatureBatch is how many items one batch assignment holds.
	FeatureBatch = 25
	// FeatureConsolidateEvery is how often a repository's features are
	// consolidated, when they changed.
	FeatureConsolidateEvery = 7 * 24 * time.Hour
	// FeatureSplitPulls and FeatureSplitAge make a feature a split
	// candidate: more pull requests than that, or pull requests opened
	// further apart. A pass offers at most FeatureMaxSplits features.
	FeatureSplitPulls = 30
	FeatureSplitAge   = 8 * 7 * 24 * time.Hour
	FeatureMaxSplits  = 3
	// FeatureConsolidateOutputTokens bounds a merge or split reply. Hosted
	// uses consolidate.MaxOutputTokens; locally Claude Code's models think
	// within the reply's bound whatever thinking is set to, and a merge over a
	// first build's features ran out of it, so local calls get more room.
	FeatureConsolidateOutputTokens = 4 * consolidate.MaxOutputTokens
	// FeatureSplitHold is how long after a split a merge undoing it is not
	// applied.
	FeatureSplitHold = 4 * 7 * 24 * time.Hour
	// FeatureFirstSyncWindow is how far back the first sync lists a
	// repository's pull requests; a first build lists its whole history
	// (FeatureWorker.ListHistory).
	FeatureFirstSyncWindow = 180 * 24 * time.Hour
	// FeatureBuildTries is how many times a first build makes a failed batch
	// call before splitting it; FeatureBuildCandidates is the most
	// candidates a batch call offers; FeatureBuildSummarizeEvery is how many
	// batches go between its text passes.
	FeatureBuildTries          = 2
	FeatureBuildCandidates     = 40
	FeatureBuildSummarizeEvery = 3
)

// What a build's estimate assumes of what it cannot know beforehand (local
// only; hosted builds have no estimate): the pull requests a new feature
// gathers, and how many times a new feature's text is written.
const (
	FeatureEstimatePullsPerFeature = 3
	FeatureEstimateTextPasses      = 2
)
