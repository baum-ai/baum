package codexsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/reviewpal/internal/goldentest"
	"github.com/lastpersonlabs/reviewpal/sessions/codextranscript"
	collectcodex "github.com/lastpersonlabs/reviewpal/sessions/collector/codex"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func TestFixtureGolden(t *testing.T) {
	graph, report, err := Build(fixtureTree(t))
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Golden(t, filepath.Join("testdata", "golden", "fixture_graph.json"), graph)
	goldentest.Golden(t, filepath.Join("testdata", "golden", "fixture_report.json"), report)
}

func fixtureTree(t *testing.T) *codextranscript.Tree {
	t.Helper()
	store := filepath.Join("..", "codextranscript", "testdata", "store")
	index, _, err := collectcodex.ScanStore(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index {
		if entry.Class == codextranscript.ThreadRoot {
			files, err := collectcodex.Find(index, entry)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := codextranscript.DecodeLocalFiles(files)
			if err != nil {
				t.Fatal(err)
			}
			return tree
		}
	}
	t.Fatal("fixture has no root")
	return nil
}

func TestBuildMapsCollaborationFixture(t *testing.T) {
	graph, report, err := Build(fixtureTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Records.Balanced() {
		t.Fatalf("records are not balanced: %+v", report.Records)
	}
	if report.UnsupportedTurns != 0 {
		t.Fatalf("unsupported turns = %d", report.UnsupportedTurns)
	}
	if len(graph.Sessions) != 2 || graph.Sessions[1].SpawnedByToolUseID == nil {
		t.Fatalf("sessions = %+v", graph.Sessions)
	}
	if graph.UserInputs[0].Origin != session.OriginHuman || graph.UserInputs[1].Origin != session.OriginAgent {
		t.Fatalf("input origins = %q, %q", graph.UserInputs[0].Origin, graph.UserInputs[1].Origin)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("mapped graph is invalid: %v", err)
	}
}

func TestCodex0129LifecycleFixtures(t *testing.T) {
	tests := []struct {
		name        string
		inputs      int
		interrupted bool
	}{
		{name: "basic_rollout.jsonl", inputs: 2},
		{name: "commit_rollout.jsonl", inputs: 1},
		{name: "aborted_rollout.jsonl", inputs: 1, interrupted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("testdata", "lifecycle", test.name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			graph, report := buildLines(t, strings.Split(strings.TrimSpace(string(data)), "\n")...)
			if report.UnsupportedTurns != 0 || !report.Records.Balanced() {
				t.Fatalf("report = %+v", report)
			}
			if len(graph.UserInputs) != test.inputs {
				t.Fatalf("user inputs = %+v", graph.UserInputs)
			}
			for _, use := range graph.ToolUses {
				if use.UserInputID == "" {
					t.Fatalf("tool has no input owner: %+v", use)
				}
			}
			if test.interrupted && (!graph.UserInputs[0].Interrupted || graph.Root().Outcome != session.OutcomeInterrupted) {
				t.Fatalf("aborted graph = %+v", graph)
			}
			if err := session.Validate(graph); err != nil {
				t.Fatalf("mapped graph is invalid: %v", err)
			}
		})
	}
}

func TestInheritedHistoryIsCountedNotMapped(t *testing.T) {
	graph, report, err := Build(fixtureTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Records.Discarded["inherited history"] != 1 {
		t.Fatalf("inherited history count = %d", report.Records.Discarded["inherited history"])
	}
	if got := graph.Sessions[1].StartedAt; got == nil || got.Format(time.RFC3339) != "2026-08-01T12:01:00Z" {
		t.Fatalf("child start includes inherited history: %v", got)
	}
}

func TestSpawnRequiresBothLinks(t *testing.T) {
	tree := fixtureTree(t)
	tree.Children[0].Meta.AgentPath = "/root/wrong"
	_, _, err := Build(tree)
	if err == nil {
		t.Fatal("Build accepted a child without matching activity evidence")
	}
}

func TestUnprovableBoundaryFails(t *testing.T) {
	tree := fixtureTree(t)
	child := tree.Children[0]
	fork := tree.Rollout(child.Meta.ForkedFromID)
	forkTurns := map[string]bool{}
	for _, line := range fork.Lines {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil {
			forkTurns[line.EventMsg.TaskStarted.TurnID] = true
		}
	}
	for _, line := range child.Lines {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil && !forkTurns[line.EventMsg.TaskStarted.TurnID] {
			fork.Lines = append(fork.Lines, line)
			break
		}
	}
	_, _, err := Build(tree)
	if err == nil {
		t.Fatal("Build accepted a child with no provable own-history boundary")
	}
}

func TestTriggerTurnFalseStaysParentContext(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "inter_agent_communication_metadata", `{"trigger_turn":false}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"agent_message","id":"context","author":"/root/a","recipient":"/root","content":[{"type":"input_text","text":"Context only"}]}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Still answering"}]}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if len(graph.UserInputs) != 1 || len(graph.AssistantMessages) != 1 || report.UnsupportedTurns != 0 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}
