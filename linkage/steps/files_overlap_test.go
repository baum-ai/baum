package steps

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/linkage"
)

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestContainment(t *testing.T) {
	tests := []struct {
		name             string
		touched, edited  []string
		commit           []string
		wantScore        float64
		wantIntersection int
	}{
		{"identical edit", []string{"a.py"}, []string{"a.py"}, []string{"a.py"}, 1.0, 1},
		{"read only", []string{"a.py"}, nil, []string{"a.py"}, 0.25, 0},
		{"mixed edit and read", []string{"a.py", "b.py"}, []string{"a.py"}, []string{"a.py", "b.py"}, 0.625, 1},
		{"commit fully inside a much larger run", []string{"a.py", "b.py", "c.py", "d.py"}, []string{"a.py", "b.py", "c.py", "d.py"}, []string{"a.py"}, 1.0, 1},
		{"cwd-relative edit", []string{"src/agent/mod.rs"}, []string{"src/agent/mod.rs"}, []string{"src-tauri/src/agent/mod.rs"}, 1.0, 1},
		{"cwd-relative read", []string{"dockercli/dockercli.go"}, nil, []string{"platform/internal/recountablectl/dockercli/dockercli.go"}, 0.25, 0},
		{"partial component is not a match", []string{"gent/mod.rs"}, []string{"gent/mod.rs"}, []string{"src-tauri/src/agent/mod.rs"}, 0.0, 0},
		{"disjoint", []string{"a.py"}, []string{"a.py"}, []string{"b.py"}, 0.0, 0},
		{"empty run", nil, nil, []string{"a.py"}, 0.0, 0},
		{"empty commit", []string{"a.py"}, []string{"a.py"}, nil, 0.0, 0},
		{"both empty", nil, nil, nil, 0.0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, intersection := containment(tt.touched, tt.edited, toSet(tt.commit))
			if !approxEqual(score, tt.wantScore) {
				t.Errorf("containment(%v, %v, %v) score = %v, want %v", tt.touched, tt.edited, tt.commit, score, tt.wantScore)
			}
			if intersection != tt.wantIntersection {
				t.Errorf("containment(%v, %v, %v) intersection = %d, want %d", tt.touched, tt.edited, tt.commit, intersection, tt.wantIntersection)
			}
		})
	}
}

func TestContainmentRejectsAmbiguousSuffixFallback(t *testing.T) {
	commit := toSet([]string{"ui/routes/index.ts", "ui/components/index.ts"})
	score, intersection := containment([]string{"index.ts"}, []string{"index.ts"}, commit)

	if !approxEqual(score, 0) || intersection != 0 {
		t.Fatalf("containment score = %v, intersection = %d; want 0, 0", score, intersection)
	}
}

func TestContainmentAcceptsSuffixUniqueAfterExactMatches(t *testing.T) {
	commit := toSet([]string{"ui/routes/index.ts", "index.ts"})
	score, intersection := containment(
		[]string{"index.ts", "routes/index.ts"},
		[]string{"index.ts", "routes/index.ts"},
		commit,
	)

	if !approxEqual(score, 1) || intersection != 2 {
		t.Fatalf("containment score = %v, intersection = %d; want 1, 2", score, intersection)
	}
}

func TestContainmentPrefersExactPathsBeforeSuffixFallback(t *testing.T) {
	commit := toSet([]string{"index.ts", "ui/index.ts"})
	score, intersection := containment([]string{"index.ts"}, []string{"index.ts"}, commit)

	if !approxEqual(score, 0.5) || intersection != 1 {
		t.Fatalf("containment score = %v, intersection = %d; want 0.5, 1", score, intersection)
	}
}

func TestFilesOverlapLinksTopScore(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"src/a.py", "src/b.py"}}
	best := linkage.RunCandidate{RunID: "best", FilesTouched: []string{"src/a.py", "src/b.py"}, FilesEdited: []string{"src/a.py", "src/b.py"}}
	weak := linkage.RunCandidate{RunID: "weak", FilesTouched: []string{"src/a.py"}, FilesEdited: []string{"src/a.py"}}

	result := step.Apply(commit, []linkage.RunCandidate{weak, best}, linkage.DefaultConfig())
	if !result.IsFinal() {
		t.Fatal("expected a final edge")
	}
	if len(result.Edges) != 1 {
		t.Fatalf("edges = %+v, want one", result.Edges)
	}
	edge := result.Edges[0]
	if edge.RunID != "best" || edge.MatchedBy != "files_overlap" {
		t.Fatalf("edge = %+v", edge)
	}
	if !edge.ScoreValid || !approxEqual(edge.Score, 1.0) {
		t.Fatalf("edge score = %v (valid=%v), want 1.0", edge.Score, edge.ScoreValid)
	}
	if edge.CandidatesRemaining != 2 {
		t.Fatalf("CandidatesRemaining = %d, want 2", edge.CandidatesRemaining)
	}
	wantMax := linkage.DefaultConfig().FilesOverlapMaxConfidence
	if edge.Confidence != wantMax {
		t.Fatalf("Confidence = %v, want clamped max %v", edge.Confidence, wantMax)
	}
	if edge.Tie != nil {
		t.Fatalf("Tie = %+v, want none", edge.Tie)
	}
}

func TestFilesOverlapLinksEveryExactlyTiedCandidate(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"src/a.py", "src/b.py"}}
	z := linkage.RunCandidate{RunID: "run-z", FilesTouched: []string{"src/a.py", "src/b.py"}, FilesEdited: []string{"src/a.py", "src/b.py"}}
	a := linkage.RunCandidate{RunID: "run-a", FilesTouched: []string{"src/a.py", "src/b.py"}, FilesEdited: []string{"src/a.py", "src/b.py"}}

	cfg := linkage.DefaultConfig()
	wantConfidence := cfg.FilesOverlapMaxConfidence
	for iteration := 0; iteration < 10; iteration++ {
		result := step.Apply(commit, []linkage.RunCandidate{z, a}, cfg)
		if len(result.Edges) != 2 {
			t.Fatalf("iteration %d edges = %+v, want two", iteration, result.Edges)
		}
		for i, want := range []struct {
			runID  string
			peerID string
		}{
			{runID: "run-a", peerID: "run-z"},
			{runID: "run-z", peerID: "run-a"},
		} {
			edge := result.Edges[i]
			if edge.RunID != want.runID || !approxEqual(edge.Confidence, wantConfidence) {
				t.Fatalf("iteration %d edges[%d] = %+v, want run %s at confidence %v", iteration, i, edge, want.runID, wantConfidence)
			}
			if edge.Tie == nil || edge.Tie.Resolution != linkage.TieResolutionLinkedAll ||
				len(edge.Tie.Peers) != 1 || edge.Tie.Peers[0].RunID != want.peerID ||
				!approxEqual(edge.Tie.Peers[0].Score, 1) {
				t.Fatalf("iteration %d edges[%d].Tie = %+v, want linked_all peer %s at score 1", iteration, i, edge.Tie, want.peerID)
			}
		}
	}
}

func TestFilesOverlapLinksNWayTieInRunIDOrder(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a", "b"}}
	candidates := []linkage.RunCandidate{
		{RunID: "run-c", FilesTouched: []string{"a", "b"}, FilesEdited: []string{"a", "b"}},
		{RunID: "run-a", FilesTouched: []string{"a", "b"}, FilesEdited: []string{"a", "b"}},
		{RunID: "run-b", FilesTouched: []string{"a", "b"}, FilesEdited: []string{"a", "b"}},
	}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Edges) != 3 {
		t.Fatalf("edges = %+v, want three", result.Edges)
	}
	for i, want := range []string{"run-a", "run-b", "run-c"} {
		edge := result.Edges[i]
		if edge.RunID != want || edge.Tie == nil || len(edge.Tie.Peers) != 2 {
			t.Fatalf("edges[%d] = %+v, want %s with two peers", i, edge, want)
		}
	}
}

func TestFilesOverlapExcludesTiedPeerBelowIntersection(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a", "b", "c", "d", "e"}}
	admitted := linkage.RunCandidate{
		RunID: "run-a", FilesTouched: []string{"a", "b"}, FilesEdited: []string{"a", "b"},
	}
	rejected := linkage.RunCandidate{
		RunID:        "run-z",
		FilesTouched: []string{"a", "b", "c", "d", "e"},
		FilesEdited:  []string{"a"},
	}
	cfg := linkage.DefaultConfig()
	cfg.FilesOverlapMinScore = 0.4

	result := step.Apply(commit, []linkage.RunCandidate{rejected, admitted}, cfg)
	if len(result.Edges) != 1 || result.Edges[0].RunID != "run-a" {
		t.Fatalf("edges = %+v, want only run-a", result.Edges)
	}
	if result.Edges[0].Tie != nil {
		t.Fatalf("Tie = %+v, want the ineligible candidate excluded", result.Edges[0].Tie)
	}
}

// A tied peer that sorts first on RunID but fails a gate the other clears is the
// exact shape of the veto: same evidence, opposite outcome, decided by an id.
func TestFilesOverlapIneligibleFirstCandidateDoesNotVetoTheStep(t *testing.T) {
	step := FilesOverlapStep{}
	files := make([]string, 20)
	for i := range files {
		files[i] = fmt.Sprintf("f%02d", i)
	}
	commit := linkage.EnrichedCommit{FilesTouched: files}
	// Both score 0.2: four reads trade for one edit. Only the second clears the
	// broad-commit fallback, which wants four edited files.
	reader := linkage.RunCandidate{
		RunID:        "run-aaa",
		FilesTouched: []string{"f00", "f01", "f02", "f03", "f04", "f05", "f06"},
		FilesEdited:  []string{"f00", "f01", "f02"},
	}
	editor := linkage.RunCandidate{
		RunID:        "run-zzz",
		FilesTouched: []string{"f10", "f11", "f12", "f13"},
		FilesEdited:  []string{"f10", "f11", "f12", "f13"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{reader, editor}, linkage.DefaultConfig())
	if len(result.Edges) != 1 || result.Edges[0].RunID != "run-zzz" {
		t.Fatalf("edges = %+v, want only run-zzz", result.Edges)
	}
}

func TestExplainedFilesPairsLikeTheScore(t *testing.T) {
	commit := linkage.EnrichedCommit{FilesTouched: []string{
		"src/a.py", "src/b.py", "src-tauri/src/agent/mod.rs", "src/dup/index.ts",
	}}
	candidate := linkage.RunCandidate{
		// One exact spelling, one recovered by the suffix fallback, and one read
		// that explains nothing because nobody changed it.
		FilesEdited:  []string{"src/a.py", "agent/mod.rs"},
		FilesTouched: []string{"src/a.py", "agent/mod.rs", "src/b.py"},
	}

	explained := toSet(ExplainedFiles(commit, candidate))
	if len(explained) != 2 || !explained["src/a.py"] || !explained["src-tauri/src/agent/mod.rs"] {
		t.Fatalf("ExplainedFiles = %v, want the edited exact and suffix matches only", explained)
	}
}

func TestExplainedFilesCountsOneCommitPathPerSpelling(t *testing.T) {
	commit := linkage.EnrichedCommit{FilesTouched: []string{"ui/index.ts", "api/index.ts"}}
	// An ambiguous short spelling accounts for neither, the same rule the score
	// keeps, so a session like this never makes a commit look explained.
	candidate := linkage.RunCandidate{FilesEdited: []string{"index.ts"}}

	if explained := ExplainedFiles(commit, candidate); len(explained) != 0 {
		t.Fatalf("ExplainedFiles = %v, want nothing from an ambiguous suffix", explained)
	}
}

func TestFilesOverlapBreaksTiesOnTheRunEndingNearestTheCommit(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2026-08-31T12:00:00Z"},
		FilesTouched: []string{"a", "b"},
	}
	tied := func(runID, endedAt string) linkage.RunCandidate {
		ended, err := time.Parse(time.RFC3339, endedAt)
		if err != nil {
			t.Fatal(err)
		}
		return linkage.RunCandidate{
			RunID: runID, EndedAt: &ended,
			FilesTouched: []string{"a", "b"}, FilesEdited: []string{"a", "b"},
		}
	}
	// run-aaa wins on RunID and loses on time, so ordering by id would invert this.
	far := tied("run-aaa", "2026-08-31T09:00:00Z")
	near := tied("run-zzz", "2026-08-31T11:50:00Z")

	result := step.Apply(commit, []linkage.RunCandidate{far, near}, linkage.DefaultConfig())
	if len(result.Edges) != 2 || result.Edges[0].RunID != "run-zzz" {
		t.Fatalf("edges = %+v, want the nearest run first", result.Edges)
	}
}

func TestFilesOverlapRejectsBelowMinScore(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a.py", "b.py", "c.py", "d.py", "e.py", "f.py"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a.py", "b.py", "x.py"}, FilesEdited: []string{"a.py", "b.py", "x.py"}}

	cfg := linkage.DefaultConfig()
	cfg.FilesOverlapMinScore = 0.5
	cfg.FilesOverlapMinIntersection = 2
	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	if result.IsFinal() {
		t.Fatal("expected passthrough below min score")
	}
	if len(result.Candidates) != 1 || result.Candidates[0].RunID != "run-1" {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
}

func TestFilesOverlapMatchesBroadCommitFallback(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a", "b", "c", "d"}, FilesEdited: []string{"a", "b", "c", "d"}}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	if !result.IsFinal() {
		t.Fatalf("four edited files at 0.4 containment were rejected; candidates = %+v", result.Candidates)
	}
}

func TestFilesOverlapBroadCommitFallbackRequiresFourEdits(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a", "b", "c"}, FilesEdited: []string{"a", "b", "c"}}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatalf("three edited files matched through broad-commit fallback: %+v", result.Edges)
	}
}

func TestFilesOverlapBroadCommitFallbackRequiresMinScore(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a", "b", "c", "d"}, FilesEdited: []string{"a", "b", "c", "d"}}
	cfg := linkage.DefaultConfig()
	cfg.FilesOverlapFallbackScore = 0.5

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	if result.IsFinal() {
		t.Fatalf("fallback matched below its configured score: %+v", result.Edges)
	}
}

func TestFilesOverlapRejectsReadOnlyOverlap(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a.py", "b.py"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a.py", "b.py"}}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatalf("read-only overlap matched with edges %+v", result.Edges)
	}
}

// FilesOverlapMinScore is documented as the minimum score the step "will
// accept", and the sibling intersection gate treats its minimum inclusively,
// so a score landing exactly on it has to match.
func TestFilesOverlapAcceptsScoreExactlyAtMinScore(t *testing.T) {
	step := FilesOverlapStep{}
	// 3 of the commit's 5 files = exactly 0.6, the shipped default minimum.
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a.py", "b.py", "c.py", "d.py", "e.py"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a.py", "b.py", "c.py"}, FilesEdited: []string{"a.py", "b.py", "c.py"}}

	cfg := linkage.DefaultConfig()
	if cfg.FilesOverlapMinScore != 0.6 {
		t.Fatalf("test assumes a 0.6 default min score, got %v", cfg.FilesOverlapMinScore)
	}
	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	if !result.IsFinal() {
		t.Fatalf("score exactly at the minimum was rejected; candidates = %+v", result.Candidates)
	}
	if !approxEqual(result.Edges[0].Score, cfg.FilesOverlapMinScore) {
		t.Fatalf("edge score = %v, want exactly %v", result.Edges[0].Score, cfg.FilesOverlapMinScore)
	}
}

func TestFilesOverlapRejectsBelowMinIntersection(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"a.py", "b.py", "c.py", "d.py"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"a.py", "b.py"}, FilesEdited: []string{"a.py"}}

	cfg := linkage.DefaultConfig()
	cfg.FilesOverlapMinScore = 0 // isolate the intersection gate
	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	if result.IsFinal() {
		t.Fatal("expected passthrough below min intersection (default requires 2)")
	}
}

// A one-file commit cannot supply two shared files, so the floor is capped.
func TestFilesOverlapMatchesSingleFileCommit(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"src/a.py"}}
	candidate := linkage.RunCandidate{RunID: "run-1", FilesTouched: []string{"src/a.py", "src/b.py"}, FilesEdited: []string{"src/a.py", "src/b.py"}}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	if !result.IsFinal() {
		t.Fatalf("single-file commit was left unmatched; candidates = %+v", result.Candidates)
	}
	if !approxEqual(result.Edges[0].Score, 1.0) {
		t.Fatalf("edge score = %v, want 1.0", result.Edges[0].Score)
	}
}

// A zero-file denominator must not become a match at any threshold.
func TestFilesOverlapPassesThroughCommitWithNoFiles(t *testing.T) {
	step := FilesOverlapStep{}
	candidates := []linkage.RunCandidate{{RunID: "run-1", FilesTouched: []string{"src/a.py"}}}

	result := step.Apply(linkage.EnrichedCommit{}, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatalf("expected passthrough for a commit with no files, got edges %+v", result.Edges)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the input preserved", result.Candidates)
	}
}

func TestFilesOverlapLinksHigherScoreWithinMargin(t *testing.T) {
	step := FilesOverlapStep{}
	commitFiles := []string{
		"01", "02", "03", "04", "05", "06", "07", "08",
		"09", "10", "11", "12", "13", "14", "15", "16",
	}
	commit := linkage.EnrichedCommit{FilesTouched: commitFiles}
	top := linkage.RunCandidate{RunID: "top", FilesTouched: commitFiles, FilesEdited: commitFiles}
	near := linkage.RunCandidate{
		RunID:        "near",
		FilesTouched: commitFiles,
		FilesEdited:  commitFiles[:15],
	}

	result := step.Apply(commit, []linkage.RunCandidate{top, near}, linkage.DefaultConfig())
	if len(result.Edges) != 1 || result.Edges[0].RunID != "top" {
		t.Fatalf("edges = %+v, want only top", result.Edges)
	}
	edge := result.Edges[0]
	if edge.Confidence != linkage.DefaultConfig().FilesOverlapMaxConfidence {
		t.Fatalf("Confidence = %v, want normal confidence", edge.Confidence)
	}
	if edge.Tie == nil || edge.Tie.Resolution != linkage.TieResolutionScoreOrder ||
		len(edge.Tie.Peers) != 1 || edge.Tie.Peers[0].RunID != "near" ||
		!approxEqual(edge.Tie.Peers[0].Score, 15.25/16.0) {
		t.Fatalf("Tie = %+v, want score_order with near runner-up", edge.Tie)
	}
}

func TestFilesOverlapNoPositiveScorePassthrough(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{FilesTouched: []string{"src/a.py", "src/b.py"}}
	candidates := []linkage.RunCandidate{{RunID: "x", FilesTouched: []string{"src/other.py"}}}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough: zero overlap")
	}
}

func TestFilesOverlapEmptyCandidates(t *testing.T) {
	step := FilesOverlapStep{}
	result := step.Apply(linkage.EnrichedCommit{}, nil, linkage.DefaultConfig())
	if result.IsFinal() {
		t.Fatal("expected passthrough for empty candidates")
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want empty", result.Candidates)
	}
}

func TestClampConfidence(t *testing.T) {
	def := linkage.DefaultConfig()
	tests := []struct {
		score float64
		want  float64
	}{
		{0.0, def.FilesOverlapMinConfidence},
		{0.03, def.FilesOverlapMinConfidence},
		{0.5, 0.5},
		{0.9, def.FilesOverlapMaxConfidence},
		{1.0, def.FilesOverlapMaxConfidence},
	}
	for _, tt := range tests {
		if got := clampConfidence(tt.score, def.FilesOverlapMinConfidence, def.FilesOverlapMaxConfidence); !approxEqual(got, tt.want) {
			t.Errorf("clampConfidence(%v) = %v, want %v", tt.score, got, tt.want)
		}
	}
}

// The gap discount is what lets the tolerance widen without calling a commit
// made a day later as certain as one made inside the session.
func TestFilesOverlapDiscountsConfidenceByCommitGap(t *testing.T) {
	step := FilesOverlapStep{}
	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 24 * time.Hour
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-06-02T12:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}

	tests := []struct {
		name           string
		endedAt        string
		wantConfidence float64
		wantGapSeconds float64
	}{
		{"inside the window", "2024-06-02T12:00:00Z", 0.75, 0},
		{"half a tolerance late", "2024-06-02T00:00:00Z", 0.5625, 43200},
		{"a full tolerance late", "2024-06-01T12:00:00Z", 0.375, 86400},
		{"past the tolerance", "2024-05-30T12:00:00Z", 0.375, 259200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := linkage.RunCandidate{
				RunID:        "run-a",
				EndedAt:      timePtr(t, tt.endedAt),
				FilesTouched: []string{"src/a.py", "src/b.py"},
				FilesEdited:  []string{"src/a.py", "src/b.py"},
			}

			result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
			if len(result.Edges) != 1 {
				t.Fatalf("edges = %+v, want one", result.Edges)
			}
			edge := result.Edges[0]
			if edge.RunID != "run-a" || !approxEqual(edge.Score, 1) {
				t.Fatalf("edge = %+v, want run-a still linked at score 1", edge)
			}
			if !approxEqual(edge.Confidence, tt.wantConfidence) {
				t.Fatalf("confidence = %v, want %v", edge.Confidence, tt.wantConfidence)
			}
			if tt.wantGapSeconds == 0 {
				if edge.TimeDiscount != nil {
					t.Fatalf("TimeDiscount = %+v, want none for a commit inside the window", edge.TimeDiscount)
				}
				return
			}
			if edge.TimeDiscount == nil || !approxEqual(edge.TimeDiscount.GapSeconds, tt.wantGapSeconds) ||
				edge.TimeDiscount.SyntheticEnd {
				t.Fatalf("TimeDiscount = %+v, want gap %v from a recorded end", edge.TimeDiscount, tt.wantGapSeconds)
			}
		})
	}
}

// A session with no recorded end is measured from its upload, and says so, so a
// reader can tell a real end from one the platform supplied.
func TestFilesOverlapMeasuresTheGapFromTheUploadWhenNoEndWasRecorded(t *testing.T) {
	step := FilesOverlapStep{}
	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 24 * time.Hour
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-06-02T12:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	candidate := linkage.RunCandidate{
		RunID:        "run-a",
		IngestedAt:   timePtr(t, "2024-06-02T00:00:00Z"),
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	edge := result.Edges[0]
	if edge.TimeDiscount == nil || !edge.TimeDiscount.SyntheticEnd ||
		!approxEqual(edge.TimeDiscount.GapSeconds, 43200) || !approxEqual(edge.Confidence, 0.5625) {
		t.Fatalf("edge = %+v, discount = %+v; want a synthetic end 12h before the commit", edge, edge.TimeDiscount)
	}
}

// A session open for days accounts for any commit in that span on the same file
// evidence as the hour that produced it, so the span itself has to cost it.
func TestFilesOverlapDiscountsLongWindows(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-06-03T12:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	candidate := linkage.RunCandidate{
		RunID:        "run-a",
		StartedAt:    timePtr(t, "2024-06-01T12:00:00Z"),
		EndedAt:      timePtr(t, "2024-06-03T12:00:00Z"),
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	edge := result.Edges[0]
	if edge.TimeDiscount == nil || !approxEqual(edge.TimeDiscount.WindowSeconds, 172800) ||
		!approxEqual(edge.TimeDiscount.Factor, 0.5) || !approxEqual(edge.Confidence, 0.375) {
		t.Fatalf("edge = %+v, discount = %+v; want a 48h window at half confidence", edge, edge.TimeDiscount)
	}
}

func TestFilesOverlapComposesGapAndWindowDiscounts(t *testing.T) {
	step := FilesOverlapStep{}
	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 24 * time.Hour
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-06-04T00:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	candidate := linkage.RunCandidate{
		RunID:        "run-a",
		StartedAt:    timePtr(t, "2024-06-01T12:00:00Z"),
		EndedAt:      timePtr(t, "2024-06-03T12:00:00Z"),
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	edge := result.Edges[0]
	// 12h past a 48h window: 0.75 for the gap, 0.5 for the span.
	if edge.TimeDiscount == nil || !approxEqual(edge.TimeDiscount.Factor, 0.375) ||
		!approxEqual(edge.Confidence, 0.28125) {
		t.Fatalf("edge = %+v, discount = %+v; want the two discounts multiplied", edge, edge.TimeDiscount)
	}
}

// Timing is confidence, never rank: the tie is decided before any of it, so
// both edges keep their order and take the same factor.
func TestFilesOverlapDiscountsEveryTiedEdgeEqually(t *testing.T) {
	step := FilesOverlapStep{}
	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 24 * time.Hour
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-06-02T12:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	files := []string{"src/a.py", "src/b.py"}
	candidates := []linkage.RunCandidate{
		{RunID: "run-z", EndedAt: timePtr(t, "2024-06-02T00:00:00Z"), FilesTouched: files, FilesEdited: files},
		{RunID: "run-a", EndedAt: timePtr(t, "2024-06-02T00:00:00Z"), FilesTouched: files, FilesEdited: files},
	}

	result := step.Apply(commit, candidates, cfg)
	if len(result.Edges) != 2 {
		t.Fatalf("edges = %+v, want two", result.Edges)
	}
	for i, want := range []string{"run-a", "run-z"} {
		edge := result.Edges[i]
		if edge.RunID != want || edge.Tie == nil || !approxEqual(edge.Confidence, 0.5625) ||
			edge.TimeDiscount == nil || !approxEqual(edge.TimeDiscount.Factor, 0.75) {
			t.Fatalf("edges[%d] = %+v, discount = %+v; want %s discounted like its peer", i, edge, edge.TimeDiscount, want)
		}
	}
}

func TestFilesOverlapTimeDiscountStopsAtMinConfidence(t *testing.T) {
	step := FilesOverlapStep{}
	cfg := linkage.DefaultConfig()
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "2024-07-01T12:00:00Z"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	// A month-long window discounts by 0.03, well under the floor.
	candidate := linkage.RunCandidate{
		RunID:        "run-a",
		StartedAt:    timePtr(t, "2024-06-01T12:00:00Z"),
		EndedAt:      timePtr(t, "2024-07-01T12:00:00Z"),
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, cfg)
	edge := result.Edges[0]
	if !approxEqual(edge.Confidence, cfg.FilesOverlapMinConfidence) || edge.TimeDiscount == nil {
		t.Fatalf("confidence = %v, discount = %+v; want the floor with the discount recorded",
			edge.Confidence, edge.TimeDiscount)
	}
}

// timeframe fails closed on a commit timestamp nobody can read, so only an
// ablation without it reaches this step with one. It must cost the match its
// gap discount, not the run.
func TestFilesOverlapWithoutACommitTimestampDoesNotDiscountTheGap(t *testing.T) {
	step := FilesOverlapStep{}
	commit := linkage.EnrichedCommit{
		Commit:       linkage.Commit{CommittedAt: "not-a-timestamp"},
		FilesTouched: []string{"src/a.py", "src/b.py"},
	}
	candidate := linkage.RunCandidate{
		RunID:        "run-a",
		EndedAt:      timePtr(t, "2024-06-01T12:00:00Z"),
		FilesTouched: []string{"src/a.py", "src/b.py"},
		FilesEdited:  []string{"src/a.py", "src/b.py"},
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	edge := result.Edges[0]
	if edge.TimeDiscount != nil || !approxEqual(edge.Confidence, linkage.DefaultConfig().FilesOverlapMaxConfidence) {
		t.Fatalf("edge = %+v, discount = %+v; want no discount", edge, edge.TimeDiscount)
	}
}
