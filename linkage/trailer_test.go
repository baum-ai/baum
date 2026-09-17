package linkage

import (
	"reflect"
	"testing"
)

func TestNormalizeAgent(t *testing.T) {
	aliases := DefaultAgentAliases()
	tests := []struct {
		raw  string
		want string
	}{
		{"claude", "claude_code"},
		{"  Claude ", "claude_code"},
		{"cursor", "cursor"},
		{"aider", "aider"},
	}
	for _, tt := range tests {
		if got := NormalizeAgent(tt.raw, aliases); got != tt.want {
			t.Errorf("NormalizeAgent(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestTrailerAttributionNormalizesAndDeduplicates(t *testing.T) {
	commit := EnrichedCommit{
		CoauthoredAgents: []string{"claude", "aider", " Claude ", "cursor", "aider"},
	}

	got := TrailerAttribution(commit, DefaultConfig())
	want := []string{"claude_code", "aider", "cursor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TrailerAttribution() = %v, want %v", got, want)
	}
}

func TestTrailerAttributionEmpty(t *testing.T) {
	got := TrailerAttribution(EnrichedCommit{}, DefaultConfig())
	if got == nil || len(got) != 0 {
		t.Fatalf("TrailerAttribution() = %#v, want empty slice", got)
	}
}

func TestTrailerEdgePreservesUnmatchedOutcome(t *testing.T) {
	unmatched := Edge{
		CommitSHA:           "abc123",
		RunID:               "must-clear",
		Status:              StatusUnmatched,
		Reason:              ReasonAgentMismatch,
		CandidatesRemaining: 3,
	}
	cfg := DefaultConfig()
	agents := []string{"claude_code", "aider"}

	got := TrailerEdge(unmatched, agents, cfg)

	if got.CommitSHA != unmatched.CommitSHA ||
		got.Status != StatusUnmatched ||
		got.Reason != unmatched.Reason ||
		got.CandidatesRemaining != unmatched.CandidatesRemaining {
		t.Fatalf("TrailerEdge() did not preserve unmatched outcome: %+v", got)
	}
	if got.RunID != "" ||
		got.Method != MethodTrailer ||
		got.MatchedBy != "trailer" ||
		got.Confidence != cfg.TrailerConfidence ||
		!reflect.DeepEqual(got.TrailerAgents, agents) {
		t.Fatalf("TrailerEdge() attribution fields = %+v", got)
	}
}
