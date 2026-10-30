package entirecheckpoint

import (
	"errors"
	"strings"
	"testing"
)

const hostedCompactFixture = `{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"user","ts":"2026-08-05T10:00:00Z","content":[{"id":"u1","text":"hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}
{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","ts":"2026-08-05T09:59:59Z","id":"msg_1","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"done"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.go"},"result":{"file":{"filePath":"a.go","numLines":2},"output":"ok","status":"success"}},{"type":"tool_use","id":"toolu_2","name":"Bash"}]}`

func TestHostedDecodeCompactClassifiesV1(t *testing.T) {
	records, err := hostedDecodeCompact(strings.NewReader(hostedCompactFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Type != CompactUser || records[1].Type != CompactAssistant {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Content[0].Kind != CompactUserText || records[0].Content[1].Kind != CompactImage {
		t.Fatalf("user blocks = %+v", records[0].Content)
	}
	tools := records[1].Content
	if tools[1].ToolUse.Result.File.FilePath != "a.go" || tools[2].ToolUse.InputPresent || string(tools[2].ToolUse.Input) != `{}` {
		t.Fatalf("assistant blocks = %+v", tools)
	}
	if records[0].Timestamp == nil || records[1].Timestamp == nil || !records[1].Timestamp.Before(*records[0].Timestamp) {
		t.Fatal("fixture no longer proves timestamps may invert without reordering records")
	}
	if len(records[1].Raw) == 0 || len(records[1].Content[1].Raw) == 0 {
		t.Fatal("source provenance bytes were not retained")
	}
}

func TestHostedDecodeCompactAcceptsCursorShapeWithoutTimesOrToolIDs(t *testing.T) {
	line := `{"v":1,"agent":"cursor","cli_version":"0.8.42","type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"path":"a.go"}}]}`
	records, err := hostedDecodeCompact(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Timestamp != nil || records[0].Content[0].ToolUse.ID != "" {
		t.Fatalf("record = %+v", records[0])
	}
}

func TestHostedDecodeCompactAcceptsCursorApplyPatchStringInput(t *testing.T) {
	line := `{"v":1,"agent":"cursor","cli_version":"0.8.42","type":"assistant","content":[{"type":"tool_use","name":"ApplyPatch","input":"*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"}]}`
	records, err := hostedDecodeCompact(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	tool := records[0].Content[0].ToolUse
	if !tool.InputPresent || string(tool.Input) != `"*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"` {
		t.Fatalf("tool = %+v", tool)
	}
}

// Entire CLI releases through 0.10.6 copy Pi's native tool call items into the
// compact transcript when the Pi session opens with a title record.
func TestHostedDecodeCompactAcceptsPiNativeToolCalls(t *testing.T) {
	line := `{"v":1,"agent":"pi","cli_version":"0.10.6","type":"assistant","ts":"2026-09-10T17:33:26.783Z","id":"41f7c128","content":[` +
		`{"text":"Reproduced the reviewer's claim.","type":"text"},` +
		`{"arguments":{"i":"Reading worktree AGENTS.md","path":".agents/worktrees/pr-188/AGENTS.md"},"id":"call_00","intent":"Reading worktree AGENTS.md","name":"read","partialArgs":"{\"i\": \"Reading\"}","streamIndex":0,"type":"toolCall"},` +
		`{"arguments":{"command":"git status"},"id":"call_01","intent":"Checking the tree","name":"bash","type":"toolCall"},` +
		`{"id":"call_02","name":"hub","type":"toolCall"}]}`
	records, err := hostedDecodeCompact(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	blocks := records[0].Content
	if len(blocks) != 4 || blocks[0].Kind != CompactAssistantText {
		t.Fatalf("blocks = %+v", blocks)
	}
	read := blocks[1]
	if read.Kind != CompactToolUse || read.ID != "call_00" || read.ToolUse.Name != "Read" || !read.ToolUse.InputPresent ||
		string(read.ToolUse.Input) != `{"i":"Reading worktree AGENTS.md","path":".agents/worktrees/pr-188/AGENTS.md"}` || read.ToolUse.Result != nil {
		t.Fatalf("read block = %+v tool = %+v", read, read.ToolUse)
	}
	if !strings.Contains(string(read.Raw), `"partialArgs"`) {
		t.Fatal("source provenance bytes were not retained")
	}
	if bash := blocks[2].ToolUse; bash.Name != "bash" || string(bash.Input) != `{"command":"git status"}` {
		t.Fatalf("bash tool = %+v; only the file tools are renamed, as the CLI's Pi compactor does", bash)
	}
	if hub := blocks[3].ToolUse; hub.InputPresent || string(hub.Input) != `{}` {
		t.Fatalf("argument-less tool = %+v", hub)
	}
}

func TestHostedDecodeCompactRejectsNamedDrift(t *testing.T) {
	tests := []struct {
		name string
		line string
		want error
	}{
		{"version", `{"v":2,"type":"user","ts":"2026-08-05T10:00:00Z","content":[]}`, ErrCompactVersion},
		{"record type", `{"v":1,"type":"system","ts":"2026-08-05T10:00:00Z","content":[]}`, ErrCompactRecordType},
		{"block shape", `{"v":1,"type":"assistant","ts":"2026-08-05T10:00:00Z","content":[{"type":"thinking","text":"x"}]}`, ErrCompactBlockShape},
		{"tool input shape", `{"v":1,"type":"assistant","content":[{"type":"tool_use","name":"Read","input":[]}]}`, ErrCompactBlockShape},
		{"pi toolCall arguments shape", `{"v":1,"type":"assistant","content":[{"type":"toolCall","id":"c","name":"read","arguments":"path"}]}`, ErrCompactBlockShape},
		{"pi toolCall without name", `{"v":1,"type":"assistant","content":[{"type":"toolCall","id":"c","arguments":{}}]}`, ErrCompactBlockShape},
		{"pi toolCall on user line", `{"v":1,"type":"user","content":[{"type":"toolCall","id":"c","name":"read","arguments":{}}]}`, ErrCompactBlockShape},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := hostedDecodeCompact(strings.NewReader(test.line))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestHostedDecodeCompactRejectsTruncatedJSON(t *testing.T) {
	_, err := hostedDecodeCompact(strings.NewReader(`{"v":1,"type":"user"`))
	if err == nil {
		t.Fatal("truncated checkpoint record was accepted")
	}
}
