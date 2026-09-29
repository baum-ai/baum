package steps

import (
	"strings"

	"github.com/lastpersonlabs/baum/linkage"
)

const StartCommitStepID = "start_commit"

// StartCommitStep hard-links a commit to the run whose StartingCommitSHA
// (role=starting) equals the commit's AncestorOID — Codex-oriented, but a
// harmless no-op for runs that never set it. A commit with no AncestorOID
// (root commit, or enrichment gap) leaves candidates untouched. Ties break
// on the lowest RunID.
type StartCommitStep struct{}

func (StartCommitStep) ID() string { return StartCommitStepID }

func (StartCommitStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	if commit.AncestorOID == "" {
		return linkage.Filtered(candidates)
	}

	var matches []linkage.RunCandidate
	for _, c := range candidates {
		if c.StartingCommitSHA != "" && strings.EqualFold(c.StartingCommitSHA, commit.AncestorOID) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return linkage.Filtered(candidates)
	}

	chosen := linkage.PickLowestRunID(matches)
	return linkage.Final(linkage.MatchedEdge(commit, chosen, linkage.MethodStartCommit, cfg.StartCommitConfidence, len(matches)))
}
