package steps

import (
	"strings"
	"testing"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const testAncestorSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestStartCommitNoopWithoutAncestor(t *testing.T) {
	step := StartCommitStep{}
	commit := linkage.EnrichedCommit{}
	candidates := []linkage.RunCandidate{{RunID: "a", StartingCommitSHA: testAncestorSHA}}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough, got a final edge")
	}
	if len(result.Candidates) != len(candidates) {
		t.Fatalf("candidates = %+v, want unchanged", result.Candidates)
	}
}

func TestStartCommitNoopWithoutMatchingStart(t *testing.T) {
	step := StartCommitStep{}
	commit := linkage.EnrichedCommit{AncestorOID: testAncestorSHA}
	candidates := []linkage.RunCandidate{{RunID: "a", StartingCommitSHA: "cccccccccccccccccccccccccccccccccccccccc"}}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough, got a final edge")
	}
}

func TestStartCommitHardLinksMatch(t *testing.T) {
	step := StartCommitStep{}
	commit := linkage.EnrichedCommit{AncestorOID: testAncestorSHA}
	hit := linkage.RunCandidate{RunID: "hit", Source: "codex", StartingCommitSHA: testAncestorSHA}
	other := linkage.RunCandidate{RunID: "other", Source: "codex"}

	result := step.Apply(commit, []linkage.RunCandidate{other, hit}, linkage.DefaultConfig())
	if !result.IsFinal() {
		t.Fatal("expected a final edge")
	}
	edge := result.Edges[0]
	if edge.RunID != "hit" || edge.MatchedBy != "start_commit" {
		t.Fatalf("edge = %+v", edge)
	}
	want := linkage.DefaultConfig().StartCommitConfidence
	if edge.Confidence != want {
		t.Fatalf("Confidence = %v, want %v", edge.Confidence, want)
	}
}

func TestStartCommitTieBreakLowestRunID(t *testing.T) {
	step := StartCommitStep{}
	commit := linkage.EnrichedCommit{AncestorOID: testAncestorSHA}
	a := linkage.RunCandidate{RunID: "run-z", StartingCommitSHA: testAncestorSHA}
	// Case-insensitive equality.
	b := linkage.RunCandidate{RunID: "run-a", StartingCommitSHA: strings.ToUpper(testAncestorSHA)}

	result := step.Apply(commit, []linkage.RunCandidate{a, b}, linkage.DefaultConfig())
	if len(result.Edges) != 1 || result.Edges[0].RunID != "run-a" {
		t.Fatalf("edges = %+v, want RunID run-a", result.Edges)
	}
	if result.Edges[0].CandidatesRemaining != 2 {
		t.Fatalf("CandidatesRemaining = %d, want 2", result.Edges[0].CandidatesRemaining)
	}
}
