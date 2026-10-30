package codexsession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func hostedBuildLines(t *testing.T, lines ...string) (*session.Graph, *Report) {
	t.Helper()
	graph, report, err := hostedBuildLinesResult(t, lines...)
	if err != nil {
		t.Fatal(err)
	}
	return graph, report
}

func hostedBuildLinesResult(t *testing.T, lines ...string) (*session.Graph, *Report, error) {
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
	return hostedBuild(tree)
}

func hostedEnvelope(at, class, payload string) string {
	return `{"timestamp":"` + at + `","type":"` + class + `","payload":` + payload + `}`
}

func hostedPaginatedEnvelope(at string, ordinal int, class, payload string) string {
	return `{"timestamp":"` + at + `","ordinal":` + fmt.Sprint(ordinal) + `,"type":"` + class + `","payload":` + payload + `}`
}

func hostedBasicStart() []string {
	return []string{
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
	}
}

func TestHostedPathsFromPatchIncludesRenameDestination(t *testing.T) {
	got := strings.Join(pathsFromPatch("*** Update File: old/name.go\n*** Move to: new/name.go"), ",")
	if got != "old/name.go,new/name.go" {
		t.Fatalf("pathsFromPatch = %q", got)
	}
}

func hostedToolForCall(t *testing.T, graph *session.Graph, callID string) session.ToolUse {
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

func TestHostedSessionMetaGitAttributes(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli","git":{"commit_hash":"0123456789abcdef","branch":"feature","repository_url":"https://example.test/repo.git"}}`),
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

func TestHostedToolStatusesAndOrphanPatchEvidence(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"unknown_tool","arguments":"{}","call_id":"unknown"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"unknown","output":"saved output"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"true","call_id":"ok","status":"in_progress"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "response_item", `{"type":"custom_tool_call_output","call_id":"ok","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"exec_command_end","call_id":"ok","status":"completed","exit_code":0,"command":"true","stdout":"done"}`),
		hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"false","call_id":"error","status":"completed"}`),
		hostedEnvelope("2026-08-01T12:00:10Z", "response_item", `{"type":"custom_tool_call_output","call_id":"error","output":"failed"}`),
		hostedEnvelope("2026-08-01T12:00:11Z", "event_msg", `{"type":"exec_command_end","call_id":"error","status":"failed","exit_code":1,"command":"false","stderr":"explicit error"}`),
		hostedEnvelope("2026-08-01T12:00:12Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"sleep 1","call_id":"interrupted","status":"cancelled"}`),
		hostedEnvelope("2026-08-01T12:00:13Z", "response_item", `{"type":"custom_tool_call_output","call_id":"interrupted","output":"stopped"}`),
		hostedEnvelope("2026-08-01T12:00:14Z", "event_msg", `{"type":"exec_command_end","call_id":"interrupted","status":"failed","exit_code":1,"command":"sleep 1"}`),
		hostedEnvelope("2026-08-01T12:00:15Z", "response_item", `{"type":"function_call","name":"open_tool","arguments":"{}","call_id":"open"}`),
		hostedEnvelope("2026-08-01T12:00:16Z", "event_msg", `{"type":"patch_apply_end","call_id":"exec-orphan","success":true,"status":"completed","changes":{"/repo/orphan.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old\n+new"}}}`),
		hostedEnvelope("2026-08-01T12:00:17Z", "response_item", `{"type":"web_search_call","status":"in_progress","action":{"type":"search","query":"mapper"}}`),
		hostedEnvelope("2026-08-01T12:00:18Z", "event_msg", `{"type":"web_search_end","call_id":"provider-id","query":"mapper","results":[]}`),
		hostedEnvelope("2026-08-01T12:00:19Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	for callID, want := range map[string]session.ToolStatus{
		"unknown": session.StatusUnknown, "ok": session.StatusOK, "error": session.StatusError,
		"interrupted": session.StatusInterrupted, "open": session.StatusNoResult,
	} {
		if got := hostedToolForCall(t, graph, callID).Status; got != want {
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

func TestHostedPreStartHumanInputOwnsTurnConsequences(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"true\"}","call_id":"call"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"call","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 || len(graph.ToolUses) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if graph.ToolUses[0].UserInputID != graph.UserInputs[0].ID {
		t.Fatalf("tool owner = %q, want %q", graph.ToolUses[0].UserInputID, graph.UserInputs[0].ID)
	}
}

func TestHostedPendingHumanInputDoesNotOwnPreStartConsequences(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"user_message","message":"Continue"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Too early"}]}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_started","turn_id":"turn-2"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"task_complete","turn_id":"turn-2"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 1 || len(graph.UserInputs) != 2 || len(graph.AssistantMessages) != 0 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestHostedPendingHumanInputAtEOFIsRecorded(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("mapped graph is invalid: %v", err)
	}
}

func TestHostedAbortWithoutTurnIDClosesOpenTurn(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines, hostedEnvelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"turn_aborted","reason":"interrupted"}`))
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 0 || !graph.UserInputs[0].Interrupted || graph.Root().Outcome != session.OutcomeInterrupted {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestHostedAbortWithMismatchedTurnIDFails(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines, hostedEnvelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"turn_aborted","turn_id":"other","reason":"interrupted"}`))
	_, _, err := hostedBuildLinesResult(t, lines...)
	if err == nil || !strings.Contains(err.Error(), "turn other ends while turn turn is open") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostedPatchCompletionSortsTouches(t *testing.T) {
	for _, matched := range []bool{false, true} {
		name := "orphan"
		if matched {
			name = "matched"
		}
		t.Run(name, func(t *testing.T) {
			lines := hostedBasicStart()
			if matched {
				lines = append(lines, hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","call_id":"patch","status":"in_progress"}`))
			}
			lines = append(lines,
				hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"patch_apply_end","call_id":"patch","success":true,"changes":{"/repo/z.go":{"type":"update"},"/repo/a.go":{"type":"update"},"/repo/m.go":{"type":"update"}}}`),
				hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
			)
			graph, _ := hostedBuildLines(t, lines...)
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

func TestHostedOnlyFileCapableToolsProduceTouches(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"symbol_lookup","arguments":"{\"path\":\"/repo/not-edited.go\"}","call_id":"lookup"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call","name":"read_file","arguments":"{\"path\":\"/repo/read.go\"}","call_id":"read"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"apply_patch","arguments":"{\"path\":\"/repo/edited.go\"}","call_id":"edit"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := hostedBuildLines(t, lines...)
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

func TestHostedMCPCompletionUsesTaggedOutcome(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"mcp","arguments":"{}","call_id":"ok"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"mcp_tool_call_end","call_id":"ok","result":{"Ok":{"content":[]}}}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"mcp","arguments":"{}","call_id":"error"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"mcp_tool_call_end","call_id":"error","result":{"Err":"permission denied"}}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := hostedBuildLines(t, lines...)
	if got := hostedToolForCall(t, graph, "ok").Status; got != session.StatusOK {
		t.Errorf("Ok status = %q", got)
	}
	if got := hostedToolForCall(t, graph, "error").Status; got != session.StatusError {
		t.Errorf("Err status = %q", got)
	}
}

func TestHostedTokenAssignmentAndAmbiguousUsage(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"first_tool","arguments":"{}","call_id":"first"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"first","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Second"}]}`),
		hostedEnvelope("2026-08-01T12:00:10Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":5,"output_tokens":5,"total_tokens":35},"last_token_usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"total_tokens":23}}}`),
		hostedEnvelope("2026-08-01T12:00:11Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":5,"output_tokens":5,"total_tokens":35},"last_token_usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"total_tokens":23}}}`),
		hostedEnvelope("2026-08-01T12:00:12Z", "event_msg", `{"type":"user_message","message":"Continue"}`),
		hostedEnvelope("2026-08-01T12:00:13Z", "response_item", `{"type":"function_call","name":"third_tool","arguments":"{}","call_id":"third"}`),
		hostedEnvelope("2026-08-01T12:00:14Z", "response_item", `{"type":"function_call_output","call_id":"third","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:15Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6},"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`),
		hostedEnvelope("2026-08-01T12:00:16Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
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

func hostedUsageJSON(input, output int) string {
	return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}`, input, output, input+output)
}

func hostedRecordLine(at, last, threadTotal string) string {
	return hostedEnvelope(at, "token_usage_record", `{"thread_id":"thread","turn_id":"turn","response_id":"resp","usage":`+last+`,"turn_token_usage":`+threadTotal+`,"thread_token_usage":`+threadTotal+`}`)
}

func hostedCountLine(at, last, total string) string {
	return hostedEnvelope(at, "event_msg", `{"type":"token_count","info":{"total_token_usage":`+total+`,"last_token_usage":`+last+`}}`)
}

func hostedTotalUsage(graph *session.Graph) int64 {
	var total int64
	for _, invocation := range graph.ModelInvocations {
		total += invocation.TotalTokens
	}
	for _, row := range graph.TokenUsage {
		if row.TotalTokens != nil {
			total += *row.TotalTokens
		}
	}
	return total
}

// A rollout with token_usage_records counts each record once. The record comes
// before its call's output, the token_count after it is superseded, and a
// trailing record whose token_count never arrived still counts.
func TestHostedTokenUsageRecordCountsEachRequestOnce(t *testing.T) {
	first, second := hostedUsageJSON(10, 2), `{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"total_tokens":23}`
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"first_tool","arguments":"{}","call_id":"first"}`),
		hostedRecordLine("2026-08-01T12:00:05Z", first, first),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call_output","call_id":"first","output":"done"}`),
		hostedCountLine("2026-08-01T12:00:07Z", first, first),
		hostedEnvelope("2026-08-01T12:00:08Z", "response_item", `{"type":"function_call","name":"second_tool","arguments":"{}","call_id":"second"}`),
		hostedRecordLine("2026-08-01T12:00:09Z", second, `{"total_tokens":35}`),
		hostedEnvelope("2026-08-01T12:00:10Z", "response_item", `{"type":"function_call_output","call_id":"second","output":"done"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if graph.ModelInvocations[0].InputTokens != 10 || graph.ModelInvocations[0].TotalTokens != 12 {
		t.Fatalf("first invocation usage = %+v", graph.ModelInvocations[0])
	}
	last := graph.ModelInvocations[len(graph.ModelInvocations)-1]
	if last.InputTokens != 20 || last.CacheReadTokens != 5 || last.TotalTokens != 23 {
		t.Fatalf("trailing record usage = %+v", last)
	}
	if len(graph.TokenUsage) != 0 {
		t.Fatalf("token usage rows = %+v, want none", graph.TokenUsage)
	}
	if report.Tokens.AssignedInvocations != 2 || report.Tokens.RepeatedTotals != 0 || report.Tokens.Records != 2 {
		t.Fatalf("token report = %+v", report.Tokens)
	}
	if report.Records.Discarded["context/token_count_superseded"] != 1 || !report.Records.Balanced() {
		t.Fatalf("records = %+v", report.Records)
	}
}

// After a remote compaction the record's thread total includes the compaction
// request but token_count's does not, so totals stay offset. Records are the
// sole source: every request counts once, the compaction request is one
// ambiguous row, and the zero-usage token_count after it produces nothing.
func TestHostedTokenUsageRecordsSurviveCompactionTotalDrift(t *testing.T) {
	a, compaction, b := hostedUsageJSON(100, 0), hostedUsageJSON(236, 0), hostedUsageJSON(50, 0)
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"a_tool","arguments":"{}","call_id":"a"}`),
		hostedRecordLine("2026-08-01T12:00:05Z", a, hostedUsageJSON(100, 0)),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call_output","call_id":"a","output":"done"}`),
		hostedCountLine("2026-08-01T12:00:07Z", a, hostedUsageJSON(100, 0)),
		hostedRecordLine("2026-08-01T12:00:08Z", compaction, hostedUsageJSON(336, 0)),
		hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"compaction","id":"c","encrypted_content":"x","internal_chat_message_metadata_passthrough":{}}`),
		hostedEnvelope("2026-08-01T12:00:10Z", "compacted", `{"message":""}`),
		hostedEnvelope("2026-08-01T12:00:11Z", "world_state", `{}`),
		hostedEnvelope("2026-08-01T12:00:12Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:13Z", "event_msg", `{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5"}}`),
		hostedCountLine("2026-08-01T12:00:14Z", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, hostedUsageJSON(100, 0)),
		hostedEnvelope("2026-08-01T12:00:15Z", "response_item", `{"type":"function_call","name":"b_tool","arguments":"{}","call_id":"b"}`),
		hostedRecordLine("2026-08-01T12:00:16Z", b, hostedUsageJSON(386, 0)),
		hostedEnvelope("2026-08-01T12:00:17Z", "response_item", `{"type":"function_call_output","call_id":"b","output":"done"}`),
		hostedCountLine("2026-08-01T12:00:18Z", b, hostedUsageJSON(150, 0)),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedTotalUsage(graph); got != 386 {
		t.Fatalf("counted %d tokens, want the final thread total 386", got)
	}
	if len(graph.ModelInvocations) != 2 || len(graph.TokenUsage) != 1 || *graph.TokenUsage[0].TotalTokens != 236 {
		t.Fatalf("invocations = %+v, token usage = %+v", graph.ModelInvocations, graph.TokenUsage)
	}
	if report.Tokens.AssignedInvocations != 2 || report.Tokens.AmbiguousRows != 1 || report.Tokens.RepeatedTotals != 0 || report.Tokens.Records != 3 {
		t.Fatalf("token report = %+v", report.Tokens)
	}
	if report.Records.Discarded["context/token_count_superseded"] != 3 || report.Records.Discarded["context/compaction_item"] != 1 || !report.Records.Balanced() {
		t.Fatalf("records = %+v", report.Records)
	}
}

// A session an older Codex wrote and a newer one resumed has token_counts first
// and token_usage_records after. Only token_counts after the first record are
// superseded; the earlier ones count through the repeat check, so every request
// counts once.
func TestHostedTokenUsageRecordsSupersedeOnlyLaterTokenCounts(t *testing.T) {
	first, second, third, fourth := hostedUsageJSON(10, 2), hostedUsageJSON(20, 3), hostedUsageJSON(30, 4), hostedUsageJSON(40, 5)
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"a_tool","arguments":"{}","call_id":"a"}`),
		hostedCountLine("2026-08-01T12:00:05Z", first, first),
		hostedCountLine("2026-08-01T12:00:06Z", first, first),
		hostedEnvelope("2026-08-01T12:00:07Z", "response_item", `{"type":"function_call","name":"b_tool","arguments":"{}","call_id":"b"}`),
		hostedCountLine("2026-08-01T12:00:08Z", second, hostedUsageJSON(30, 5)),
		hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"function_call","name":"c_tool","arguments":"{}","call_id":"c"}`),
		hostedRecordLine("2026-08-01T12:00:10Z", third, hostedUsageJSON(64, 9)),
		hostedCountLine("2026-08-01T12:00:11Z", third, hostedUsageJSON(64, 9)),
		hostedEnvelope("2026-08-01T12:00:12Z", "response_item", `{"type":"function_call","name":"d_tool","arguments":"{}","call_id":"d"}`),
		hostedRecordLine("2026-08-01T12:00:13Z", fourth, hostedUsageJSON(109, 14)),
		hostedCountLine("2026-08-01T12:00:14Z", fourth, hostedUsageJSON(109, 14)),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedTotalUsage(graph); got != 12+23+34+45 {
		t.Fatalf("counted %d tokens, want %d", got, 12+23+34+45)
	}
	if report.Tokens.RepeatedTotals != 1 || report.Records.Discarded["context/token_repeat"] != 1 ||
		report.Records.Discarded["context/token_count_superseded"] != 2 || !report.Records.Balanced() {
		t.Fatalf("records = %+v tokens = %+v", report.Records, report.Tokens)
	}
}

// A record that names another thread or turn is still counted where it appears,
// but the mismatch is noted.
func TestHostedTokenUsageRecordAttributionMismatchIsNoted(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"a_tool","arguments":"{}","call_id":"a"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "token_usage_record", `{"thread_id":"other","turn_id":"elsewhere","usage":`+hostedUsageJSON(10, 2)+`}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedTotalUsage(graph); got != 12 {
		t.Fatalf("counted %d tokens, want 12", got)
	}
	notes := strings.Join(report.Notes, "\n")
	if !strings.Contains(notes, "names thread other, not root") || !strings.Contains(notes, "names turn elsewhere") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestHostedTokenUsageRecordWithoutUsageIsDiscarded(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"a_tool","arguments":"{}","call_id":"a"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "token_usage_record", `{"thread_id":"thread","turn_id":"turn","usage":{}}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedTotalUsage(graph); got != 0 || report.Records.Discarded["context/token_without_usage"] != 1 || !report.Records.Balanced() {
		t.Fatalf("usage = %d, records = %+v", got, report.Records)
	}
}

// Without records the token_count path stays, and its repeat check spans turns:
// a token_count re-emitted right after the next task_started is a repeat.
func TestHostedTokenCountRepeatedAtStartOfNextTurnCountsOnce(t *testing.T) {
	usage := hostedUsageJSON(10, 2)
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"a_tool","arguments":"{}","call_id":"a"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"a","output":"done"}`),
		hostedCountLine("2026-08-01T12:00:06Z", usage, usage),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"task_started","turn_id":"turn2"}`),
		hostedCountLine("2026-08-01T12:00:09Z", usage, usage),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedTotalUsage(graph); got != 12 {
		t.Fatalf("counted %d tokens, want 12", got)
	}
	if report.Records.Discarded["context/token_repeat"] != 1 || report.Tokens.RepeatedTotals != 1 || !report.Records.Balanced() {
		t.Fatalf("records = %+v tokens = %+v", report.Records, report.Tokens)
	}
}

func TestHostedCompactionItemAndRealtimeItemAreContext(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"compaction","id":"c","encrypted_content":"x"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "realtime_item", `{"type":"realtime_session_started","id":"r","realtime_session_id":"s"}`),
	)
	_, report := hostedBuildLines(t, lines...)
	if report.Records.Discarded["context/compaction_item"] != 1 || report.Records.Discarded["context/realtime_item"] != 1 || !report.Records.Balanced() {
		t.Fatalf("records = %+v", report.Records)
	}
}

func TestHostedPaginatedTokenCountKeepsPromptOwner(t *testing.T) {
	graph, _ := hostedBuildLines(t,
		hostedPaginatedEnvelope("2026-08-01T12:00:00Z", 0, "session_meta", `{"id":"root","history_mode":"paginated","source":"cli"}`),
		hostedPaginatedEnvelope("2026-08-01T12:00:01Z", 1, "event_msg", `{"type":"user_message","message":"Do it"}`),
		hostedPaginatedEnvelope("2026-08-01T12:00:02Z", 2, "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedPaginatedEnvelope("2026-08-01T12:00:03Z", 3, "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedPaginatedEnvelope("2026-08-01T12:00:04Z", 4, "response_item", `{"type":"message","id":"answer","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedPaginatedEnvelope("2026-08-01T12:00:05Z", 5, "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	if len(graph.ModelInvocations) != 1 || graph.ModelInvocations[0].UserInputID != graph.UserInputs[0].ID || graph.ModelInvocations[0].TotalTokens != 23 {
		t.Fatalf("paginated invocation usage = %+v, want 23 tokens owned by prompt", graph.ModelInvocations)
	}
}

func TestHostedTokenCountAfterTurnEndKeepsItsPromptOwner(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	graph, _ := hostedBuildLines(t, lines...)
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

func TestHostedTokenCountAfterTurnEndKeepsPromptSeenBeforeTurnStart(t *testing.T) {
	graph, _ := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"tool","arguments":"{}","call_id":"tool"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"tool","output":"done"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"last_token_usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`),
	)
	if len(graph.TokenUsage) != 1 || graph.TokenUsage[0].UserInputID == nil ||
		*graph.TokenUsage[0].UserInputID != graph.UserInputs[0].ID ||
		graph.TokenUsage[0].Scope != session.UsageScopeInput {
		t.Fatalf("post-turn usage = %+v, want prompt %s", graph.TokenUsage, graph.UserInputs[0].ID)
	}
}

func TestHostedCommitOutcomeNeedsSuccessfulHashEvidence(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"git commit -m x","call_id":"commit","status":"in_progress"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"custom_tool_call_output","call_id":"commit","output":"[main abc1234] x"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"exec_command_end","call_id":"commit","status":"completed","exit_code":0,"stdout":"[main abc1234] x"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := hostedBuildLines(t, lines...)
	if graph.Root().Outcome != session.OutcomeCommitted {
		t.Fatalf("outcome = %q", graph.Root().Outcome)
	}
}

func TestHostedManyExecCompletionsScanInLinearTime(t *testing.T) {
	const completions = 10_000
	output := strings.Repeat("x", 1024)
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"exec_command","input":"git commit -m x","call_id":"commit","status":"in_progress"}`),
	)
	for range completions {
		lines = append(lines, hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"exec_command_end","call_id":"commit","status":"completed","exit_code":0,"stdout":"`+output+`"}`))
	}
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"exec_command_end","call_id":"commit","status":"completed","exit_code":0,"stdout":"[main abc1234] x"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	started := time.Now()
	graph, _ := hostedBuildLines(t, lines...)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("mapping %d exec completions took %v", completions, elapsed)
	}
	if graph.Root().Outcome != session.OutcomeCommitted {
		t.Fatalf("outcome = %q", graph.Root().Outcome)
	}
}

func TestHostedUnprovenUserItemMakesDownstreamTurnUnsupported(t *testing.T) {
	lines := hostedBasicStart()[:3]
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:03Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"Unproven"}]}`),
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Should not persist"}]}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 1 || !report.Records.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if len(graph.UserInputs) != 0 || len(graph.AssistantMessages) != 0 {
		t.Fatalf("unsupported consequences persisted: %+v", graph)
	}
}

func TestHostedConsequenceOutsideATurnBlocksPersistence(t *testing.T) {
	lines := hostedBasicStart()[:1]
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:01Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Unowned"}]}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "response_item", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"true\"}","call_id":"outside"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
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

func TestHostedUnknownToolOutputIsReportedAndBlocksPersistence(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call_output","call_id":"missing","output":"orphan"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 1 || report.Records.Discarded["unsupported tool output"] != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestHostedOnlyStartedSubagentActivityProvesSuccess(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"spawn_agent","arguments":"{}","call_id":"spawn"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"sub_agent_activity","event_id":"spawn","agent_thread_id":"child","agent_path":"/root/a","kind":"failed"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if got := hostedToolForCall(t, graph, "spawn").Status; got != session.StatusUnknown {
		t.Fatalf("spawn status = %q", got)
	}
	if len(graph.ToolResults) != 1 || report.Records.Merged != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}

func TestHostedUserItemsAreAlwaysModelVisibleContext(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"Different input"}]}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Must not persist"}]}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 0 || len(graph.AssistantMessages) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if report.Records.Discarded["context/model_visible_user_copy"] != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}

func hostedUserItem(at, text string) string {
	return hostedEnvelope(at, "event_msg", `{"type":"item_completed","thread_id":"root","turn_id":"turn","item":{"type":"UserMessage","id":"item","client_id":"client","content":[{"type":"text","text":`+fmt.Sprintf("%q", text)+`}]}}`)
}

// Current Codex rollouts record the human prompt only as an item_completed
// UserMessage item. It must create the same inputs a user_message would,
// including a steering message that arrives mid-turn.
func TestHostedItemCompletedUserMessageIsHumanInput(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"Do it"}]}`),
		hostedUserItem("2026-08-01T12:00:04Z", "Do it"),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"message","id":"one","role":"assistant","content":[{"type":"output_text","text":"Working"}]}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"true\"}","call_id":"call"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "response_item", `{"type":"function_call_output","call_id":"call","output":"done"}`),
		hostedUserItem("2026-08-01T12:00:08Z", "Also this"),
		hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"message","id":"two","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:10Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	if report.UnsupportedTurns != 0 || !report.Records.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if len(graph.UserInputs) != 2 {
		t.Fatalf("user inputs = %+v, want 2", graph.UserInputs)
	}
	first, second := graph.UserInputs[0], graph.UserInputs[1]
	if first.Text != "Do it" || second.Text != "Also this" || first.Origin != session.OriginHuman || second.Origin != session.OriginHuman {
		t.Fatalf("inputs = %q/%s and %q/%s", first.Text, first.Origin, second.Text, second.Origin)
	}
	if len(graph.AssistantMessages) != 2 || graph.AssistantMessages[0].UserInputID != first.ID || graph.AssistantMessages[1].UserInputID != second.ID {
		t.Fatalf("assistant messages = %+v", graph.AssistantMessages)
	}
	if len(graph.ToolUses) != 1 || graph.ToolUses[0].UserInputID != first.ID {
		t.Fatalf("tool uses = %+v", graph.ToolUses)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("mapped graph is invalid: %v", err)
	}
}

// An item_completed UserMessage that precedes task_started is pending input,
// exactly like a user_message in that position.
func TestHostedItemCompletedUserMessageBeforeTaskStartIsPending(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedUserItem("2026-08-01T12:00:01Z", "Do it"),
		hostedEnvelope("2026-08-01T12:00:02Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 || len(graph.AssistantMessages) != 1 ||
		graph.AssistantMessages[0].UserInputID != graph.UserInputs[0].ID {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

// When a rollout also writes user_message events, the matching item_completed
// is a copy and must not create a second input.
func TestHostedItemCompletedUserMessageIsCopyWhenUserMessageEventsExist(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedUserItem("2026-08-01T12:00:04Z", "Do it"),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if report.Records.Discarded["context/item_completed_copy"] != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}

// A rollout an older Codex wrote with user_message events and a newer Codex
// resumed has both kinds of turn: the resumed turn's item_completed is its only
// prompt and must still map.
func TestHostedItemCompletedUserMessageMapsInResumedTurnWithoutUserMessage(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedUserItem("2026-08-01T12:00:04Z", "Do it"),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T13:00:00Z", "event_msg", `{"type":"task_started","turn_id":"resumed"}`),
		hostedEnvelope("2026-08-01T13:00:01Z", "turn_context", `{"turn_id":"resumed","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T13:00:02Z", "event_msg", `{"type":"item_completed","thread_id":"root","turn_id":"resumed","item":{"type":"UserMessage","id":"item2","content":[{"type":"text","text":"And again"}]}}`),
		hostedEnvelope("2026-08-01T13:00:03Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done again"}]}`),
		hostedEnvelope("2026-08-01T13:00:04Z", "event_msg", `{"type":"task_complete","turn_id":"resumed"}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 2 || graph.UserInputs[1].Text != "And again" {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
	if report.Records.Discarded["context/item_completed_copy"] != 1 {
		t.Fatalf("records = %+v", report.Records)
	}
}

// A user_message written before task_started still marks its turn, so the
// turn's item_completed stays a copy.
func TestHostedItemCompletedUserMessageIsCopyWhenUserMessagePrecedesTaskStart(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"user_message","message":"Do it"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedUserItem("2026-08-01T12:00:03Z", "Do it"),
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	if report.UnsupportedTurns != 0 || len(graph.UserInputs) != 1 || report.Records.Discarded["context/item_completed_copy"] != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestHostedItemCompletedNonUserItemIsDiscarded(t *testing.T) {
	lines := hostedBasicStart()[:1]
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"item_completed","thread_id":"root","turn_id":"turn","item":{"type":"AgentMessage","content":[{"type":"text","text":"hi"}]}}`),
	)
	graph, report := hostedBuildLines(t, lines...)
	if len(graph.UserInputs) != 0 || report.Records.Discarded["context/item_completed_copy"] != 1 {
		t.Fatalf("graph/report = %+v / %+v", graph, report)
	}
}

func TestHostedMalformedItemCompletedUserMessageFails(t *testing.T) {
	lines := hostedBasicStart()[:1]
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"item_completed","item":{"type":"UserMessage","content":"oops"}}`),
	)
	if _, _, err := hostedBuildLinesResult(t, lines...); err == nil {
		t.Fatal("Build accepted a malformed UserMessage item")
	}
}

// A child thread's item_completed UserMessage is not proven human input, just
// like a child's user_message event.
func TestHostedChildItemCompletedUserMessageIsUnconfirmed(t *testing.T) {
	tree := hostedFixtureTree(t)
	item := func(text string) *codextranscript.EventMsg {
		return &codextranscript.EventMsg{
			Type: "item_completed",
			ItemCompleted: &codextranscript.ItemCompleted{
				Item: []byte(`{"type":"UserMessage","content":[{"type":"text","text":"` + text + `"}]}`),
			},
		}
	}
	// The root's prompts become item_completed items; the child gets one more
	// at the end of its own history.
	for i := range tree.Root.Lines {
		if tree.Root.Lines[i].EventMsg != nil && tree.Root.Lines[i].EventMsg.UserMessage != nil {
			tree.Root.Lines[i].EventMsg = item("Root prompt")
		}
	}
	child := tree.Children[0]
	last := child.Lines[len(child.Lines)-1]
	last.Ordinal++
	last.Class = codextranscript.ClassEventMsg
	last.EventMsg = item("Steer the child")
	child.Lines = append(child.Lines, last)
	graph, report, err := hostedBuild(tree)
	if err != nil {
		t.Fatal(err)
	}
	if report.Records.Discarded["context/unconfirmed_human_input"] == 0 {
		t.Fatalf("records = %+v, want the child item discarded as unconfirmed_human_input", report.Records)
	}
	if len(graph.UserInputs) == 0 {
		t.Fatal("root item_completed produced no inputs")
	}
	for _, input := range graph.UserInputs {
		if input.Text == "Steer the child" {
			t.Fatalf("child item became input %+v", input)
		}
	}
}

// A UserMessage item keeps the images and text_elements a user_message would
// carry as input metadata.
func TestHostedItemCompletedUserMessageKeepsImagesAndTextElements(t *testing.T) {
	graph, report := hostedBuildLines(t,
		hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
		hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		hostedEnvelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
		hostedEnvelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"Look","text_elements":[{"byte_range":{"start":0,"end":4}}]},{"type":"local_image","path":"/tmp/a.png"},{"type":"image","image_url":"data:image/png;base64,AAAA"}]}}`),
	)
	if !report.Records.Balanced() || len(graph.UserInputs) != 1 {
		t.Fatalf("report = %+v inputs = %+v", report.Records, graph.UserInputs)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(graph.UserInputs[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"local_images":  `["/tmp/a.png"]`,
		"images":        `["data:image/png;base64,AAAA"]`,
		"text_elements": `[{"byte_range":{"start":0,"end":4}}]`,
	} {
		if string(metadata[key]) != want {
			t.Errorf("metadata[%s] = %s, want %s", key, metadata[key], want)
		}
	}
}

// Each text part's byte_range is relative to the part, so after joining the
// parts it moves by the byte length of the text before it. A malformed element
// fails the line.
func TestHostedItemCompletedShiftsTextElementOffsets(t *testing.T) {
	lines := func(content string) []string {
		return []string{
			hostedEnvelope("2026-08-01T12:00:00Z", "session_meta", `{"id":"root","session_id":"root","timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":"cli"}`),
			hostedEnvelope("2026-08-01T12:00:01Z", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
			hostedEnvelope("2026-08-01T12:00:02Z", "turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo"}`),
			hostedEnvelope("2026-08-01T12:00:03Z", "event_msg", `{"type":"item_completed","item":{"type":"UserMessage","content":[`+content+`]}}`),
		}
	}
	graph, _ := hostedBuildLines(t, lines(`{"type":"text","text":"Hé ","text_elements":[]},{"type":"text","text":"@file now","text_elements":[{"byte_range":{"start":0,"end":5},"placeholder":"x"}]}`)...)
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(graph.UserInputs[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	// "Hé " is 4 bytes.
	if want := `[{"byte_range":{"end":9,"start":4},"placeholder":"x"}]`; string(metadata["text_elements"]) != want {
		t.Fatalf("text_elements = %s, want %s", metadata["text_elements"], want)
	}
	if _, _, err := hostedBuildLinesResult(t, lines(`{"type":"text","text":"a","text_elements":[{"byte_range":{"start":"x"}}]}`)...); err == nil {
		t.Fatal("malformed text element did not fail the line")
	}
}

func TestHostedGoalUpdateAndImageGenerationAreContext(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "event_msg", `{"type":"thread_goal_updated","threadId":"root","turnId":"turn","goal":{}}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"image_generation_call","id":"ig","status":"generating","result":"AAAA"}`),
	)
	_, report := hostedBuildLines(t, lines...)
	if report.Records.Discarded["context/thread_goal_updated"] != 1 || report.Records.Discarded["context/image_generation_call"] != 1 || !report.Records.Balanced() {
		t.Fatalf("records = %+v", report.Records)
	}
}
