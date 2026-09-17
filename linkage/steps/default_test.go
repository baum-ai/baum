package steps

import (
	"slices"
	"testing"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

// Sanity: the default step registry order and ids match linkage.DefaultMatchSteps.
func TestDefaultStepsOrderAndIDs(t *testing.T) {
	steps := Default()
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID()
	}

	want := linkage.DefaultMatchSteps
	if len(ids) != len(want) {
		t.Fatalf("Default() has %d steps, want %d: %v", len(ids), len(want), ids)
	}
	for i, id := range ids {
		if id != want[i] {
			t.Fatalf("Default()[%d].ID() = %q, want %q (full order: %v)", i, id, want[i], ids)
		}
	}
}

// linkage.Config.CandidatePrefilterSound exempts coauthor_agent, user_email and
// branch from having to run after timeframe: they never finalize, and they
// judge each candidate on that candidate's own evidence, so running them
// against the prefiltered set produces exactly the prefiltered part of what a
// full scan would produce. If one of them learns to finalize, or to rank
// candidates against each other, that exemption becomes a silently dropped
// match under an ablation. The property is pinned here because the package
// holding the exemption cannot import these steps.
func TestPrefilterExemptStepsAreNonFinalizingPerCandidateFilters(t *testing.T) {
	const sha = "9f2c4b7a1e6d8c0b3a5f7e9d1c3b5a7f9e1d3c5b"
	commit := linkage.EnrichedCommit{
		Commit: linkage.Commit{
			OID: sha, CommittedAt: "2026-07-26T12:00:00Z", AuthorEmail: "dev@example.com",
		},
		CoauthoredAgents: []string{"claude"},
		BranchNames:      []string{"main"},
		FilesTouched:     []string{"parse.go"},
	}
	candidates := []linkage.RunCandidate{
		{
			RunID: "run-1", Agent: "claude_code", UserEmail: "dev@example.com",
			Branches: []string{"main"}, FilesTouched: []string{"parse.go"},
			FilesEdited: []string{"parse.go"},
			CommitRefs:  []linkage.CommitRef{{SHA: sha, Role: linkage.RoleCreated}},
		},
		{RunID: "run-2", Agent: "codex", UserEmail: "other@example.com", Branches: []string{"topic"}},
		{RunID: "run-3"},
	}
	cfg := linkage.DefaultConfig()

	for _, step := range []linkage.Step{CoauthorAgentStep{}, UserEmailStep{}, BranchStep{}} {
		full := step.Apply(commit, candidates, cfg)
		if full.IsFinal() {
			t.Fatalf("%s finalized an edge; it is exempt from the prefilter's ordering rule", step.ID())
		}
		for _, dropped := range candidates {
			subset := make([]linkage.RunCandidate, 0, len(candidates)-1)
			var want []string
			for _, candidate := range candidates {
				if candidate.RunID == dropped.RunID {
					continue
				}
				subset = append(subset, candidate)
			}
			for _, kept := range full.Candidates {
				if kept.RunID != dropped.RunID {
					want = append(want, kept.RunID)
				}
			}
			result := step.Apply(commit, subset, cfg)
			if result.IsFinal() {
				t.Fatalf("%s finalized an edge without %s in its input", step.ID(), dropped.RunID)
			}
			var got []string
			for _, kept := range result.Candidates {
				got = append(got, kept.RunID)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s without %s kept %v, want %v: its verdict depends on the other candidates",
					step.ID(), dropped.RunID, got, want)
			}
		}
	}
}
