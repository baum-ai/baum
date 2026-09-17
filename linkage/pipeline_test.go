package linkage

import (
	"strings"
	"testing"
)

// recordingStep appends its id to calls every time Apply runs, then passes
// candidates through unchanged.
type recordingStep struct {
	id    string
	calls *[]string
}

func (s recordingStep) ID() string { return s.id }
func (s recordingStep) Apply(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult {
	*s.calls = append(*s.calls, s.id)
	return Filtered(candidates)
}

// fakeStep is a passthrough Step stand-in for tests that only need a
// registered id, not real matching behavior.
type fakeStep struct{ id string }

func (f fakeStep) ID() string { return f.id }
func (f fakeStep) Apply(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult {
	return Filtered(candidates)
}

func TestMatchCommitExcludesMerges(t *testing.T) {
	commit := EnrichedCommit{Commit: Commit{OID: "deadbeef"}, IsMerge: true}
	cfg := DefaultConfig()

	edges, err := MatchCommit(commit, []RunCandidate{{RunID: "run-1"}}, cfg, nil)
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != StatusExcludedMerge || edge.Reason != ReasonMergeCommit || edge.CommitSHA != "deadbeef" {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.CandidatesRemaining != 0 || edge.RunID != "" {
		t.Fatalf("excluded_merge edge should carry no run/candidates: %+v", edge)
	}
}

func TestMatchCommitDoesNotExcludeMergesWhenDisabled(t *testing.T) {
	commit := EnrichedCommit{Commit: Commit{OID: "deadbeef"}, IsMerge: true}
	cfg := DefaultConfig()
	cfg.ExcludeMergeCommits = false
	cfg.EnabledMatchSteps = nil

	edges, err := MatchCommit(commit, []RunCandidate{{RunID: "run-1"}}, cfg, nil)
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status == StatusExcludedMerge {
		t.Fatalf("expected merge exclusion to be skipped, got %+v", edge)
	}
}

func TestMatchCommitHardLinkStopsTheChain(t *testing.T) {
	var calls []string
	unreached := fakeStep{id: "unreached"}
	linkStep := stepFunc{id: "link", fn: func(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult {
		calls = append(calls, "link")
		return Final(Edge{CommitSHA: commit.OID, RunID: "run-1", Status: StatusMatched, MatchedBy: "link"})
	}}

	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"link", "unreached"}
	edges, err := MatchCommit(enrichedCommit("deadbeef"), []RunCandidate{{RunID: "run-1"}}, cfg, []Step{linkStep, unreached})
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != StatusMatched || edge.RunID != "run-1" || edge.MatchedBy != "link" {
		t.Fatalf("edge = %+v", edge)
	}
	if len(calls) != 1 || calls[0] != "link" {
		t.Fatalf("calls = %v, want only [link] (chain should stop on a final edge)", calls)
	}
}

func TestMatchCommitUnmatchedReasons(t *testing.T) {
	t.Run("no candidates at all", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.EnabledMatchSteps = nil
		edges, err := MatchCommit(enrichedCommit("deadbeef"), nil, cfg, nil)
		if err != nil {
			t.Fatalf("MatchCommit: %v", err)
		}
		edge := edges[0]
		if edge.Status != StatusUnmatched || edge.Reason != ReasonNoCandidates {
			t.Fatalf("edge = %+v, want reason %q", edge, ReasonNoCandidates)
		}
	})

	t.Run("candidates remain after files_overlap", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.EnabledMatchSteps = []string{"files_overlap"}
		passthrough := fakeStep{id: "files_overlap"}
		edges, err := MatchCommit(enrichedCommit("deadbeef"), []RunCandidate{{RunID: "run-1"}}, cfg, []Step{passthrough})
		if err != nil {
			t.Fatalf("MatchCommit: %v", err)
		}
		edge := edges[0]
		if edge.Status != StatusUnmatched || edge.Reason != ReasonNoFileOverlap {
			t.Fatalf("edge = %+v, want reason %q", edge, ReasonNoFileOverlap)
		}
	})

	t.Run("candidates remain after a non-terminal step", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.EnabledMatchSteps = []string{"timeframe"}
		passthrough := fakeStep{id: "timeframe"}
		edges, err := MatchCommit(enrichedCommit("deadbeef"), []RunCandidate{{RunID: "run-1"}}, cfg, []Step{passthrough})
		if err != nil {
			t.Fatalf("MatchCommit: %v", err)
		}
		edge := edges[0]
		if edge.Status != StatusUnmatched || edge.Reason != "no_step_finalized_match" {
			t.Fatalf("edge = %+v", edge)
		}
	})
}

func TestMatchCommitAppliesOnlyEnabledStepsInConfigOrder(t *testing.T) {
	var calls []string
	first := recordingStep{id: "first", calls: &calls}
	second := recordingStep{id: "second", calls: &calls}

	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"second", "first"}
	edges, err := MatchCommit(enrichedCommit("deadbeef"), []RunCandidate{{RunID: "run-1"}}, cfg, []Step{first, second})
	if err != nil {
		t.Fatalf("MatchCommit: %v", err)
	}
	edge := edges[0]
	if edge.Status != StatusUnmatched {
		t.Fatalf("edge = %+v", edge)
	}
	if strings.Join(calls, ",") != "second,first" {
		t.Fatalf("calls = %v, want [second, first] (config order, not registry order)", calls)
	}
}

func TestMatchCommitRejectsUnknownEnabledStep(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"missing"}
	if _, err := MatchCommit(EnrichedCommit{}, nil, cfg, []Step{fakeStep{id: "first"}}); err == nil {
		t.Fatal("expected an error for an unknown enabled step id")
	}
}

func TestMatchCommitRejectsDuplicateEnabledStep(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"first", "first"}
	if _, err := MatchCommit(EnrichedCommit{}, nil, cfg, []Step{fakeStep{id: "first"}}); err == nil {
		t.Fatal("expected an error for a duplicate enabled step id")
	}
}

func TestMatchCommitRejectsDuplicateAvailableStep(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"first"}
	if _, err := MatchCommit(EnrichedCommit{}, nil, cfg, []Step{fakeStep{id: "first"}, fakeStep{id: "first"}}); err == nil {
		t.Fatal("expected an error for a duplicate available step id")
	}
}

func TestMatchCommitsPreservesCommitOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = nil
	commits := []EnrichedCommit{enrichedCommit("sha-1"), enrichedCommit("sha-2"), enrichedCommit("sha-3")}

	edges, err := MatchCommits(commits, nil, cfg, nil)
	if err != nil {
		t.Fatalf("MatchCommits: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("len(edges) = %d, want 3", len(edges))
	}
	for i, want := range []string{"sha-1", "sha-2", "sha-3"} {
		if edges[i].CommitSHA != want {
			t.Fatalf("edges[%d].CommitSHA = %q, want %q", i, edges[i].CommitSHA, want)
		}
	}
}

func TestMatchCommitsFlattensFinalEdgesInCommitOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnabledMatchSteps = []string{"multi"}
	multi := stepFunc{id: "multi", fn: func(commit EnrichedCommit, _ []RunCandidate, _ Config) StepResult {
		edges := []Edge{{CommitSHA: commit.OID, RunID: commit.OID + "-a", Status: StatusMatched}}
		if commit.OID == "sha-1" {
			edges = append(edges, Edge{CommitSHA: commit.OID, RunID: commit.OID + "-b", Status: StatusMatched})
		}
		return FinalEdges(edges)
	}}

	edges, err := MatchCommits(
		[]EnrichedCommit{enrichedCommit("sha-1"), enrichedCommit("sha-2")},
		nil,
		cfg,
		[]Step{multi},
	)
	if err != nil {
		t.Fatalf("MatchCommits: %v", err)
	}
	if len(edges) != 3 ||
		edges[0].RunID != "sha-1-a" ||
		edges[1].RunID != "sha-1-b" ||
		edges[2].RunID != "sha-2-a" {
		t.Fatalf("edges = %+v", edges)
	}
}

func enrichedCommit(oid string) EnrichedCommit {
	return EnrichedCommit{Commit: Commit{OID: oid}}
}

// stepFunc adapts a plain function to the Step interface for tests that
// need a final edge without a full concrete step implementation.
type stepFunc struct {
	id string
	fn func(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult
}

func (s stepFunc) ID() string { return s.id }
func (s stepFunc) Apply(commit EnrichedCommit, candidates []RunCandidate, cfg Config) StepResult {
	return s.fn(commit, candidates, cfg)
}
