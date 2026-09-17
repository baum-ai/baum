package entirecheckpoint

import (
	"errors"
	"strings"
	"testing"
)

const compactFixture = `{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"user","ts":"2026-08-05T10:00:00Z","content":[{"id":"u1","text":"hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}
{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","ts":"2026-08-05T09:59:59Z","id":"msg_1","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"done"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.go"},"result":{"file":{"filePath":"a.go","numLines":2},"output":"ok","status":"success"}},{"type":"tool_use","id":"toolu_2","name":"Bash"}]}`

func TestDecodeCompactClassifiesV1(t *testing.T) {
	records, err := DecodeCompact(strings.NewReader(compactFixture))
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

func TestDecodeCompactAcceptsCursorShapeWithoutTimesOrToolIDs(t *testing.T) {
	line := `{"v":1,"agent":"cursor","cli_version":"0.8.42","type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"path":"a.go"}}]}`
	records, err := DecodeCompact(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Timestamp != nil || records[0].Content[0].ToolUse.ID != "" {
		t.Fatalf("record = %+v", records[0])
	}
}

func TestDecodeCompactAcceptsCursorApplyPatchStringInput(t *testing.T) {
	line := `{"v":1,"agent":"cursor","cli_version":"0.8.42","type":"assistant","content":[{"type":"tool_use","name":"ApplyPatch","input":"*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"}]}`
	records, err := DecodeCompact(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	tool := records[0].Content[0].ToolUse
	if !tool.InputPresent || string(tool.Input) != `"*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"` {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestDecodeCompactRejectsNamedDrift(t *testing.T) {
	tests := []struct {
		name string
		line string
		want error
	}{
		{"version", `{"v":2,"type":"user","ts":"2026-08-05T10:00:00Z","content":[]}`, ErrCompactVersion},
		{"record type", `{"v":1,"type":"system","ts":"2026-08-05T10:00:00Z","content":[]}`, ErrCompactRecordType},
		{"block shape", `{"v":1,"type":"assistant","ts":"2026-08-05T10:00:00Z","content":[{"type":"thinking","text":"x"}]}`, ErrCompactBlockShape},
		{"tool input shape", `{"v":1,"type":"assistant","content":[{"type":"tool_use","name":"Read","input":[]}]}`, ErrCompactBlockShape},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCompact(strings.NewReader(test.line))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeCompactRejectsTruncatedJSON(t *testing.T) {
	_, err := DecodeCompact(strings.NewReader(`{"v":1,"type":"user"`))
	if err == nil {
		t.Fatal("truncated checkpoint record was accepted")
	}
}
