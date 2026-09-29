package steps

import (
	"testing"

	"github.com/lastpersonlabs/baum/linkage"
)

func TestCoauthorEmptyIsNoop(t *testing.T) {
	step := CoauthorAgentStep{}
	commit := linkage.EnrichedCommit{}
	candidates := []linkage.RunCandidate{
		{RunID: "a", Agent: "claude_code", Source: "upload"},
		{RunID: "b", Agent: "codex", Source: "upload"},
	}
	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Candidates) != len(candidates) {
		t.Fatalf("candidates = %+v, want unchanged %+v", result.Candidates, candidates)
	}
}

func TestCoauthorFiltersToMatchingAgents(t *testing.T) {
	step := CoauthorAgentStep{}
	commit := linkage.EnrichedCommit{CoauthoredAgents: []string{"claude", "cursor"}}
	claude := linkage.RunCandidate{RunID: "a", Agent: "claude_code", Source: "upload"}
	codex := linkage.RunCandidate{RunID: "b", Agent: "codex", Source: "upload"}
	cursor := linkage.RunCandidate{RunID: "c", Agent: "cursor", Source: "entire_checkpoint"}

	result := step.Apply(commit, []linkage.RunCandidate{claude, codex, cursor}, linkage.DefaultConfig())
	if len(result.Candidates) != 2 || result.Candidates[0].RunID != "a" || result.Candidates[1].RunID != "c" {
		t.Fatalf("candidates = %+v, want [a, c]", result.Candidates)
	}
}

// The candidate's vendor decides, not how the tree arrived. Reading the source
// instead treated every Entire-carried session as agent-agnostic, so a Cursor
// session was as compatible with a Claude trailer as Claude's own.
func TestCoauthorMatchesTheAgentNotTheSource(t *testing.T) {
	step := CoauthorAgentStep{}
	commit := linkage.EnrichedCommit{CoauthoredAgents: []string{"claude"}}
	candidates := []linkage.RunCandidate{
		{RunID: "a", Agent: "claude_code", Source: "entire_checkpoint"},
		{RunID: "b", Agent: "cursor", Source: "entire_checkpoint"},
	}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Candidates) != 1 || result.Candidates[0].RunID != "a" {
		t.Fatalf("candidates = %+v, want only the Claude session", result.Candidates)
	}
}

// A candidate with no vendor at all falls back to its source, and every source
// value names a recorder rather than an agent. Excluding on that non-comparison
// would make such a candidate permanently unmatchable.
func TestCoauthorKeepsAgentAgnosticCandidates(t *testing.T) {
	step := CoauthorAgentStep{}
	commit := linkage.EnrichedCommit{CoauthoredAgents: []string{"claude"}}
	candidates := []linkage.RunCandidate{
		{RunID: "a", Source: "entire_checkpoint"},
		{RunID: "b", Agent: "codex", Source: "upload"},
		{RunID: "c", Source: "upload"},
	}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Candidates) != 2 || result.Candidates[0].RunID != "a" || result.Candidates[1].RunID != "c" {
		t.Fatalf("candidates = %+v, want [a, c]", result.Candidates)
	}
}

func TestCoauthorReportsAgentMismatchWhenItEmptiesTheSet(t *testing.T) {
	step := CoauthorAgentStep{}
	commit := linkage.EnrichedCommit{CoauthoredAgents: []string{"claude"}}
	candidates := []linkage.RunCandidate{{RunID: "a", Agent: "codex", Source: "upload"}}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Candidates) != 0 || result.Reason != linkage.ReasonAgentMismatch {
		t.Fatalf("result = %+v, want no candidates and reason %q", result, linkage.ReasonAgentMismatch)
	}
}
