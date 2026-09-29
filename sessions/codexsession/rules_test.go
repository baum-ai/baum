package codexsession

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func buildLines(t *testing.T, lines ...string) (*session.Graph, *Report) {
	t.Helper()
	graph, report, err := buildLinesResult(t, lines...)
	if err != nil {
		t.Fatal(err)
	}
	return graph, report
}

func buildLinesResult(t *testing.T, lines ...string) (*session.Graph, *Report, error) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "08", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-test.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := codextranscript.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := codextranscript.SingleFileTree(file)
	if err != nil {
		t.Fatal(err)
	}
	return Build(tree)
}

func envelope(at, class, payload string) string {
	return `{"timestamp":"` + at + `","type":"` + class + `","payload":` + payload + `}`
}

func paginatedEnvelope(at string, ordinal int, class, payload string) string {
	return `{"timestamp":"` + at + `","ordinal":` + fmt.Sprint(ordinal) + `,"type":"` + class + `","payload":` + payload + `}`
}

func basicStart() []string {
	return []string{
		envelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		envelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		envelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
	}
}

func TestPathsFromPatchIncludesRenameDestination(t *testing.T) {
	got := strings.Join(pathsFromPatch("*** Update File: old/name.go\n*** Move to: new/name.go"), ",")
	if got != "old/name.go,new/name.go" {
		t.Fatalf("pathsFromPatch = %q", got)
	}
}

func toolForCall(t *testing.T, graph *session.Graph, callID string) session.ToolUse {
	t.Helper()
	for _, mapping := range graph.VendorIDs {
		if mapping.OurType != session.EntityToolUse || mapping.VendorIDKind != "call_id" || mapping.VendorID != callID {
			continue
		}
		for _, use := range graph.ToolUses {
			if use.ID == mapping.OurID {
				return use
			}
		}
	}
	t.Fatalf("no tool use for call %s", callID)
	return session.ToolUse{}
}

func TestSessionMetaGitAttributes(t *testing.T) {
	graph, report := buildLines(t,
		envelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli","git":{"commit_hash":"0123456789abcdef","branch":"feature","repository_url":"https://example.test/repo.git"}}`),
	)
	if !report.Records.Balanced() {
		t.Fatalf("report = %+v", report.Records)
	}
	attributes := map[string]string{}
	for _, attribute := range graph.Attributes {
		attributes[attribute.Key] = string(attribute.Value)
	}
	for key, want := range map[string]string{
		"git.commit_hash":    `"0123456789abcdef"`,
		"git.branch":         `"feature"`,
		"git.repository_url": `"https://example.test/repo.git"`,
	} {
		if attributes[key] != want {
			t.Errorf("%s = %s, want %s", key, attributes[key], want)
		}
	}
}

func TestToolStatusesAndOrphanPatchEvidence(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"unknown_tool","arguments":"{}","call_id":"unknown"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"unknown","output":"saved output"}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"true","call_id":"ok","status":"in_progress"}`),
		envelope("2026-08-01T12:00:07Z", "response_item", `{"type":"custom_tool_call_output","call_id":"ok","output":"done"}`),
		envelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"exec_command_end","call_id":"ok","status":"completed","exit_code":0,"command":"true","stdout":"done"}`),
		envelope("2026-08-01T12:00:09Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"false","call_id":"error","status":"completed"}`),
		envelope("2026-08-01T12:00:10Z", "response_item", `{"type":"custom_tool_call_output","call_id":"error","output":"failed"}`),
		envelope("2026-08-01T12:00:11Z", "event_msg", `{"type":"exec_command_end","call_id":"error","status":"failed","exit_code":1,"command":"false","stderr":"explicit error"}`),
		envelope("2026-08-01T12:00:12Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"sleep 1","call_id":"interrupted","status":"cancelled"}`),
		envelope("2026-08-01T12:00:13Z", "response_item", `{"type":"custom_tool_call_output","call_id":"interrupted","output":"stopped"}`),
		envelope("2026-08-01T12:00:14Z", "event_msg", `{"type":"exec_command_end","call_id":"interrupted","status":"failed","exit_code":1,"command":"sleep 1"}`),
		envelope("2026-08-01T12:00:15Z", "response_item", `{"type":"function_call","name":"open_tool","arguments":"{}","call_id":"open"}`),
		envelope("2026-08-01T12:00:16Z", "event_msg", `{"type":"patch_apply_end","call_id":"exec-orphan","success":true,"status":"completed","changes":{"/repo/orphan.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old\n+new"}}}`),
		envelope("2026-08-01T12:00:17Z", "response_item", `{"type":"web_search_call","status":"in_progress","action":{"type":"search","query":"mapper"}}`),
		envelope("2026-08-01T12:00:18Z", "event_msg", `{"type":"web_search_end","call_id":"provider-id","query":"mapper","results":[]}`),
		envelope("2026-08-01T12:00:19Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	for callID, want := range map[string]session.ToolStatus{
		"unknown": session.StatusUnknown, "ok": session.StatusOK, "error": session.StatusError,
		"interrupted": session.StatusInterrupted, "open": session.StatusNoResult,
	} {
		if got := toolForCall(t, graph, callID).Status; got != want {
			t.Errorf("call %s status = %q, want %q", callID, got, want)
		}
	}
	if len(graph.FileTouches) != 1 || graph.FileTouches[0].ToolUseID != nil || graph.FileTouches[0].Path != "/repo/orphan.go" {
		t.Fatalf("orphan patch touches = %+v", graph.FileTouches)
	}
	if report.Records.Discarded["context/web_search_end"] != 1 {
		t.Fatalf("web_search_end was not kept separate: %+v", report.Records)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("mapped tools graph is invalid: %v", err)
	}
}

func TestPreStartHumanInputOwnsTurnConsequences(t *testing.T) {
	graph, report := buildLines(t,
		envelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		envelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
		envelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		envelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"true\"}","call_id":"call"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"call","output":"done"}`),
		envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 || len(graph.ToolUses) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if graph.ToolUses[0].UserInputID != graph.UserInputs[0].ID {
		t.Fatalf("tool owner = %q, want %q", graph.ToolUses[0].UserInputID, graph.UserInputs[0].ID)
	}
}

func TestPendingHumanInputDoesNotOwnPreStartConsequences(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"user_message","message":"Continue"}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Too early"}]}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_started","turn_id":"turn-2"}`),
		envelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"task_complete","turn_id":"turn-2"}`),
	)
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 1 || len(graph.UserInputs) != 2 || len(graph.AssistantMessages) != 0 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestPendingHumanInputAtEOFIsRecorded(t *testing.T) {
	graph, report := buildLines(t,
		envelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		envelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("mapped graph is invalid: %v", err)
	}
}

func TestAbortWithoutTurnIDClosesOpenTurn(t *testing.T) {
	lines := basicStart()
	lines = append(lines, envelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"turn_aborted","reason":"interrupted"}`))
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 0 || !graph.UserInputs[0].Interrupted || graph.Root().Outcome != session.OutcomeInterrupted {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestAbortWithMismatchedTurnIDFails(t *testing.T) {
	lines := basicStart()
	lines = append(lines, envelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"turn_aborted","turn_id":"other","reason":"interrupted"}`))
	_, _, err := buildLinesResult(t, lines...)
	if err == nil || !strings.Contains(err.Error(), "turn other ends while turn turn is open") {
		t.Fatalf("error = %v", err)
	}
}

func TestPatchCompletionSortsTouches(t *testing.T) {
	for _, matched := range []bool{false, true} {
		name := "orphan"
		if matched {
			name = "matched"
		}
		t.Run(name, func(t *testing.T) {
			lines := basicStart()
			if matched {
				lines = append(lines, envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","call_id":"patch","status":"in_progress"}`))
			}
			lines = append(lines,
				envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"patch_apply_end","call_id":"patch","success":true,"changes":{"/repo/z.go":{"type":"update"},"/repo/a.go":{"type":"update"},"/repo/m.go":{"type":"update"}}}`),
				envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
			)
			graph, _ := buildLines(t, lines...)
			var paths []string
			for _, touch := range graph.FileTouches {
				paths = append(paths, touch.Path)
				if matched != (touch.ToolUseID != nil) {
					t.Fatalf("touch association = %+v", touch)
				}
			}
			if got := strings.Join(paths, ","); got != "/repo/a.go,/repo/m.go,/repo/z.go" {
				t.Fatalf("touch paths = %q", got)
			}
		})
	}
}

func TestOnlyFileCapableToolsProduceTouches(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"symbol_lookup","arguments":"{\"path\":\"/repo/not-edited.go\"}","call_id":"lookup"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call","name":"read_file","arguments":"{\"path\":\"/repo/read.go\"}","call_id":"read"}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"apply_patch","arguments":"{\"path\":\"/repo/edited.go\"}","call_id":"edit"}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := buildLines(t, lines...)
	if len(graph.FileTouches) != 2 {
		t.Fatalf("file touches = %+v", graph.FileTouches)
	}
	byPath := map[string]session.TouchKind{}
	for _, touch := range graph.FileTouches {
		byPath[touch.Path] = touch.Kind
	}
	if _, exists := byPath["/repo/not-edited.go"]; exists {
		t.Error("a non-file-capable tool produced a file touch")
	}
	if byPath["/repo/read.go"] != session.TouchRead || byPath["/repo/edited.go"] != session.TouchEdit {
		t.Fatalf("touch kinds = %+v", byPath)
	}
}

func TestMCPCompletionUsesTaggedOutcome(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"mcp","arguments":"{}","call_id":"ok"}`),
		envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"mcp_tool_call_end","call_id":"ok","result":{"Ok":{"content":[]}}}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"mcp","arguments":"{}","call_id":"error"}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"mcp_tool_call_end","call_id":"error","result":{"Err":"permission denied"}}`),
		envelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := buildLines(t, lines...)
	if got := toolForCall(t, graph, "ok").Status; got != session.StatusOK {
		t.Errorf("Ok status = %q", got)
	}
	if got := toolForCall(t, graph, "error").Status; got != session.StatusError {
		t.Errorf("Err status = %q", got)
	}
}

func TestTokenAssignmentAndAmbiguousUsage(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"first_tool","arguments":"{}","call_id":"first"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"first","output":"done"}`),
		envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`),
		envelope("2026-08-01T12:00:07Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		envelope("2026-08-01T12:00:08Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		envelope("2026-08-01T12:00:09Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Second"}]}`),
		envelope("2026-08-01T12:00:10Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":5,"output_tokens":5,"total_tokens":35},"last_token_usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"total_tokens":23}}}`),
		envelope("2026-08-01T12:00:11Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":5,"output_tokens":5,"total_tokens":35},"last_token_usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"total_tokens":23}}}`),
		envelope("2026-08-01T12:00:12Z", "event_msg", `{"type":"user_message","message":"Continue"}`),
		envelope("2026-08-01T12:00:13Z", "response_item", `{"type":"function_call","name":"third_tool","arguments":"{}","call_id":"third"}`),
		envelope("2026-08-01T12:00:14Z", "response_item", `{"type":"function_call_output","call_id":"third","output":"done"}`),
		envelope("2026-08-01T12:00:15Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6},"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`),
		envelope("2026-08-01T12:00:16Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if graph.ModelInvocations[0].InputTokens != 10 || graph.ModelInvocations[0].OutputTokens != 2 {
		t.Fatalf("assigned invocation usage = %+v", graph.ModelInvocations[0])
	}
	if len(graph.TokenUsage) != 1 || graph.TokenUsage[0].Scope != session.UsageScopeInput || graph.TokenUsage[0].CacheReadTokens == nil || *graph.TokenUsage[0].CacheReadTokens != 5 {
		t.Fatalf("ambiguous usage = %+v", graph.TokenUsage)
	}
	lastInvocation := graph.ModelInvocations[len(graph.ModelInvocations)-1]
	if lastInvocation.InputTokens != 5 || lastInvocation.TotalTokens != 6 {
		t.Fatalf("usage after a falling cumulative total = %+v", lastInvocation)
	}
	if report.Tokens.AssignedInvocations != 2 || report.Tokens.AmbiguousRows != 1 || report.Tokens.RepeatedTotals != 1 {
		t.Fatalf("token report = %+v", report.Tokens)
	}
}

func TestPaginatedTokenCountKeepsPromptOwner(t *testing.T) {
	graph, _ := buildLines(t,
		paginatedEnvelope("2026-08-01T12:00:00Z", 0, "session_meta", `{"id":"root","history_mode":"paginated","source":"cli"}`),
		paginatedEnvelope("2026-08-01T12:00:01Z", 1, "event_msg", `{"type":"user_message","message":"Do it"}`),
		paginatedEnvelope("2026-08-01T12:00:02Z", 2, "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		paginatedEnvelope("2026-08-01T12:00:03Z", 3, "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		paginatedEnvelope("2026-08-01T12:00:04Z", 4, "response_item", `{"type":"message","id":"answer","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		paginatedEnvelope("2026-08-01T12:00:05Z", 5, "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	if len(graph.ModelInvocations) != 1 || graph.ModelInvocations[0].UserInputID != graph.UserInputs[0].ID || graph.ModelInvocations[0].TotalTokens != 23 {
		t.Fatalf("paginated invocation usage = %+v, want 23 tokens owned by prompt", graph.ModelInvocations)
	}
}

func TestTokenCountAfterTurnEndKeepsItsPromptOwner(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	graph, _ := buildLines(t, lines...)
	if len(graph.TokenUsage) != 1 || graph.TokenUsage[0].UserInputID == nil {
		t.Fatalf("post-turn usage = %+v, want one input-scoped aggregate", graph.TokenUsage)
	}
	if *graph.TokenUsage[0].UserInputID != graph.UserInputs[0].ID || graph.TokenUsage[0].Scope != session.UsageScopeInput {
		t.Fatalf("post-turn usage owner = %+v, want prompt %s", graph.TokenUsage[0], graph.UserInputs[0].ID)
	}
	if graph.TokenUsage[0].Model == nil || *graph.TokenUsage[0].Model != "gpt-5" {
		t.Fatalf("post-turn usage model = %v, want gpt-5", graph.TokenUsage[0].Model)
	}
}

func TestTokenCountAfterTurnEndKeepsPromptSeenBeforeTurnStart(t *testing.T) {
	graph, _ := buildLines(t,
		envelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		envelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
		envelope("2026-08-01T12:00:02Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:03Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		envelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		envelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	if len(graph.TokenUsage) != 1 || graph.TokenUsage[0].UserInputID == nil ||
		*graph.TokenUsage[0].UserInputID != graph.UserInputs[0].ID ||
		graph.TokenUsage[0].Scope != session.UsageScopeInput {
		t.Fatalf("post-turn usage = %+v, want prompt %s", graph.TokenUsage, graph.UserInputs[0].ID)
	}
}

func TestCommitOutcomeNeedsSuccessfulHashEvidence(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"git commit -m x","call_id":"commit","status":"in_progress"}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"custom_tool_call_output","call_id":"commit","output":"[main abc1234] x"}`),
		envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"exec_command_end","call_id":"commit","status":"completed","exit_code":0,"stdout":"[main abc1234] x"}`),
		envelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := buildLines(t, lines...)
	if graph.Root().Outcome != session.OutcomeCommitted {
		t.Fatalf("outcome = %q", graph.Root().Outcome)
	}
}

func TestUnprovenUserItemMakesDownstreamTurnUnsupported(t *testing.T) {
	lines := basicStart()[:3]
	lines = append(lines,
		envelope("2026-08-01T12:00:03Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"Unproven"}]}`),
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Should not persist"}]}`),
		envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 1 || !report.Records.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if len(graph.UserInputs) != 0 || len(graph.AssistantMessages) != 0 {
		t.Fatalf("unsupported consequences persisted: %+v", graph)
	}
}

func TestConsequenceOutsideATurnBlocksPersistence(t *testing.T) {
	lines := basicStart()[:1]
	lines = append(lines,
		envelope("2026-08-01T12:00:01Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Unowned"}]}`),
		envelope("2026-08-01T12:00:02Z", "response_item", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"true\"}","call_id":"outside"}`),
	)
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 1 || !report.Records.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if len(graph.AssistantMessages) != 0 || len(graph.ToolUses) != 0 {
		t.Fatalf("outside-turn consequences persisted: %+v", graph)
	}
	if report.Records.Discarded["unsupported activity outside turn"] != 2 {
		t.Fatalf("records = %+v", report.Records)
	}
}

func TestUnknownToolOutputIsReportedAndBlocksPersistence(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call_output","call_id":"missing","output":"orphan"}`),
		envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 1 || report.Records.Discarded["unsupported tool output"] != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestOnlyStartedSubagentActivityProvesSuccess(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"spawn_agent","arguments":"{}","call_id":"spawn"}`),
		envelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"sub_agent_activity","event_id":"spawn","agent_thread_id":"child","agent_path":"/root/a","kind":"failed"}`),
		envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if got := toolForCall(t, graph, "spawn").Status; got != session.StatusUnknown {
		t.Fatalf("spawn status = %q", got)
	}
	if len(graph.ToolResults) != 1 || report.Records.Merged != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}

func TestUserItemsAreAlwaysModelVisibleContext(t *testing.T) {
	lines := basicStart()
	lines = append(lines,
		envelope("2026-08-01T12:00:04Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"Different input"}]}`),
		envelope("2026-08-01T12:00:05Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Must not persist"}]}`),
		envelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := buildLines(t, lines...)
	if report.UnsupportedTurns != 0 || len(graph.AssistantMessages) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if report.Records.Discarded["context/model_visible_user_copy"] != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}
