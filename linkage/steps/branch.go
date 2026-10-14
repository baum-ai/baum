package steps

import (
	"strings"

	"github.com/baum-ai/baum/linkage"
)

const BranchStepID = "branch"

// branchPrefixes are stripped before comparing branch names, so
// "feature/x", "origin/feature/x", and "refs/heads/feature/x" all match.
var branchPrefixes = []string{"refs/heads/", "refs/remotes/origin/", "origin/"}

// BranchStep excludes candidates whose Branches disagree with the commit's
// BranchNames. If either side has no branch info, it does not exclude.
// RunCandidate.Branches is a set: the branch
// history sessioncorrelate derived: a session's branch can change mid-session, so this step keeps a candidate when
// any of its branches is compatible with any of the commit's.
type BranchStep struct{}

func (BranchStep) ID() string { return BranchStepID }

func (BranchStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	kept := make([]linkage.RunCandidate, 0, len(candidates))
	for _, c := range candidates {
		if branchCompatible(c, commit) {
			kept = append(kept, c)
		}
	}
	return linkage.FilteredWithReason(kept, linkage.ReasonBranchMismatch)
}

func branchCompatible(candidate linkage.RunCandidate, commit linkage.EnrichedCommit) bool {
	candidateBranches := normalizedBranchSet(candidate.Branches)
	if len(candidateBranches) == 0 {
		return true
	}
	commitBranches := normalizedBranchSet(commit.BranchNames)
	if len(commitBranches) == 0 {
		return true
	}
	for b := range candidateBranches {
		if commitBranches[b] {
			return true
		}
	}
	return false
}

func normalizedBranchSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		if n := normalizeBranch(name); n != "" {
			set[n] = true
		}
	}
	return set
}

func normalizeBranch(name string) string {
	text := strings.TrimSpace(name)
	for _, prefix := range branchPrefixes {
		if strings.HasPrefix(text, prefix) {
			text = text[len(prefix):]
			break
		}
	}
	return strings.ToLower(text)
}
