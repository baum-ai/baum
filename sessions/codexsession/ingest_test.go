package codexsession

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baumai/baum/internal/goldentest"
	"github.com/baumai/baum/sessions/codextranscript"
	collectcodex "github.com/baumai/baum/sessions/collector/codex"
	"github.com/baumai/baum/sessions/session"
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
		{name: "fast_mode_rollout.jsonl", inputs: 2},
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

// Codex reports fast mode as the thread's service tier: every invocation whose
// usage arrives after a "priority" settings event is fast, and after "default"
// standard. A rollout that never names a tier leaves the speed unreported, and
// so do invocations whose usage could not be attributed to one (basic_rollout's
// first turn has two token counts for three invocations).
func TestInvocationSpeedFollowsThreadServiceTier(t *testing.T) {
	read := func(name string) *session.Graph {
		data, err := os.ReadFile(filepath.Join("testdata", "lifecycle", name))
		if err != nil {
			t.Fatal(err)
		}
		graph, _ := buildLines(t, strings.Split(strings.TrimSpace(string(data)), "\n")...)
		return graph
	}
	speeds := func(graph *session.Graph) []string {
		var out []string
		for _, m := range graph.ModelInvocations {
			if m.CacheWrite1hTokens != nil {
				t.Errorf("invocation %s has a cache TTL split; Codex reports none", m.ID)
			}
			if m.Speed == nil {
				out = append(out, "nil")
			} else {
				out = append(out, *m.Speed)
			}
		}
		return out
	}
	if got, want := strings.Join(speeds(read("fast_mode_rollout.jsonl")), ","), "fast,nil,nil,standard"; got != want {
		t.Errorf("speeds with priority then default = %s, want %s", got, want)
	}
	if got, want := strings.Join(speeds(read("basic_rollout.jsonl")), ","), "nil,nil,nil,nil"; got != want {
		t.Errorf("speeds with no service tier = %s, want %s", got, want)
	}
	for _, tier := range []struct{ tier, want string }{{"priority", "fast"}, {"fast", "fast"}, {"ultrafast", "ultrafast"}, {"default", "standard"}, {"standard", "standard"}, {"flex", ""}, {"auto", ""}, {"", ""}} {
		got := speedForServiceTier(tier.tier)
		if (got == nil) != (tier.want == "") || got != nil && *got != tier.want {
			t.Errorf("speedForServiceTier(%q) = %v, want %q", tier.tier, got, tier.want)
		}
	}
}

// A token_count that cannot be attributed to one invocation becomes a usage
// row of exactly one request, at the thread's tier. A settings snapshot with no
// service_tier clears the tier, so the next row has no speed.
func TestUnattributedTokenCountIsOneRequestAtTheThreadTier(t *testing.T) {
	count := func(at string, total int) string {
		usage := fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":5,"total_tokens":%d}`, total, total+5)
		return envelope(at, "event_msg", `{"type":"token_count","info":{"total_token_usage":`+usage+`,"last_token_usage":`+usage+`}}`)
	}
	settings := func(at, tier string) string {
		return envelope(at, "event_msg", `{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5"`+tier+`}}`)
	}
	graph, _ := buildLines(t, append(basicStart(),
		settings("2026-08-01T12:00:04Z", `,"service_tier":"priority"`),
		count("2026-08-01T12:00:05Z", 100),
		settings("2026-08-01T12:00:06Z", ``),
		count("2026-08-01T12:00:07Z", 200),
	)...)
	if len(graph.TokenUsage) != 2 {
		t.Fatalf("token usage rows = %+v, want 2", graph.TokenUsage)
	}
	for i, row := range graph.TokenUsage {
		if row.APICallCount == nil || *row.APICallCount != 1 {
			t.Errorf("row %d api_call_count = %v, want 1", i, row.APICallCount)
		}
	}
	if s := graph.TokenUsage[0].Speed; s == nil || *s != session.SpeedFast {
		t.Errorf("row under priority has speed %v, want fast", s)
	}
	if s := graph.TokenUsage[1].Speed; s != nil {
		t.Errorf("row after a snapshot without service_tier has speed %q, want nil", *s)
	}
}
