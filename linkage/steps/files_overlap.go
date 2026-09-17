package steps

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const (
	FilesOverlapStepID  = "files_overlap"
	readOnlyTouchWeight = 0.25
)

// FilesOverlapStep is the soft finalizer: it ranks remaining candidates by how
// much of the commit a run accounts for and links every exactly tied top
// candidate that clears every gate on Config for itself. Hard-link steps
// (commit_ref, start_commit) finalize earlier and never reach here.
//
// The score is containment, not Jaccard overlap. Jaccard divides by the union
// of the two file sets, which penalises the many-commits-to-one-session
// cardinality the matcher supports: a forty-file session and the four-file
// commit it produced cannot score above ~0.1 however certain the match.
type FilesOverlapStep struct{}

func (FilesOverlapStep) ID() string { return FilesOverlapStepID }

func (FilesOverlapStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	if len(candidates) == 0 || len(commit.FilesTouched) == 0 {
		return linkage.Filtered(candidates)
	}

	type scoredCandidate struct {
		score        float64
		intersection int
		distance     time.Duration
		candidate    linkage.RunCandidate
	}
	commitFiles := toSet(commit.FilesTouched)
	committedAt, committedAtKnown := parseCommitTime(commit.CommittedAt)
	scored := make([]scoredCandidate, len(candidates))
	for i, c := range candidates {
		score, intersection := containment(c.FilesTouched, c.FilesEdited, commitFiles)
		scored[i] = scoredCandidate{
			score:        score,
			intersection: intersection,
			distance:     endDistance(c.EndedAt, committedAt, committedAtKnown),
			candidate:    c,
		}
	}
	// Highest score first. Equal scores go to whichever run ended nearest the
	// commit; RunID only breaks what that leaves.
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].distance != scored[j].distance {
			return scored[i].distance < scored[j].distance
		}
		return scored[i].candidate.RunID < scored[j].candidate.RunID
	})

	// Capped at what the commit can supply: a one-file commit cannot share two
	// files, and applying the floor literally made every such commit unmatchable.
	minIntersection := min(cfg.FilesOverlapMinIntersection, len(commitFiles))
	topScore := scored[0].score
	// Every tied candidate clears the gates on its own evidence. Reading them off
	// the first candidate instead would let its failure veto a peer that passes,
	// which is the unmatched outcome this step exists to stop.
	tied := make([]scoredCandidate, 0, len(scored))
	runnerUp := -1
	for i, c := range scored {
		if c.score != topScore {
			runnerUp = i
			break
		}
		if c.intersection < minIntersection {
			continue
		}
		broadCommitMatch := cfg.FilesOverlapFallbackEdits > 0 && cfg.FilesOverlapFallbackScore > 0 &&
			c.intersection >= cfg.FilesOverlapFallbackEdits && c.score >= cfg.FilesOverlapFallbackScore
		if c.score < cfg.FilesOverlapMinScore && !broadCommitMatch {
			continue
		}
		tied = append(tied, c)
	}
	if len(tied) == 0 {
		return linkage.Filtered(candidates)
	}

	edges := make([]linkage.Edge, 0, len(tied))
	for i, c := range tied {
		confidence := clampConfidence(c.score, cfg.FilesOverlapMinConfidence, cfg.FilesOverlapMaxConfidence)
		discount := timeDiscount(c.candidate, committedAt, committedAtKnown, cfg)
		if discount != nil {
			confidence = max(confidence*discount.Factor, cfg.FilesOverlapMinConfidence)
		}
		edge := linkage.MatchedEdge(commit, c.candidate, linkage.MethodFilesOverlap, confidence, len(candidates))
		edge.Score = c.score
		edge.ScoreValid = true
		edge.TimeDiscount = discount
		switch {
		case len(tied) > 1:
			peers := make([]linkage.TiePeer, 0, len(tied)-1)
			for j, peer := range tied {
				if i != j {
					peers = append(peers, linkage.TiePeer{RunID: peer.candidate.RunID, Score: peer.score})
				}
			}
			edge.Tie = &linkage.TieDiagnostic{Resolution: linkage.TieResolutionLinkedAll, Peers: peers}
		case runnerUp >= 0 && topScore-scored[runnerUp].score < cfg.FilesOverlapMinScoreMargin:
			edge.Tie = &linkage.TieDiagnostic{
				Resolution: linkage.TieResolutionScoreOrder,
				Peers: []linkage.TiePeer{{
					RunID: scored[runnerUp].candidate.RunID,
					Score: scored[runnerUp].score,
				}},
			}
		}
		edges = append(edges, edge)
	}
	return linkage.FinalEdges(edges)
}

// timeDiscount lowers files_overlap confidence for post-session gaps and long
// session windows without changing rank. nil means neither factor discounted
// the match.
func timeDiscount(
	candidate linkage.RunCandidate, committedAt time.Time, committedAtKnown bool, cfg linkage.Config,
) *linkage.TimeDiscount {
	end := candidate.EffectiveEndedAt()
	discount := linkage.TimeDiscount{
		Factor:       1,
		SyntheticEnd: candidate.EndedAt == nil && candidate.IngestedAt != nil,
	}
	tolerance := cfg.Timing.Tolerance.Seconds()
	if committedAtKnown && end != nil && tolerance > 0 {
		if gap := committedAt.Sub(*end).Seconds(); gap > 0 {
			discount.GapSeconds = gap
			discount.Factor *= 1 - (1-cfg.Timing.GapConfidenceFloor)*min(1, gap/tolerance)
		}
	}
	fullConfidenceWindow := cfg.Timing.FullConfidenceWindow.Seconds()
	if end != nil && candidate.StartedAt != nil && fullConfidenceWindow > 0 {
		if window := end.Sub(*candidate.StartedAt).Seconds(); window > 0 {
			discount.WindowSeconds = window
			discount.Factor *= min(1, fullConfidenceWindow/window)
		}
	}
	if discount.Factor >= 1 {
		return nil
	}
	return &discount
}

func parseCommitTime(value string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339, value)
	return at, err == nil
}

// endDistance is how far a run's end sits from the commit, which orders equal
// scores: ENG-279 measured that as decisive on every tie in the bounce data. A
// run that never recorded an end sorts last rather than winning the tie by
// accident. So does every run when the commit timestamp will not parse, which
// is defence rather than a live case: timeframe parses the same value first and
// fails closed, so the default chain never reaches this step without one.
func endDistance(endedAt *time.Time, committedAt time.Time, committedAtKnown bool) time.Duration {
	if !committedAtKnown || endedAt == nil {
		return math.MaxInt64
	}
	if d := committedAt.Sub(*endedAt); d >= 0 {
		return d
	}
	return endedAt.Sub(committedAt)
}

// ExplainedFiles returns the commit paths a run's edits account for, paired the
// same way the score pairs them. Reads are excluded: a file somebody read is not
// a file they changed, so it leaves the commit no better explained.
func ExplainedFiles(commit linkage.EnrichedCommit, candidate linkage.RunCandidate) []string {
	commitFiles := toSet(commit.FilesTouched)
	explained := make(map[string]bool, len(commitFiles))
	matchPaths(toSet(candidate.FilesEdited), commitFiles, explained)
	return setValues(explained)
}

func clampConfidence(score, min, max float64) float64 {
	if score < min {
		return min
	}
	if score > max {
		return max
	}
	return score
}

// containment scores edits at full weight and reads at readOnlyTouchWeight.
// The intersection is edited files only, for the minimum-intersection gate.
func containment(runFiles, editedFiles []string, commitFiles map[string]bool) (float64, int) {
	if len(commitFiles) == 0 {
		return 0, 0
	}
	touched := toSet(runFiles)
	edited := toSet(editedFiles)
	for path := range edited {
		delete(touched, path)
	}

	matchedCommits := make(map[string]bool, len(commitFiles))
	intersection := matchPaths(edited, commitFiles, matchedCommits)
	readIntersection := matchPaths(touched, commitFiles, matchedCommits)
	score := float64(intersection) + float64(readIntersection)*readOnlyTouchWeight
	return score / float64(len(commitFiles)), intersection
}

// matchPaths pairs each piece of session evidence with at most one commit
// path. Exact repo-relative spellings win. The suffix fallback recovers
// evidence that older ingests incorrectly made relative to a session cwd
// below the repository root (for example "agent/mod.rs" for
// "src-tauri/src/agent/mod.rs"). Requiring a slash boundary avoids partial
// component matches, and one-to-one pairing prevents a short spelling such as
// "index.ts" from accounting for several files in one commit.
func matchPaths(runFiles, commitFiles, matchedCommits map[string]bool) int {
	runPaths := setValues(runFiles)
	commitPaths := setValues(commitFiles)
	matchedRuns := make(map[string]bool, len(runPaths))
	matches := 0

	for _, runPath := range runPaths {
		if commitFiles[runPath] && !matchedCommits[runPath] {
			matchedRuns[runPath] = true
			matchedCommits[runPath] = true
			matches++
		}
	}
	for _, runPath := range runPaths {
		if matchedRuns[runPath] {
			continue
		}
		candidate := ""
		for _, commitPath := range commitPaths {
			if matchedCommits[commitPath] || !strings.HasSuffix(commitPath, "/"+runPath) {
				continue
			}
			if candidate != "" {
				candidate = ""
				break
			}
			candidate = commitPath
		}
		if candidate != "" {
			matchedCommits[candidate] = true
			matches++
		}
	}
	return matches
}

func setValues(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	// Prefer the most specific path when suffixes overlap; lexical order keeps
	// the one-to-one pairing deterministic for equal-length paths.
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	return values
}

func toSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return set
}
