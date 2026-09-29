// End-to-end tests of linkage.MatchCommit/MatchCommits wired to the real
// Default() step registry.
package steps_test

import (
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/linkage"
	"github.com/lastpersonlabs/baum/linkage/steps"
)

func TestMatchCommitsExcludesMergesAndPreservesOrder(t *testing.T) {
	committedAt, err := time.Parse(time.RFC3339, "2024-06-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	startedAt, err := time.Parse(time.RFC3339, "2024-06-01T11:50:00Z")
	if err != nil {
		t.Fatal(err)
	}
	endedAt, err := time.Parse(time.RFC3339, "2024-06-01T12:10:00Z")
	if err != nil {
		t.Fatal(err)
	}

	merge := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "merge-sha"}, IsMerge: true}
	matchedSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	matched := linkage.EnrichedCommit{Commit: linkage.Commit{OID: matchedSHA, CommittedAt: committedAt.Format(time.RFC3339)}}
	unmatched := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CommittedAt: committedAt.Format(time.RFC3339)}, FilesTouched: []string{"src/unrelated.py"}}

	candidates := []linkage.RunCandidate{
		{
			RunID:        "run-1",
			StartedAt:    &startedAt,
			EndedAt:      &endedAt,
			CommitRefs:   []linkage.CommitRef{{SHA: matchedSHA, Role: linkage.RoleCreated}},
			FilesTouched: []string{"src/a.py"},
		},
	}

	cfg := linkage.DefaultConfig()
	edges, err := linkage.MatchCommits([]linkage.EnrichedCommit{merge, matched, unmatched}, candidates, cfg, steps.Default())
	if err != nil {
		t.Fatalf("MatchCommits: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("len(edges) = %d, want 3", len(edges))
	}

	if edges[0].CommitSHA != merge.OID || edges[0].Status != linkage.StatusExcludedMerge {
		t.Fatalf("edges[0] = %+v", edges[0])
	}
	if edges[1].CommitSHA != matched.OID || edges[1].Status != linkage.StatusMatched || edges[1].RunID != "run-1" || edges[1].MatchedBy != "commit_ref" {
		t.Fatalf("edges[1] = %+v", edges[1])
	}
	if edges[2].CommitSHA != unmatched.OID || edges[2].Status != linkage.StatusUnmatched || edges[2].Reason != linkage.ReasonNoFileOverlap {
		t.Fatalf("edges[2] = %+v", edges[2])
	}
}

// A session that recorded creating this exact SHA must link even when every
// soft filter would have dropped it: no timestamps for timeframe, a vendor
// that matches no Co-Authored-By agent, a disagreeing branch, and no file
// overlap. Running those filters first made hard-linkable sessions unmatchable.
func TestMatchCommitHardLinksThroughSoftFilters(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commit := linkage.EnrichedCommit{
		Commit:           linkage.Commit{OID: sha, CommittedAt: "2024-06-01T12:00:00Z"},
		CoauthoredAgents: []string{"claude"},
		BranchNames:      []string{"main"},
		FilesTouched:     []string{"src/only-here.py"},
	}
	candidate := linkage.RunCandidate{
		RunID:      "run-1",
		Agent:      "cursor",
		Source:     "entire_checkpoint",
		Branches:   []string{"some-other-branch"},
		CommitRefs: []linkage.CommitRef{{SHA: sha, Role: linkage.RoleCreated}},
	}

	edges, err := linkage.MatchCommit(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig(), steps.Default())
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != linkage.StatusMatched || edge.RunID != "run-1" || edge.MatchedBy != "commit_ref" {
		t.Fatalf("edge = %+v, want a commit_ref match on run-1", edge)
	}
}

// An unmatched edge should name the filter that ruled every run out, not
// report the generic "no candidates" that hides which step was responsible.
func TestMatchCommitReportsTheFilterThatEmptiedTheSet(t *testing.T) {
	commit := linkage.EnrichedCommit{
		Commit:           linkage.Commit{OID: "deadbeef", CommittedAt: "2024-06-01T12:00:00Z"},
		CoauthoredAgents: []string{"claude"},
	}
	startedAt, err := time.Parse(time.RFC3339, "2024-06-01T11:50:00Z")
	if err != nil {
		t.Fatal(err)
	}
	candidate := linkage.RunCandidate{RunID: "run-1", Agent: "codex", Source: "upload", StartedAt: &startedAt}

	edges, err := linkage.MatchCommit(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig(), steps.Default())
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != linkage.StatusUnmatched || edge.Reason != linkage.ReasonAgentMismatch {
		t.Fatalf("edge = %+v, want unmatched with reason %q", edge, linkage.ReasonAgentMismatch)
	}
}

// An empty candidate set is "no candidates", whatever the commit says about who
// wrote it. The matcher cannot tell a repository nobody has uploaded to from one
// whose sessions the SQL prefilter left behind, so it must not guess: the
// resolver makes that call, against the repository.
func TestMatchCommitDoesNotGuessWhyThereAreNoCandidates(t *testing.T) {
	signed := linkage.EnrichedCommit{
		Commit:           linkage.Commit{OID: "deadbeef", CommittedAt: "2024-06-01T12:00:00Z"},
		CoauthoredAgents: []string{"claude"},
	}

	edges, err := linkage.MatchCommit(signed, nil, linkage.DefaultConfig(), steps.Default())
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %+v, want one unmatched outcome", edges)
	}
	edge := edges[0]
	if edge.Status != linkage.StatusUnmatched || edge.Reason != linkage.ReasonNoCandidates {
		t.Fatalf("edge = %+v, want unmatched with reason %q", edge, linkage.ReasonNoCandidates)
	}
}

func TestMatchCommitUsesConfiguredTimeframeTolerance(t *testing.T) {
	committedAt, err := time.Parse(time.RFC3339, "2024-06-01T12:10:00Z")
	if err != nil {
		t.Fatal(err)
	}
	startedAt, err := time.Parse(time.RFC3339, "2024-06-01T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	endedAt, err := time.Parse(time.RFC3339, "2024-06-01T12:05:00Z")
	if err != nil {
		t.Fatal(err)
	}

	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "deadbeef", CommittedAt: committedAt.Format(time.RFC3339)}, FilesTouched: []string{"src/a.py", "src/b.py"}}
	candidate := linkage.RunCandidate{
		RunID:        "run-1",
		StartedAt:    &startedAt,
		EndedAt:      &endedAt,
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 10 * time.Minute
	edges, err := linkage.MatchCommit(commit, []linkage.RunCandidate{candidate}, cfg, steps.Default())
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != linkage.StatusMatched || edge.MatchedBy != "files_overlap" {
		t.Fatalf("edge = %+v, want a files_overlap match", edge)
	}
}
