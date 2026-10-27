//go:build unix

package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// A fix's transcript has the messages, reasoning and tools of the run, from
// the complete assistant messages and the tool results.
func TestFixTranscript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := `#!/bin/sh
if [ "$1" = --version ]; then echo '2.1.310 (Claude Code)'; exit 0; fi
cat > /dev/null
cat <<'EVENTS'
{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"fixture"}}}
{"type":"assistant","message":{"id":"m1","model":"fixture","content":[{"type":"thinking","thinking":"Look at it first."}]}}
{"type":"assistant","message":{"id":"m1","model":"fixture","content":[{"type":"text","text":"Reading main.go."}]}}
{"type":"assistant","message":{"id":"m1","model":"fixture","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"package main"}]}}
{"type":"assistant","message":{"id":"m2","model":"fixture","content":[{"type":"tool_use","id":"t2","name":"Grep","input":{"pattern":"TODO"}},{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"x.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"nothing"}]},{"type":"tool_result","tool_use_id":"t3","is_error":true,"content":"not found"}]}}
{"type":"result","subtype":"success","result":"Done.","usage":{"input_tokens":10,"output_tokens":2}}
EVENTS
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	var got []agent.TranscriptEntry
	request := agent.Request{Directory: t.TempDir(), Prompt: "fix", MaxCalls: 4, MaxTokens: 100, Transcript: func(e agent.TranscriptEntry) { got = append(got, e) }}
	if _, err := (&Backend{Binary: script}).Fix(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []agent.TranscriptEntry{
		{Kind: "reasoning", Text: "Look at it first."},
		{Kind: "message", Text: "Reading main.go."},
		{Kind: "tool", ID: "t1", Tool: "Read", Title: "Read main.go", Status: "running"},
		{Kind: "tool", ID: "t1", Tool: "Read", Title: "Read main.go", Status: "completed", Text: "package main"},
		{Kind: "tool", ID: "t2", Tool: "Grep", Title: "Grep TODO", Status: "running"},
		{Kind: "tool", ID: "t3", Tool: "Edit", Title: "Edit x.go", Status: "running"},
		{Kind: "tool", ID: "t2", Tool: "Grep", Title: "Grep TODO", Status: "completed", Text: "nothing"},
		{Kind: "tool", ID: "t3", Tool: "Edit", Title: "Edit x.go", Status: "failed", Text: "not found"},
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
	// Without a Transcript the run is the same.
	if _, err := (&Backend{Binary: script}).Fix(context.Background(), agent.Request{Directory: t.TempDir(), Prompt: "fix", MaxCalls: 4, MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
}
