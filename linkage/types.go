package linkage

import (
	"slices"
	"time"
)

// Role is the relationship between a session and a commit SHA (mirrors
// agent_session_commit_refs.role; kept as an independent string type here so
// this package stays free of any DB import).
type Role string

const (
	RoleCreated    Role = "created"
	RoleMentioned  Role = "mentioned"
	RoleCheckedOut Role = "checked_out"
	RoleStarting   Role = "starting"
)

// CommitRef is one role-tagged commit SHA carried on a RunCandidate.
type CommitRef struct {
	SHA  string
	Role Role
}

// RunCandidate is the in-memory projection of one stored session tree and the
// matcher evidence derived from it at ingest (specs/platform/session-ingest.md
// §7.3). Callers build it from the ingest and evidence tables; the matcher
// never touches Postgres.
type RunCandidate struct {
	// RunID is the tree's root session id.
	RunID string
	// AdditionalRepository identifies evidence scoped to the commit repository
	// through an active additional-repository scope.
	AdditionalRepository bool
	// Agent is the vendor that ran, e.g. "claude_code". This is what a
	// Co-Authored-By trailer names.
	Agent string
	// Source is how the tree reached us ("upload", "entire_checkpoint"). It
	// says nothing about which agent ran, which is why Agent exists.
	Source    string
	UserEmail string // "" if unknown
	Branches  []string
	StartedAt *time.Time
	EndedAt   *time.Time
	// IngestedAt is when the upload carrying this tree arrived. It stands in
	// for a missing EndedAt: an upload cannot hold activity later than itself,
	// so a session that never recorded an end is still bounded.
	IngestedAt *time.Time
	// CommitRefs are this tree's agent_session_commit_refs rows.
	CommitRefs []CommitRef
	// StartingCommitSHA is the role=starting ref's SHA, if any (denormalized
	// for the start_commit step; also present in CommitRefs).
	StartingCommitSHA string
	// FilesTouched is the union of the tree's read and edit evidence.
	FilesTouched []string
	// FilesEdited is the subset the tree edited.
	FilesEdited []string
}

// EffectiveEndedAt is the end the matcher measures a commit against: the
// recorded end, or the upload that carried the tree when it recorded none.
func (c RunCandidate) EffectiveEndedAt() *time.Time {
	if c.EndedAt != nil {
		return c.EndedAt
	}
	return c.IngestedAt
}

// AgentOrSource is what the coauthor step compares against a commit's
// Co-Authored-By trailers: the vendor that ran, falling back to how the tree
// arrived when a candidate carries no vendor.
func (c RunCandidate) AgentOrSource() string {
	if c.Agent != "" {
		return c.Agent
	}
	return c.Source
}

// Commit is what the matcher reads about one commit. Timestamps are RFC 3339
// strings as git prints them with %cI; a value that does not parse counts as
// unknown rather than failing the match.
type Commit struct {
	OID         string `json:"oid"`
	AuthorEmail string `json:"author_email"`
	CommittedAt string `json:"committed_at"`
}

// EnrichedCommit is a Commit with the derived facts the steps match on.
// AncestorOID is the commit the branch started from (the merge base with the
// base branch), FilesTouched its repository-relative paths, CoauthoredAgents
// the agent slugs named by Co-Authored-By trailers.
type EnrichedCommit struct {
	Commit
	IsMerge          bool     `json:"is_merge"`
	AncestorOID      string   `json:"ancestor_oid,omitempty"`
	CoauthoredAgents []string `json:"coauthored_agents"`
	FilesTouched     []string `json:"files_touched"`
	BranchNames      []string `json:"branch_names"`
}

// Method identifies which step or attribution source finalized an edge.
type Method string

const (
	MethodCommitRef        Method = "commit_ref"
	MethodStartCommit      Method = "start_commit"
	MethodFilesOverlap     Method = "files_overlap"
	MethodEntireCheckpoint Method = "entire_checkpoint"

	// MethodManual is a person's answer rather than a step's. It outranks every
	// heuristic method and survives re-resolution.
	MethodManual  Method = "manual"
	MethodTrailer Method = "trailer"

	// Reserved: not yet implemented.
	MethodCollector     Method = "collector"
	MethodBotConvention Method = "bot_convention"
)

// EdgeStatus is the outcome of matching one commit.
type EdgeStatus string

const (
	StatusMatched       EdgeStatus = "matched"
	StatusExcludedMerge EdgeStatus = "excluded_merge"
	StatusUnmatched     EdgeStatus = "unmatched"
)

type TieResolution string

const (
	TieResolutionLinkedAll    TieResolution = "linked_all"
	TieResolutionLinkedCapped TieResolution = "linked_capped"
	TieResolutionScoreOrder   TieResolution = "score_order"
)

type TiePeer struct {
	RunID string
	Score float64
}

type TieDiagnostic struct {
	Resolution TieResolution
	Peers      []TiePeer
}

// TimeDiscount is why a files_overlap edge's confidence was reduced for when
// the work happened rather than for what it touched: how far past the
// session's effective end the commit landed, how wide the session's window
// was, whether that end was the upload time, and the resulting factor.
type TimeDiscount struct {
	GapSeconds    float64
	WindowSeconds float64
	SyntheticEnd  bool
	Factor        float64
}

// Reason codes for exclusion / unmatched diagnostics. The first group is
// reported by the filter step that emptied the candidate set (see
// FilteredWithReason), so an unmatched edge names the step that ruled every
// run out instead of the generic ReasonNoCandidates.
const (
	ReasonBothTimestampsMissing  = "both_timestamps_missing"
	ReasonOutsideTimeframe       = "outside_timeframe"
	ReasonInvalidCommitTimestamp = "invalid_commit_timestamp"
	ReasonAgentMismatch          = "agent_mismatch"
	ReasonUserEmailMismatch      = "user_email_mismatch"
	ReasonBranchMismatch         = "branch_mismatch"

	ReasonNoFileOverlap   = "no_file_overlap"
	ReasonNoStepFinalized = "no_step_finalized_match"
	ReasonNoCandidates    = "no_candidates"
	ReasonMergeCommit     = "merge_commit"

	// A malformed trailer falls through to the heuristic steps but remains
	// visible as a diagnostic.
	ReasonEntireCheckpointTrailerInvalid = "entire_checkpoint_trailer_invalid"
	// The trailer named a checkpoint we do not hold the sessions for yet.
	ReasonAwaitingCheckpoint = "awaiting_checkpoint"
	// The commit is signed by an agent and the repository holds no session at
	// all: nobody has sent us the run that wrote it. Written by the resolver,
	// not here — an empty candidate set does not establish it. See
	// CandidatePrefilterSound.
	ReasonAwaitingAgentSession = "awaiting_agent_session"
	// The commit was eligible when queued but disappeared before resolution,
	// usually because a retained PR ref was pruned or force-updated.
	ReasonGitObjectUnavailable = "git_object_unavailable"
	// Somebody removed the last session linked to the commit by hand.
	ReasonManuallyUnlinked = "manually_unlinked"
)

// Outcome classes group the reasons above into what a reader can act on. The
// set is closed and OutcomeClass is the only place a reason is mapped to one,
// so a new reason is classified once rather than in every report that shows it.
const (
	// An agent wrote the commit and we do not hold the session that did.
	// Nothing is wrong with the matching; the record has not reached us.
	ClassAwaitingSession = "awaiting_session"
	// Nothing about the commit says an agent wrote it, or something does and no
	// session we hold accounts for it. Either way the answer is not more data.
	ClassNoEvidence = "no_evidence"
	// Not an unattributed commit at all: it matched, it is a merge, or it has
	// no verdict yet.
	ClassNotApplicable = "not_applicable"
)

// awaitingSessionReasons are the reasons that mean an agent wrote the commit
// and we do not hold the session. Each one rests on the commit's own trailers —
// the first on Entire-Checkpoint, the other two on Co-Authored-By — which is why
// they belong together and why they read as agent work. A Co-Authored-By
// trailer also makes the outcome MethodTrailer, and reports let the method win,
// so those two reach a reader as the reason beneath a trailer attribution.
var awaitingSessionReasons = []string{
	ReasonAwaitingCheckpoint, ReasonAwaitingAgentSession, ReasonAgentMismatch,
}

// AwaitingSessionReasons is the list a query needs when it has to make the same
// split in SQL. It is passed in as a parameter rather than written out there,
// so the set stays defined here alone.
func AwaitingSessionReasons() []string {
	return slices.Clone(awaitingSessionReasons)
}

// OutcomeClass groups an edge's reason. An empty reason is not applicable: a
// matched edge carries none.
func OutcomeClass(reason string) string {
	switch {
	case slices.Contains(awaitingSessionReasons, reason):
		return ClassAwaitingSession
	case reason == "", reason == ReasonMergeCommit:
		return ClassNotApplicable
	default:
		return ClassNoEvidence
	}
}

// Edge is the matcher's in-memory output for one commit. This package does
// not persist it; a linkage_edges row is written by the resolver.
type Edge struct {
	CommitSHA            string
	RunID                string // "" if unmatched or excluded
	AdditionalRepository bool
	Status               EdgeStatus
	Method               Method // "" if no attribution method applies
	Confidence           float64
	MatchedBy            string // matching path that finalized the edge
	TrailerAgents        []string
	Score                float64
	ScoreValid           bool // Score is only meaningful when true (files_overlap)
	CandidatesRemaining  int
	Reason               string // exclusion / unmatched reason; "" if matched
	Tie                  *TieDiagnostic
	TimeDiscount         *TimeDiscount
}

// StepResult is the outcome of one match step: exactly one of Candidates or
// Edges is set. Use Filtered/FilteredWithReason/Final/FinalEdges to construct it.
type StepResult struct {
	Candidates []RunCandidate
	Edges      []Edge
	// Reason explains why this step dropped candidates. The pipeline only
	// reports it when the step emptied a non-empty set, which is the one case
	// where it is the whole story.
	Reason string
}

// Filtered returns a StepResult that narrows the candidate set without
// finalizing a match.
func Filtered(candidates []RunCandidate) StepResult {
	return StepResult{Candidates: candidates}
}

// FilteredWithReason is Filtered plus the diagnostic a caller should see if
// this step is the one that left nothing to match against.
func FilteredWithReason(candidates []RunCandidate, reason string) StepResult {
	return StepResult{Candidates: candidates, Reason: reason}
}

// Final returns a StepResult that finalizes edge and stops the step chain.
func Final(edge Edge) StepResult {
	return StepResult{Edges: []Edge{edge}}
}

// FinalEdges returns a StepResult that finalizes several edges at once.
func FinalEdges(edges []Edge) StepResult {
	return StepResult{Edges: edges}
}

// IsFinal reports whether this result finalized one or more edges.
func (r StepResult) IsFinal() bool {
	return len(r.Edges) > 0
}

// Step is one ordered filter/link step in the match pipeline.
type Step interface {
	// ID is the stable step id matched against Config.EnabledMatchSteps.
	ID() string
	Apply(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult
}

// PickLowestRunID returns the candidate with the lexicographically smallest
// RunID — the deterministic tie-break every hard-link and ranking step uses
// when more than one candidate remains. Panics on an empty slice: callers
// only reach for this after confirming at least one match.
func PickLowestRunID(candidates []RunCandidate) RunCandidate {
	lowest := candidates[0]
	for _, c := range candidates[1:] {
		if c.RunID < lowest.RunID {
			lowest = c
		}
	}
	return lowest
}

// MatchedEdge builds a status=matched Edge for a hard-link or ranked pick.
func MatchedEdge(commit EnrichedCommit, candidate RunCandidate, method Method, confidence float64, candidatesRemaining int) Edge {
	return Edge{
		CommitSHA:            commit.OID,
		RunID:                candidate.RunID,
		AdditionalRepository: candidate.AdditionalRepository,
		Status:               StatusMatched,
		Method:               method,
		Confidence:           confidence,
		MatchedBy:            string(method),
		CandidatesRemaining:  candidatesRemaining,
	}
}
