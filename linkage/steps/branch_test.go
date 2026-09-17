package steps

import (
	"testing"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

func TestBranchCompatible(t *testing.T) {
	tests := []struct {
		name            string
		candidateBranch string
		commitBranches  []string
		want            bool
	}{
		{"candidate missing", "", []string{"feature/x"}, true},
		{"commit missing", "feature/x", nil, true},
		{"exact match", "feature/x", []string{"feature/x"}, true},
		{"origin prefix stripped", "feature/x", []string{"origin/feature/x"}, true},
		{"case insensitive", "FEATURE/X", []string{"feature/x"}, true},
		{"disagreement", "feature/y", []string{"feature/x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := linkage.RunCandidate{}
			if tt.candidateBranch != "" {
				candidate.Branches = []string{tt.candidateBranch}
			}
			commit := linkage.EnrichedCommit{BranchNames: tt.commitBranches}
			if got := branchCompatible(candidate, commit); got != tt.want {
				t.Errorf("branchCompatible() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A session can switch branches mid-session (Branches is a set): keep when any
// candidate branch matches any commit branch.
func TestBranchCompatibleMultiBranchCandidate(t *testing.T) {
	candidate := linkage.RunCandidate{Branches: []string{"scratch", "feature/x"}}
	commit := linkage.EnrichedCommit{BranchNames: []string{"feature/x", "main"}}
	if !branchCompatible(candidate, commit) {
		t.Fatal("expected compatible: feature/x is shared")
	}

	noOverlap := linkage.RunCandidate{Branches: []string{"scratch", "other"}}
	if branchCompatible(noOverlap, commit) {
		t.Fatal("expected incompatible: no shared branch")
	}
}

func TestBranchStepExcludesDisagreements(t *testing.T) {
	step := BranchStep{}
	commit := linkage.EnrichedCommit{BranchNames: []string{"feature/x", "main"}}
	keep := linkage.RunCandidate{RunID: "a", Branches: []string{"feature/x"}}
	keepMissing := linkage.RunCandidate{RunID: "b"}
	drop := linkage.RunCandidate{RunID: "c", Branches: []string{"other"}}

	result := step.Apply(commit, []linkage.RunCandidate{keep, keepMissing, drop}, linkage.DefaultConfig())
	if len(result.Candidates) != 2 || result.Candidates[0].RunID != "a" || result.Candidates[1].RunID != "b" {
		t.Fatalf("candidates = %+v, want [a, b]", result.Candidates)
	}
}
