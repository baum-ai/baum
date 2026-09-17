package steps

import (
	"strings"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const CommitRefStepID = "commit_ref"

// CommitRefStep hard-links a commit to the run that recorded it with role
// "created" (full SHA or an unambiguous >=7-hex-char prefix), ignoring
// every other role. Ties break on the lowest RunID.
type CommitRefStep struct{}

func (CommitRefStep) ID() string { return CommitRefStepID }

func (CommitRefStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	var matches []linkage.RunCandidate
	for _, c := range candidates {
		if hasCreatedRef(c, commit.OID) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return linkage.Filtered(candidates)
	}

	chosen := linkage.PickLowestRunID(matches)
	return linkage.Final(linkage.MatchedEdge(commit, chosen, linkage.MethodCommitRef, cfg.CommitRefConfidence, len(matches)))
}

func hasCreatedRef(candidate linkage.RunCandidate, commitSHA string) bool {
	for _, ref := range candidate.CommitRefs {
		if ref.Role == linkage.RoleCreated && shaMatches(ref.SHA, commitSHA) {
			return true
		}
	}
	return false
}

// shaMatches reports full equality (case-insensitive), or a prefix match
// when the shorter side is at least 7 hex characters and is a prefix of
// the longer side.
func shaMatches(a, b string) bool {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == b {
		return true
	}
	shorter, longer := a, b
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < 7 {
		return false
	}
	return strings.HasPrefix(longer, shorter)
}
