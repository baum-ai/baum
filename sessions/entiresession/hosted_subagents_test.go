package entiresession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
)

const (
	reviewReport = "No defects found in parse.go.\n\n- Empty input returns an error.\n- `go test ./parser/...` passes."
	probeReport  = "Probe passed: GET /health returned 200."
	syncReport   = "The parser handles empty input."
)

// toolUseByVendorID finds our tool use for a Claude tool_use id.
func hostedToolUseByVendorID(t *testing.T, graph *session.Graph, vendorID string) session.ToolUse {
	t.Helper()
	for _, row := range graph.VendorIDs {
		if row.OurType != session.EntityToolUse || row.VendorIDKind != "tool_use.id" || row.VendorID != vendorID {
			continue
		}
		for _, use := range graph.ToolUses {
			if use.ID == row.OurID {
				return use
			}
		}
	}
	t.Fatalf("no tool use for %s", vendorID)
	return session.ToolUse{}
}

func hostedResultOf(t *testing.T, graph *session.Graph, use session.ToolUse) session.ToolResult {
	t.Helper()
	for _, result := range graph.ToolResults {
		if result.ToolUseID == use.ID {
			return result
		}
	}
	t.Fatalf("tool use %s has no result", use.ID)
	return session.ToolResult{}
}

func hostedDetailOf(t *testing.T, use session.ToolUse) session.SubagentDetail {
	t.Helper()
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(use.Metadata, &bag); err != nil {
		t.Fatal(err)
	}
	var detail session.SubagentDetail
	_ = json.Unmarshal(bag[session.SubagentDetailKey], &detail)
	return detail
}

func hostedOutputText(result session.ToolResult) string {
	if result.OutputText == nil {
		return ""
	}
	return *result.OutputText
}

// A checkpoint keeps a subagent's transcript under tasks/<tool use id>/. The
// delegate nests it under that call, with its own tool calls.
func TestHostedCheckpointSubagentTranscriptBecomesNestedSession(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "claude_subagent")
	results, report, err := hostedBuild(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Balanced() || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v, report = %+v", results, report)
	}
	if report.Tasks.Total != 2 || report.Tasks.Mapped != 1 || report.Tasks.Discarded["task.json only, no transcript"] != 1 {
		t.Fatalf("task accounting = %+v", report.Tasks)
	}
	result := results[0]
	if result.Fidelity != FidelityDelegated {
		t.Fatalf("fidelity = %d, want delegated", result.Fidelity)
	}
	graph := result.Graph
	if len(graph.Sessions) != 2 {
		t.Fatalf("sessions = %d, want the root and one nested session", len(graph.Sessions))
	}
	spawn := hostedToolUseByVendorID(t, graph, "toolu_review")
	nested := graph.Sessions[1]
	if nested.SpawnedByToolUseID == nil || *nested.SpawnedByToolUseID != spawn.ID || *nested.ParentSessionID != graph.Root().ID {
		t.Fatalf("nested session %+v is not spawned by the review call %s", nested, spawn.ID)
	}
	if spawn.Verb != session.VerbSpawnSubagent || hostedDetailOf(t, spawn) != session.SubagentTranscript {
		t.Fatalf("spawning call verb %q detail %q", spawn.Verb, hostedDetailOf(t, spawn))
	}
	var bag map[string]any
	if err := json.Unmarshal(spawn.Metadata, &bag); err != nil || bag["agentType"] != "general-purpose" || bag["description"] != "Review parser" {
		t.Fatalf("spawning call metadata = %s, want task.json's sidecar fields", spawn.Metadata)
	}
	var nestedTools []string
	for _, use := range graph.ToolUses {
		if use.SessionID == nested.ID {
			nestedTools = append(nestedTools, use.Name+"/"+string(use.Status))
		}
	}
	if strings.Join(nestedTools, ",") != "Bash/ok,Read/ok" {
		t.Fatalf("nested tool calls = %v", nestedTools)
	}
	if got := hostedOutputText(hostedResultOf(t, graph, spawn)); got != reviewReport {
		t.Fatalf("review call output = %q, want the hand-back report", got)
	}
}

// A background launch's result is a stub; the report that arrives later is
// put on the call that launched it, whichever carrier brought it.
func TestHostedCheckpointSubagentReportsReachTheirCalls(t *testing.T) {
	results, _, err := hostedBuild(hostedFixtureCheckpoint(t, "claude_subagent"))
	if err != nil {
		t.Fatal(err)
	}
	graph := results[0].Graph
	for _, test := range []struct {
		toolUseID string
		output    string
		detail    session.SubagentDetail
	}{
		{"toolu_sync", syncReport, session.SubagentReport},
		{"toolu_probe", probeReport, session.SubagentReport},
		{"toolu_review", reviewReport, session.SubagentTranscript},
	} {
		use := hostedToolUseByVendorID(t, graph, test.toolUseID)
		if got := hostedOutputText(hostedResultOf(t, graph, use)); got != test.output {
			t.Errorf("%s output = %q, want %q", test.toolUseID, got, test.output)
		}
		if got := hostedDetailOf(t, use); got != test.detail {
			t.Errorf("%s detail = %q, want %q", test.toolUseID, got, test.detail)
		}
	}
	lost := hostedToolUseByVendorID(t, graph, "toolu_lost")
	if got := hostedDetailOf(t, lost); got != session.SubagentNone || lost.Verb == session.VerbSpawnSubagent {
		t.Fatalf("a subagent with neither transcript nor report has detail %q, verb %q", got, lost.Verb)
	}
	if got := hostedOutputText(hostedResultOf(t, graph, lost)); !strings.HasPrefix(got, "Async agent launched") {
		t.Fatalf("unanswered launch output = %q, want its launch result", got)
	}
	for _, input := range graph.UserInputs {
		if strings.Contains(input.Text, "agent-message") && (input.Kind == session.KindPrompt || input.Origin == session.OriginHuman) {
			t.Fatalf("a hand-back became a %s input from %s: %q", input.Kind, input.Origin, input.Text)
		}
	}
}

// A multi-session checkpoint shares one tasks/ directory. A transcript is
// claimed only by the session its lines name.
func TestHostedCheckpointSubagentBelongsToTheSessionItNames(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "claude_subagent")
	other := *checkpoint.Sessions[0]
	other.Index, other.Directory = 1, "1"
	other.Metadata.SessionID = "0e9d8c7b-6a5f-4e3d-8c2b-1a0f9e8d7c6b"
	other.Full = []byte(strings.ReplaceAll(string(other.Full), checkpoint.Sessions[0].Metadata.SessionID, other.Metadata.SessionID))
	other.FullPresent, other.Hash = true, entirecheckpoint.HashVerification{}
	checkpoint.Sessions = append(checkpoint.Sessions, &other)

	results, report, err := hostedBuild(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Balanced() || report.Tasks.Mapped != 1 {
		t.Fatalf("report balanced %v, tasks %+v", report.Balanced(), report.Tasks)
	}
	if got := len(results[0].Graph.Sessions); got != 2 {
		t.Fatalf("named session has %d sessions, want 2", got)
	}
	if got := len(results[1].Graph.Sessions); got != 1 {
		t.Fatalf("other session has %d sessions, want only its root", got)
	}
	if len(results[0].Warnings) != 0 || len(results[1].Warnings) != 0 {
		t.Fatalf("a transcript another session names is not this session's problem: %v, %v", results[0].Warnings, results[1].Warnings)
	}
	if got := hostedDetailOf(t, hostedToolUseByVendorID(t, results[1].Graph, "toolu_review")); got != session.SubagentReport {
		t.Fatalf("other session's review call detail = %q, want report", got)
	}
}

// A task directory that does not read is accounted for, and the session keeps
// its delegated tree.
func TestHostedCheckpointUnreadableTaskIsCounted(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "claude_subagent")
	checkpoint.Tasks[1].Transcript = []byte("not json\n")
	results, report, err := hostedBuild(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err != nil || results[0].Fidelity != FidelityDelegated || len(results[0].Graph.Sessions) != 1 {
		t.Fatalf("result = %+v", results[0])
	}
	if !report.Balanced() || report.Tasks.Mapped != 0 {
		t.Fatalf("tasks = %+v", report.Tasks)
	}
	if len(results[0].Warnings) != 1 || !strings.Contains(results[0].Warnings[0], "tasks/toolu_review/agent-a1111111111111111.jsonl") {
		t.Fatalf("warnings = %v", results[0].Warnings)
	}
}

// A compact transcript has no subagent transcript and no way to tell a
// report from a launch stub, so its spawning calls record missing detail.
func TestHostedCompactSpawnCallsRecordMissingDetail(t *testing.T) {
	for _, test := range []struct{ agent, tool string }{
		{"Claude Code", "Agent"},
		{"Codex", "spawn_agent"},
	} {
		compact := strings.Join([]string{
			`{"v":1,"agent":"x","cli_version":"0.9.0","type":"user","content":[{"text":"review it"}]}`,
			`{"v":1,"agent":"x","cli_version":"0.9.0","type":"assistant","content":[` +
				`{"type":"tool_use","name":"` + test.tool + `","input":{"prompt":"review"},"result":{"output":"launched","status":"success"}},` +
				`{"type":"tool_use","name":"Read","input":{"path":"/repo/a.go"},"result":{"output":"ok","status":"success"}}]}`,
		}, "\n")
		results, _, err := hostedBuild(hostedSyntheticCheckpoint(t, test.agent, compact, nil, false))
		if err != nil || results[0].Err != nil || results[0].Fidelity != FidelityCompact {
			t.Fatalf("%s: results = %+v, %v", test.agent, results, err)
		}
		for _, use := range results[0].Graph.ToolUses {
			want := session.SubagentDetail("")
			if use.Name == test.tool {
				want = session.SubagentNone
			}
			if got := hostedDetailOf(t, use); got != want {
				t.Errorf("%s %s detail = %q, want %q", test.agent, use.Name, got, want)
			}
		}
	}
}
