//go:build unix

package codex

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const transcriptEvents = `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i0","type":"reasoning","text":"Plan it."}}
{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"go test","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"go test","aggregated_output":"FAIL","exit_code":1,"status":"completed"}}
{"type":"item.started","item":{"id":"i2","type":"file_change","changes":[{"path":"a.go","kind":"update"}],"status":"in_progress"}}
{"type":"item.completed","item":{"id":"i2","type":"file_change","changes":[{"path":"a.go","kind":"update"},{"path":"b.go","kind":"add"}],"status":"completed"}}
{"type":"item.started","item":{"id":"i3","type":"mcp_tool_call","server":"docs","tool":"search","status":"in_progress"}}
{"type":"item.completed","item":{"id":"i3","type":"mcp_tool_call","server":"docs","tool":"search","status":"failed","error":{"message":"boom"}}}
{"type":"item.completed","item":{"id":"i4","type":"web_search","query":"golang"}}
{"type":"error","message":"reconnecting"}
{"type":"item.completed","item":{"id":"i5","type":"agent_message","text":"Fixed."}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`

func TestFixTranscript(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\ncat <<'EVENTS'\n"+transcriptEvents+"\nEVENTS\n")
	var got []agent.TranscriptEntry
	request := agent.Request{Directory: t.TempDir(), Scratch: t.TempDir(), Prompt: "fix", MaxCalls: 3, MaxTokens: 100, Transcript: func(e agent.TranscriptEntry) { got = append(got, e) }}
	if _, err := (&Backend{Binary: script, Model: "m"}).Fix(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []agent.TranscriptEntry{
		{Kind: "reasoning", Text: "Plan it."},
		{Kind: "tool", ID: "i1", Tool: "command", Title: "go test", Status: "running"},
		{Kind: "tool", ID: "i1", Tool: "command", Title: "go test", Status: "failed", Text: "FAIL"},
		{Kind: "tool", ID: "i2", Tool: "file_change", Title: "update a.go", Status: "running"},
		{Kind: "tool", ID: "i2", Tool: "file_change", Title: "update a.go, add b.go", Status: "completed"},
		{Kind: "tool", ID: "i3", Tool: "mcp_tool_call", Title: "docs search", Status: "running"},
		{Kind: "tool", ID: "i3", Tool: "mcp_tool_call", Title: "docs search", Status: "failed", Text: "boom"},
		{Kind: "tool", ID: "i4", Tool: "web_search", Title: "golang", Status: "completed"},
		{Kind: "error", Text: "reconnecting"},
		{Kind: "message", Text: "Fixed."},
	}
	if len(got) != len(want) {
		t.Fatalf("entries %+v", got)
	}
	for i := range want {
		if got[i].Time.IsZero() {
			t.Errorf("entry %d has no time", i)
		}
		got[i].Time = want[i].Time
		if got[i] != want[i] {
			t.Errorf("entry %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}
