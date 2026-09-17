package claudetranscript

import (
	"strings"
	"testing"
)

// envelope is a minimal well-formed assistant line; tests remove from it.
const envelope = `{"type":"assistant","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z",` +
	`"message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"hi"}]}}`

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a line that is not a JSON object",
			in:   envelope + "\n[1,2,3]\n",
			want: "is not a JSON object",
		},
		{
			name: "a line with no type",
			in:   `{"uuid":"a","sessionId":"S"}` + "\n" + envelope + "\n",
			want: "has no type",
		},
		{
			name: "an envelope with no uuid",
			in:   `{"type":"assistant","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z","message":{"id":"m"}}` + "\n",
			want: "envelope with no uuid",
		},
		{
			name: "an envelope with no timestamp",
			in:   `{"type":"assistant","uuid":"a","sessionId":"S","message":{"id":"m"}}` + "\n",
			want: "envelope with no timestamp",
		},
		{
			name: "an assistant line with no message id",
			in:   `{"type":"assistant","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z","message":{"role":"assistant"}}` + "\n",
			want: "no message id",
		},
		{
			name: "an unreadable timestamp",
			in:   `{"type":"assistant","uuid":"a","sessionId":"S","timestamp":"yesterday","message":{"id":"m"}}` + "\n",
			want: "unreadable timestamp",
		},
		{
			name: "a uuid used twice in one file",
			in:   envelope + "\n" + envelope + "\n",
			want: "repeats the uuid",
		},
		{
			name: "one tool use id on two lines",
			in: `{"type":"assistant","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z",` +
				`"message":{"id":"m1","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]}}` + "\n" +
				`{"type":"assistant","uuid":"b","sessionId":"S","timestamp":"2026-07-30T09:00:01.000Z",` +
				`"message":{"id":"m2","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]}}` + "\n",
			want: "repeats tool use id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Decode(strings.NewReader(tt.in))
			if err == nil {
				t.Fatalf("want an error mentioning %q, got none", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error mentioning %q, got: %v", tt.want, err)
			}
		})
	}
}

// A control line has neither uuid nor timestamp, which is normal: 9 of the 12
// control types in the corpus carry no time at all.
func TestDecodeAcceptsControlLinesWithNoIDOrTime(t *testing.T) {
	lines, _, err := Decode(strings.NewReader(`{"type":"mode","mode":"plan","sessionId":"S"}` + "\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("decoded %d lines, want 1", len(lines))
	}
	if lines[0].Class != ClassControl {
		t.Fatalf("class = %q, want %q", lines[0].Class, ClassControl)
	}
	if lines[0].Control.Mode != "plan" {
		t.Fatalf("mode = %q, want plan", lines[0].Control.Mode)
	}
}

func TestDecodeToleratesUnrecognisedAttachmentPayloadShapes(t *testing.T) {
	for _, payload := range []string{`"not-an-object"`, `{"type":42,"timestamp":"not-a-time"}`} {
		line := `{"type":"attachment","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z","attachment":` + payload + `}`
		lines, _, err := Decode(strings.NewReader(line + "\n"))
		if err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if len(lines) != 1 || lines[0].Class != ClassAttachment || lines[0].Attachment.Type != "" {
			t.Fatalf("line = %+v", lines)
		}
	}
}

// Most transcripts on disk end without a newline, and one still being written can
// end mid-record. Neither is corruption, and neither may lose the lines before it.
func TestDecodeToleratesAnIncompleteFinalLine(t *testing.T) {
	t.Run("no trailing newline", func(t *testing.T) {
		lines, torn, err := Decode(strings.NewReader(envelope))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if torn {
			t.Error("a complete line without a trailing newline was reported as torn")
		}
		if len(lines) != 1 {
			t.Fatalf("decoded %d lines, want 1", len(lines))
		}
	})

	t.Run("half-written final line", func(t *testing.T) {
		lines, torn, err := Decode(strings.NewReader(envelope + "\n" + `{"type":"assis`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !torn {
			t.Error("a half-written final line was not reported")
		}
		if len(lines) != 1 {
			t.Fatalf("decoded %d lines, want the one complete line", len(lines))
		}
	})
}

func TestDecodeClassifiesUserContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    UserKind
	}{
		{"a string prompt", `"do the thing"`, UserText},
		{
			// The transcript model sends this shape to user_other. It is a real
			// prompt: 114 user lines in the corpus use it, and it is the first
			// conversational line of a dozen transcripts.
			name:    "an array of text blocks",
			content: `[{"type":"text","text":"do the thing"}]`,
			want:    UserText,
		},
		{"tool results only", `[{"type":"tool_result","tool_use_id":"t","content":"out"}]`, UserToolResults},
		{"tool results beside text", `[{"type":"tool_result","tool_use_id":"t"},{"type":"text","text":"and"}]`, UserMixed},
		{"an image block", `[{"type":"image","source":{}}]`, UserOther},
		{"an empty array", `[]`, UserOther},
		{"a number", `12`, UserOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"type":"user","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z",` +
				`"message":{"role":"user","content":` + tt.content + `}}`
			lines, _, err := Decode(strings.NewReader(line))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := lines[0].UserKind; got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMessageTextReadsBothShapes(t *testing.T) {
	string_ := Message{Content: []byte(`"typed this"`)}
	if got := string_.Text(); got != "typed this" {
		t.Fatalf("string content text = %q", got)
	}
	array := Message{Content: []byte(`[{"type":"text","text":"first"},{"type":"thinking"},{"type":"text","text":"second"}]`)}
	if got := array.Text(); got != "first\nsecond" {
		t.Fatalf("array content text = %q, want the text blocks joined", got)
	}
}

func TestResultTextReadsBothShapes(t *testing.T) {
	if got := (Block{Content: []byte(`"plain output"`)}).ResultText(); got != "plain output" {
		t.Fatalf("string result = %q", got)
	}
	block := Block{Content: []byte(`[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`)}
	if got := block.ResultText(); got != "line one\nline two" {
		t.Fatalf("array result = %q", got)
	}
}

// An unrecognised type is kept and marked, not dropped: that is how a format
// change shows up as a number rather than as missing data.
func TestDecodeKeepsUnrecognisedTypes(t *testing.T) {
	lines, _, err := Decode(strings.NewReader(`{"type":"something-new","sessionId":"S","payload":1}` + "\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if lines[0].Class != ClassUnknownControl {
		t.Fatalf("class = %q, want %q", lines[0].Class, ClassUnknownControl)
	}
	if !strings.Contains(string(lines[0].Raw), "something-new") {
		t.Fatal("the original line was not retained")
	}
}
