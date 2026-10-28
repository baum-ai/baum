package cursorsession

import (
	"path/filepath"
	"testing"

	adaptertest "github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func hostedFixtureTree(t *testing.T) *cursortranscript.Tree {
	t.Helper()
	store, err := cursortranscript.Open(filepath.Join("..", "cursortranscript", "testdata", "store.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tree, err := store.Tree("root")
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestHostedFixtureGolden(t *testing.T) {
	graph, report, err := hostedBuild(hostedFixtureTree(t))
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.Golden(t, filepath.Join("testdata", "hosted", "golden", "fixture_graph.json"), graph)
	adaptertest.Golden(t, filepath.Join("testdata", "hosted", "golden", "fixture_report.json"), report)
}

func TestHostedBuildMapsFixture(t *testing.T) {
	tree := hostedFixtureTree(t)
	graph, report, err := hostedBuild(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Bubbles.Balanced() || report.Bubbles.Total != 18 {
		t.Fatalf("bubble report = %+v", report.Bubbles)
	}
	if len(graph.Sessions) != 3 || len(graph.UserInputs) != 4 || len(graph.ModelInvocations) != 5 {
		t.Fatalf("session/input/invocation counts = %d/%d/%d", len(graph.Sessions), len(graph.UserInputs), len(graph.ModelInvocations))
	}
	for _, invocation := range graph.ModelInvocations {
		referenced := false
		for _, message := range graph.AssistantMessages {
			referenced = referenced || message.ModelInvocationID == invocation.ID
		}
		for _, use := range graph.ToolUses {
			referenced = referenced || use.ModelInvocationID == invocation.ID
		}
		if !referenced {
			t.Fatalf("invocation %s has no assistant message or tool use", invocation.ID)
		}
	}
	if len(graph.AssistantMessages) != 3 || len(graph.ToolUses) != 8 || len(graph.ToolResults) != 7 {
		t.Fatalf("message/tool/result counts = %d/%d/%d", len(graph.AssistantMessages), len(graph.ToolUses), len(graph.ToolResults))
	}
	if graph.Root().Outcome != session.OutcomeCommitted {
		t.Fatalf("root outcome = %q", graph.Root().Outcome)
	}
	wantStart, wantEnd := epochMillis(tree.Composer.Data.CreatedAt), epochMillis(*tree.Composer.Data.LastUpdatedAt)
	if graph.Root().StartedAt == nil || graph.Root().EndedAt == nil || !graph.Root().StartedAt.Equal(*wantStart) || !graph.Root().EndedAt.Equal(*wantEnd) {
		t.Fatalf("metadata time range = %v to %v", graph.Root().StartedAt, graph.Root().EndedAt)
	}
	if !graph.UserInputs[0].Interrupted || !graph.UserInputs[1].Interrupted {
		t.Fatalf("root interruptions = %t/%t", graph.UserInputs[0].Interrupted, graph.UserInputs[1].Interrupted)
	}
	if graph.UserInputs[2].Origin != session.OriginAgent || graph.UserInputs[2].Kind != session.KindOther {
		t.Fatalf("child input = %+v", graph.UserInputs[2])
	}
	spawns := 0
	for _, use := range graph.ToolUses {
		if use.Verb == session.VerbSpawnSubagent {
			spawns++
		}
	}
	if spawns != 2 || graph.Sessions[1].SpawnedByToolUseID == nil || graph.Sessions[2].SpawnedByToolUseID == nil {
		t.Fatalf("spawn mapping = %d / %+v", spawns, graph.Sessions)
	}
	paths := map[string]*session.FileTouch{}
	for i := range graph.FileTouches {
		touch := &graph.FileTouches[i]
		paths[touch.Path] = touch
	}
	if paths["/repo/main.go"] == nil || paths["/repo/main.go"].Kind != session.TouchRead ||
		paths["/repo/pkg/edit.go"] == nil || paths["/repo/pkg/edit.go"].Kind != session.TouchEdit {
		t.Fatalf("touches = %+v", graph.FileTouches)
	}
	edit := paths["/repo/pkg/edit.go"]
	if edit.ToolUseID == nil || len(edit.Hunks) != 1 || edit.Hunks[0].OldStart != 2 || edit.Hunks[0].NewStart != 2 {
		t.Fatalf("joined diff touch = %+v", edit)
	}
	orphan := paths["/repo/pkg/orphan.go"]
	if orphan == nil || orphan.Kind != session.TouchEdit || orphan.ToolUseID != nil ||
		orphan.UserInputID != graph.UserInputs[0].ID || len(orphan.Hunks) != 1 {
		t.Fatalf("orphan diff touch = %+v", orphan)
	}
	if report.Tokens.AssignedInvocations != 1 || len(graph.TokenUsage) != 0 {
		t.Fatalf("tokens = %+v / %+v", report.Tokens, graph.TokenUsage)
	}
	tree.Composer.Data.LastUpdatedAt = nil
	wantFallback, _ := bubbleTime(tree.Composer.Bubbles[len(tree.Composer.Bubbles)-1])
	fallback, _, err := hostedBuild(tree)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Root().EndedAt == nil || !fallback.Root().EndedAt.Equal(*wantFallback) {
		t.Fatalf("last-bubble end = %v, want %v", fallback.Root().EndedAt, wantFallback)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("graph is invalid: %v", err)
	}
}

func TestHostedUnresolvedSpawnSkipsChildNotTree(t *testing.T) {
	tree := hostedFixtureTree(t)
	for _, bubble := range tree.Composer.Bubbles {
		if bubble.Tool != nil && bubble.Tool.ToolCallID == "call-child" {
			bubble.Tool.AdditionalData.SubagentComposerID = "wrong"
		}
	}
	graph, report, err := hostedBuild(tree)
	if err != nil {
		t.Fatal(err)
	}
	// The parent still maps; the child's subtree (child + grandchild, 4
	// bubbles) is skipped and accounted, never guessed onto a spawn edge.
	if len(graph.Sessions) != 1 || len(graph.UserInputs) != 2 {
		t.Fatalf("session/input counts = %d/%d", len(graph.Sessions), len(graph.UserInputs))
	}
	for _, use := range graph.ToolUses {
		if use.Verb == session.VerbSpawnSubagent {
			t.Fatalf("unexpected spawn verb on %s", use.Name)
		}
	}
	if report.Bubbles.Total != 18 || report.Bubbles.Discarded["unresolved subagent spawn"] != 4 {
		t.Fatalf("bubble report = %+v", report.Bubbles)
	}
}
