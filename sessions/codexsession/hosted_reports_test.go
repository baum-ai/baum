package codexsession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

func hostedAgentAnswer(at, author, kind, payload string, trigger bool) []string {
	content, _ := json.Marshal([]map[string]string{{
		"type": "input_text",
		"text": "Message Type: " + kind + "\nTask name: /root\nSender: " + author + "\nPayload:\n" + payload,
	}})
	triggerTurn := "false"
	if trigger {
		triggerTurn = "true"
	}
	return []string{
		hostedEnvelope(at, "inter_agent_communication_metadata", `{"trigger_turn":`+triggerTurn+`}`),
		hostedEnvelope(at, "response_item", `{"type":"agent_message","author":"`+author+`","recipient":"/root","content":`+string(content)+`}`),
	}
}

func hostedResultFor(t *testing.T, graph *session.Graph, use session.ToolUse) session.ToolResult {
	t.Helper()
	for _, result := range graph.ToolResults {
		if result.ToolUseID == use.ID {
			return result
		}
	}
	t.Fatalf("tool use %s (%s) has no result", use.ID, use.Name)
	return session.ToolResult{}
}

func hostedSubagentDetailOf(t *testing.T, use session.ToolUse) string {
	t.Helper()
	var bag map[string]string
	if err := json.Unmarshal(use.Metadata, &bag); err != nil {
		t.Fatalf("metadata of %s: %v", use.Name, err)
	}
	return bag[session.SubagentDetailKey]
}

// A collaboration child's final answer is the report its spawn waited for:
// spawn_agent returns only the child's path, and wait_agent only whether the
// wait completed. A follow-up's answer goes on the follow-up.
func TestHostedFinalAnswerIsTheSpawningCallsReport(t *testing.T) {
	lines := hostedBasicStart()
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"spawn_agent","arguments":"{\"task_name\":\"review\",\"message\":\"Review the change\"}","call_id":"spawn-review"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"spawn-review","output":"{\"task_name\":\"/root/review\"}"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "response_item", `{"type":"function_call","name":"spawn_agent","arguments":"{\"task_name\":\"probe\",\"message\":\"Probe the server\"}","call_id":"spawn-probe"}`),
		hostedEnvelope("2026-08-01T12:00:07Z", "response_item", `{"type":"function_call_output","call_id":"spawn-probe","output":"{\"task_name\":\"/root/probe\"}"}`),
		hostedEnvelope("2026-08-01T12:00:08Z", "response_item", `{"type":"function_call","name":"wait_agent","arguments":"{\"timeout_ms\":60000}","call_id":"wait-1"}`),
	)
	lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:09Z", "/root/review", "MESSAGE", "Reading the diff.", false)...)
	lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:10Z", "/root/review", "FINAL_ANSWER", "No defects found in parse.go.", false)...)
	lines = append(lines,
		hostedEnvelope("2026-08-01T12:00:11Z", "response_item", `{"type":"function_call_output","call_id":"wait-1","output":"{\"message\":\"Wait completed.\",\"timed_out\":false}"}`),
		hostedEnvelope("2026-08-01T12:00:12Z", "response_item", `{"type":"function_call","name":"followup_task","arguments":"{\"target\":\"review\",\"message\":\"Check the tests too\"}","call_id":"followup-review"}`),
		hostedEnvelope("2026-08-01T12:00:13Z", "response_item", `{"type":"function_call_output","call_id":"followup-review","output":""}`),
	)
	lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:14Z", "/root/review", "FINAL_ANSWER", "The tests cover empty input.", true)...)
	graph, report := hostedBuildLines(t, lines...)
	if !report.Records.Balanced() || report.UnsupportedTurns != 0 {
		t.Fatalf("report = %+v, unsupported %d", report.Records, report.UnsupportedTurns)
	}

	review := hostedToolForCall(t, graph, "spawn-review")
	if got := hostedResultFor(t, graph, review); got.OutputText == nil || *got.OutputText != "No defects found in parse.go." {
		t.Fatalf("spawn_agent output text = %v, want the child's final answer", got.OutputText)
	}
	if !hostedJsonContains(t, hostedResultFor(t, graph, review).Output, "/root/review") {
		t.Fatal("spawn_agent lost its own output")
	}
	if got := hostedSubagentDetailOf(t, review); got != string(session.SubagentReport) {
		t.Fatalf("spawn_agent subagent detail = %q, want report", got)
	}
	followup := hostedToolForCall(t, graph, "followup-review")
	if got := hostedResultFor(t, graph, followup); got.OutputText == nil || *got.OutputText != "The tests cover empty input." {
		t.Fatalf("followup_task output text = %v, want the follow-up's final answer", got.OutputText)
	}
	wait := hostedToolForCall(t, graph, "wait-1")
	if got := hostedResultFor(t, graph, wait); got.OutputText == nil || *got.OutputText != `{"message":"Wait completed.","timed_out":false}` {
		t.Fatalf("wait_agent output text = %v, want its own result", got.OutputText)
	}
	probe := hostedToolForCall(t, graph, "spawn-probe")
	if got := hostedSubagentDetailOf(t, probe); got != string(session.SubagentNone) {
		t.Fatalf("unanswered spawn_agent subagent detail = %q, want none", got)
	}
	if got := hostedResultFor(t, graph, probe); got.OutputText == nil || *got.OutputText != `{"task_name":"/root/probe"}` {
		t.Fatalf("unanswered spawn_agent output text = %v", got.OutputText)
	}
}

// A spawn_agent output can be {"task_name": ...} (above), the plain text
// "spawned /root/<name>", or {"agent_id": ...}, which names no path. The
// sub_agent_activity record naming the call gives the path whatever the output
// says, so the child's final answer still finds its spawn when the child's
// rollout was not captured. A spawn with no path keeps subagent_detail none.
func TestHostedFinalAnswerFindsSpawnByActivityOrPlainOutput(t *testing.T) {
	activity := func(at string) string {
		return hostedEnvelope(at, "event_msg", `{"type":"sub_agent_activity","kind":"started","event_id":"spawn-audit","agent_thread_id":"child-thread","agent_path":"/root/audit"}`)
	}
	output := func(at, text string) string {
		encoded, _ := json.Marshal(text)
		return hostedEnvelope(at, "response_item", `{"type":"function_call_output","call_id":"spawn-audit","output":`+string(encoded)+`}`)
	}
	for _, tc := range []struct {
		name   string
		lines  []string
		output string
		want   session.SubagentDetail
	}{
		{"plain text output", []string{output("2026-08-01T12:00:05Z", "spawned /root/audit")}, "spawned /root/audit", session.SubagentReport},
		{"activity after an output without a path", []string{
			output("2026-08-01T12:00:05Z", `{"agent_id":"child-thread"}`), activity("2026-08-01T12:00:06Z"),
		}, `{"agent_id":"child-thread"}`, session.SubagentReport},
		{"activity before the output", []string{
			activity("2026-08-01T12:00:05Z"), output("2026-08-01T12:00:06Z", `{"agent_id":"child-thread"}`),
		}, `{"agent_id":"child-thread"}`, session.SubagentReport},
		{"activity outranks a different output path", []string{
			output("2026-08-01T12:00:05Z", "spawned /root/other"), activity("2026-08-01T12:00:06Z"),
		}, "spawned /root/other", session.SubagentReport},
		{"activity before an output naming a different path", []string{
			activity("2026-08-01T12:00:05Z"), output("2026-08-01T12:00:06Z", "spawned /root/other"),
		}, "spawned /root/other", session.SubagentReport},
		{"no path anywhere", []string{output("2026-08-01T12:00:05Z", `{"agent_id":"child-thread"}`)}, `{"agent_id":"child-thread"}`, session.SubagentNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := hostedBasicStart()
			lines = append(lines, hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"spawn_agent","namespace":"collaboration","arguments":"{\"task\":\"Audit\"}","call_id":"spawn-audit"}`))
			lines = append(lines, tc.lines...)
			lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:09Z", "/root/audit", "FINAL_ANSWER", "The audit found nothing.", false)...)
			graph, report := hostedBuildLines(t, lines...)
			if !report.Records.Balanced() || report.UnsupportedTurns != 0 {
				t.Fatalf("report = %+v, unsupported %d", report.Records, report.UnsupportedTurns)
			}
			spawn := hostedToolForCall(t, graph, "spawn-audit")
			if got := hostedSubagentDetailOf(t, spawn); got != string(tc.want) {
				t.Fatalf("spawn_agent subagent detail = %q, want %q", got, tc.want)
			}
			want := tc.output
			if tc.want == session.SubagentReport {
				want = "The audit found nothing."
			}
			if got := hostedResultFor(t, graph, spawn); got.OutputText == nil || *got.OutputText != want {
				t.Fatalf("spawn_agent output text = %v, want %q", got.OutputText, want)
			}
			// The decision render only reads an OK report.
			if tc.want == session.SubagentReport && spawn.Status != session.StatusOK {
				t.Fatalf("spawn_agent status = %q, want ok once its final answer arrived", spawn.Status)
			}
		})
	}
}

// When the activity record and the output name different paths, the record
// wins in either order, and the short name the spawn was given follows it: a
// followup_task to that name gets the answer of the agent the record named.
func TestHostedFollowupFindsSpawnMovedByActivity(t *testing.T) {
	activity := hostedEnvelope("2026-08-01T12:00:05Z", "event_msg", `{"type":"sub_agent_activity","kind":"started","event_id":"spawn-audit","agent_thread_id":"child-thread","agent_path":"/root/audit"}`)
	output := hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"function_call_output","call_id":"spawn-audit","output":"spawned /root/other"}`)
	for _, tc := range []struct {
		name  string
		order []string
	}{
		{"output then activity", []string{output, activity}},
		{"activity then output", []string{activity, output}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := hostedBasicStart()
			lines = append(lines, hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"function_call","name":"spawn_agent","arguments":"{\"task_name\":\"audit\",\"message\":\"Audit\"}","call_id":"spawn-audit"}`))
			lines = append(lines, tc.order...)
			lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:07Z", "/root/audit", "FINAL_ANSWER", "The audit found nothing.", false)...)
			lines = append(lines,
				hostedEnvelope("2026-08-01T12:00:08Z", "response_item", `{"type":"function_call","name":"followup_task","arguments":"{\"target\":\"audit\",\"message\":\"Check the tests too\"}","call_id":"followup-audit"}`),
				hostedEnvelope("2026-08-01T12:00:09Z", "response_item", `{"type":"function_call_output","call_id":"followup-audit","output":""}`),
			)
			lines = append(lines, hostedAgentAnswer("2026-08-01T12:00:10Z", "/root/audit", "FINAL_ANSWER", "The tests cover empty input.", true)...)
			graph, report := hostedBuildLines(t, lines...)
			if !report.Records.Balanced() || report.UnsupportedTurns != 0 {
				t.Fatalf("report = %+v, unsupported %d", report.Records, report.UnsupportedTurns)
			}
			spawn := hostedToolForCall(t, graph, "spawn-audit")
			if got := hostedResultFor(t, graph, spawn); got.OutputText == nil || *got.OutputText != "The audit found nothing." {
				t.Fatalf("spawn_agent output text = %v, want the child's first final answer", got.OutputText)
			}
			followup := hostedToolForCall(t, graph, "followup-audit")
			if got := hostedResultFor(t, graph, followup); got.OutputText == nil || *got.OutputText != "The tests cover empty input." {
				t.Fatalf("followup_task output text = %v, want the follow-up's final answer", got.OutputText)
			}
			if got := hostedSubagentDetailOf(t, followup); got != string(session.SubagentReport) {
				t.Fatalf("followup_task subagent detail = %q, want report", got)
			}
		})
	}
}

func hostedJsonContains(t *testing.T, raw json.RawMessage, text string) bool {
	t.Helper()
	return json.Valid(raw) && strings.Contains(string(raw), text)
}
