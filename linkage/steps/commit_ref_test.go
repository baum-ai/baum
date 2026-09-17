package steps

import (
	"testing"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCommitRefNoMatchIsPassthrough(t *testing.T) {
	step := CommitRefStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: testSHA}}
	candidates := []linkage.RunCandidate{
		{RunID: "a", CommitRefs: []linkage.CommitRef{{SHA: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Role: linkage.RoleMentioned}}},
	}
	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough, got a final edge")
	}
	if len(result.Candidates) != len(candidates) {
		t.Fatalf("candidates = %+v, want unchanged", result.Candidates)
	}
}

func TestCommitRefHardLinksCreatedRole(t *testing.T) {
	step := CommitRefStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: testSHA}}
	hit := linkage.RunCandidate{RunID: "hit", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleCreated}}}
	other := linkage.RunCandidate{RunID: "other"}

	result := step.Apply(commit, []linkage.RunCandidate{other, hit}, linkage.DefaultConfig())
	if !result.IsFinal() {
		t.Fatal("expected a final edge")
	}
	edge := result.Edges[0]
	if edge.RunID != "hit" || edge.MatchedBy != "commit_ref" || edge.Status != linkage.StatusMatched {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.CandidatesRemaining != 1 {
		t.Fatalf("CandidatesRemaining = %d, want 1", edge.CandidatesRemaining)
	}
	want := linkage.DefaultConfig().CommitRefConfidence
	if edge.Confidence != want {
		t.Fatalf("Confidence = %v, want %v", edge.Confidence, want)
	}
}

func TestCommitRefIgnoresRolesOtherThanCreated(t *testing.T) {
	step := CommitRefStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: testSHA}}
	candidates := []linkage.RunCandidate{
		{RunID: "mentioned", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleMentioned}}},
		{RunID: "checked_out", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleCheckedOut}}},
		{RunID: "starting", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleStarting}}},
	}
	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough, got a final edge")
	}
	if len(result.Candidates) != len(candidates) {
		t.Fatalf("candidates = %+v, want unchanged", result.Candidates)
	}
}

func TestCommitRefTieBreakLowestRunID(t *testing.T) {
	step := CommitRefStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: testSHA}}
	a := linkage.RunCandidate{RunID: "run-b", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleCreated}}}
	b := linkage.RunCandidate{RunID: "run-a", CommitRefs: []linkage.CommitRef{{SHA: testSHA, Role: linkage.RoleCreated}}}

	result := step.Apply(commit, []linkage.RunCandidate{a, b}, linkage.DefaultConfig())
	if len(result.Edges) != 1 || result.Edges[0].RunID != "run-a" {
		t.Fatalf("edges = %+v, want RunID run-a", result.Edges)
	}
}

func TestCommitRefAcceptsAbbreviatedSHAPrefix(t *testing.T) {
	step := CommitRefStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: testSHA}}
	hit := linkage.RunCandidate{RunID: "abbr", CommitRefs: []linkage.CommitRef{{SHA: testSHA[:12], Role: linkage.RoleCreated}}}

	result := step.Apply(commit, []linkage.RunCandidate{hit}, linkage.DefaultConfig())
	if !result.IsFinal() || result.Edges[0].RunID != "abbr" {
		t.Fatalf("edges = %+v, want a final match on abbr", result.Edges)
	}
}

func TestCommitRefRejectsShortPrefix(t *testing.T) {
	// A <7-char prefix is too ambiguous to hard-link.
	if shaMatches("aaaaaa", testSHA) {
		t.Fatal("expected a 6-char prefix to be rejected")
	}
	if !shaMatches("aaaaaaa", testSHA) {
		t.Fatal("expected a 7-char prefix to be accepted")
	}
}
