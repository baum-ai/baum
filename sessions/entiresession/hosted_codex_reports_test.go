package entiresession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

// Current records used to fail native decoding, leaving only the compact
// transcript, which carries no child's final report.
func TestHostedCurrentCodexCheckpointPreservesChildReport(t *testing.T) {
	checkpoint := hostedSyntheticCheckpoint(t, "Codex", `{"v":1,"agent":"codex","type":"user","content":[{"text":"Review parser"}]}`, nil, false)
	snapshot := checkpoint.Sessions[0]
	ordinal := 0
	envelope := func(kind string, payload any) string {
		record := map[string]any{"timestamp": "2026-08-05T10:00:00Z", "ordinal": ordinal, "type": kind, "payload": payload}
		ordinal++
		if kind == "compacted" || (kind == "response_item" && payload.(map[string]any)["type"] == "reasoning") {
			delete(record, "ordinal")
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	decode := func(raw string) map[string]any {
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	lines := []string{
		envelope("session_meta", map[string]any{"id": snapshot.Metadata.SessionID, "source": "cli", "history_mode": "paginated"}),
		envelope("event_msg", decode(`{"type":"task_started","turn_id":"turn"}`)),
		envelope("turn_context", decode(`{"turn_id":"turn","model":"gpt-5"}`)),
		envelope("event_msg", decode(`{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage","content":[{"type":"text","text":"Review parser"}]}}`)),
		envelope("response_item", decode(`{"type":"reasoning","summary":[]}`)),
		envelope("response_item", decode(`{"type":"function_call","name":"spawn_agent","arguments":"{\"task_name\":\"review\"}","call_id":"spawn-review"}`)),
		envelope("response_item", decode(`{"type":"function_call_output","call_id":"spawn-review","output":"{\"task_name\":\"/root/review\"}"}`)),
		envelope("token_usage_record", decode(`{"thread_id":"`+snapshot.Metadata.SessionID+`","turn_id":"turn","response_id":"response","usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}`)),
		envelope("compacted", map[string]any{"message": "Context compacted"}),
		envelope("inter_agent_communication_metadata", map[string]any{"trigger_turn": false}),
		envelope("response_item", map[string]any{"type": "agent_message", "author": "/root/review", "recipient": "/root", "content": []map[string]string{{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/review\nPayload:\n" + reviewReport}}}),
	}
	snapshot.FullPresent = true
	snapshot.Full = []byte(strings.Join(lines, "\n") + "\n")
	results, report, err := hostedBuild(checkpoint)
	if err != nil || len(results) != 1 || results[0].Err != nil || !report.Balanced() {
		t.Fatalf("Build: results=%+v report=%+v err=%v", results, report, err)
	}
	if results[0].Fidelity != FidelityDelegated {
		t.Fatalf("fidelity=%v findings=%v", results[0].Fidelity, report.Findings)
	}
	graph := results[0].Graph
	if err := session.Validate(graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.ToolUses) != 1 || hostedDetailOf(t, graph.ToolUses[0]) != session.SubagentReport {
		t.Fatalf("spawn=%+v", graph.ToolUses)
	}
	if got := hostedOutputText(hostedResultOf(t, graph, graph.ToolUses[0])); got != reviewReport {
		t.Fatalf("report=%q", got)
	}
	if len(graph.UserInputs) != 1 || graph.UserInputs[0].Text != "Review parser" {
		t.Fatalf("inputs=%+v", graph.UserInputs)
	}
	if len(graph.Sessions) != 1 {
		t.Fatalf("report-only capture nested %d sessions", len(graph.Sessions))
	}
	// An unsupported future record must still fall back, never silently skip.
	snapshot.Full = []byte(strings.Replace(string(snapshot.Full), `"type":"token_usage_record"`, `"type":"future_usage_record"`, 1))
	results, report, err = hostedBuild(checkpoint)
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Fidelity != FidelityCompact || !report.Balanced() {
		t.Fatalf("fallback: results=%+v report=%+v err=%v", results, report, err)
	}
	if len(results[0].Graph.ToolUses) != 0 {
		t.Fatal("compact fallback invented a child report")
	}
}
