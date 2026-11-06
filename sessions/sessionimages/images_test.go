package sessionimages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/codexsession"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
)

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

func ptr[T any](v T) *T { return &v }

func fixture(t *testing.T, vendor vendors.Slug, lines ...string) sessions.Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "root.jsonl")
	write(t, path, strings.Join(lines, "\n")+"\n")
	return sessions.Session{ID: "vendor-root", Source: vendor, Path: path, Graph: &session.Graph{
		Sessions:   []session.Session{{ID: "graph-root"}},
		UserInputs: []session.UserInput{{ID: "prompt", SessionID: "graph-root", Origin: session.OriginHuman, Kind: session.KindPrompt}},
		ToolUses:   []session.ToolUse{{ID: "tool", SessionID: "graph-root", UserInputID: "prompt", Name: "browser_screenshot"}},
		VendorIDs: []session.VendorID{
			{OurID: "prompt", OurType: session.EntityUserInput, SessionID: "graph-root", VendorIDKind: "uuid", VendorID: "human", SourceRef: ptr("root.jsonl#1")},
			{OurID: "tool", OurType: session.EntityToolUse, SessionID: "graph-root", VendorIDKind: "tool_use.id", VendorID: "call", SourceRef: ptr("root.jsonl#2")},
		},
	}}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func claudeImage(data string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
}

func TestClaudeInputsAndNestedToolResultsKeepTheirOrigins(t *testing.T) {
	human := `{"type":"user","uuid":"human","timestamp":"2026-11-06T10:00:00Z","message":{"content":[` + claudeImage(png) + `]}}`
	tool := `{"type":"user","uuid":"result","timestamp":"2026-11-06T10:01:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"text","text":"captured"},` + claudeImage(png) + `,` + claudeImage(png) + `]}]}}`
	s := fixture(t, vendors.ClaudeCode, human, tool)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 3 || len(got.Diagnostics) != 0 {
		t.Fatalf("got %d images, diagnostics %v", len(got.Images), got.Diagnostics)
	}
	want, _ := base64.StdEncoding.DecodeString(png)
	for _, image := range got.Images {
		if !bytes.Equal(image.Bytes, want) || image.MediaType != "image/png" || image.Source.SessionID != "vendor-root" || image.Source.TurnID != "prompt" || image.Source.Timestamp == nil {
			t.Fatalf("image %+v", image.Source)
		}
	}
	if got.Images[0].Source.Kind != HumanInput || got.Images[0].Source.ToolUseID != "" {
		t.Fatalf("human origin %+v", got.Images[0].Source)
	}
	if source := got.Images[2].Source; source.Kind != ToolResult || source.ToolUseID != "tool" || source.ToolName != "browser_screenshot" || source.Position != 2 {
		t.Fatalf("tool origin %+v", source)
	}
}

func TestClaudeNativeMapperReferencesResolveToRetainedImages(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"ask","sessionId":"native","timestamp":"2026-11-06T10:00:00Z","message":{"role":"user","content":[` + claudeImage(png) + `]}}`,
		`{"type":"assistant","uuid":"reply","sessionId":"native","timestamp":"2026-11-06T10:00:01Z","message":{"id":"msg","role":"assistant","model":"test","content":[{"type":"tool_use","id":"call","name":"browser_screenshot","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"user","uuid":"result","sessionId":"native","timestamp":"2026-11-06T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[` + claudeImage(png) + `]}]}}`,
	}
	s := fixture(t, vendors.ClaudeCode, lines...)
	decoded, torn, err := claudetranscript.Decode(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil || torn {
		t.Fatalf("decode: %v torn %v", err, torn)
	}
	tree, err := claudetranscript.SingleFileTree(&claudetranscript.File{Path: s.Path, SessionID: "native", Lines: decoded})
	if err != nil {
		t.Fatal(err)
	}
	s.Graph, _, err = claudesession.Build(tree)
	if err != nil {
		t.Fatal(err)
	}
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 2 {
		t.Fatalf("images %d, diagnostics %v", len(got.Images), got.Diagnostics)
	}
	if got.Images[0].Source.Kind != HumanInput || got.Images[1].Source.Kind != ToolResult {
		t.Fatal(got.Images)
	}
}

func TestClaudePastedImagesRequireOrdinaryEnvelopeEvenWithHumanLookingText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		want  int
	}{
		{"ordinary", "", 1},
		{"typed", `"promptSource":"typed",`, 1},
		{"explicit_human", `"promptSource":"typed","origin":{"kind":"human"},`, 1},
		{"null_origin", `"origin":null,`, 1},
		{"meta", `"isMeta":true,`, 0},
		{"system", `"promptSource":"system",`, 0},
		{"peer", `"origin":{"kind":"peer"},`, 0},
		{"task_notification", `"origin":{"kind":"task-notification"},`, 0},
		{"unknown_origin", `"origin":{"kind":"unknown"},`, 0},
		{"unknown_source", `"promptSource":"agent",`, 0},
	} {
		for _, text := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/text=%v", tc.name, text), func(t *testing.T) {
				content := claudeImage(png)
				if text {
					content = `{"type":"text","text":"Fix this screen"},` + content
				}
				line := fmt.Sprintf(`{"type":"user","uuid":"ask","sessionId":"native","timestamp":"2026-11-06T10:00:00Z",%s"message":{"role":"user","content":[%s]}}`, tc.extra, content)
				s := fixture(t, vendors.ClaudeCode, line)
				decoded, _, err := claudetranscript.Decode(strings.NewReader(line + "\n"))
				if err != nil {
					t.Fatal(err)
				}
				tree, err := claudetranscript.SingleFileTree(&claudetranscript.File{Path: s.Path, SessionID: "native", Lines: decoded})
				if err != nil {
					t.Fatal(err)
				}
				s.Graph, _, err = claudesession.Build(tree)
				if err != nil {
					t.Fatal(err)
				}
				got := Read(context.Background(), s, Limits{})
				if len(got.Images) != tc.want {
					t.Fatalf("images %d, want %d; graph inputs %+v", len(got.Images), tc.want, s.Graph.UserInputs)
				}
			})
		}
	}
}

func TestClaudeHumanOriginCannotOverrideHarnessTextWrappers(t *testing.T) {
	for _, text := range []string{
		"<task-notification><task-id>task</task-id>Done</task-notification>",
		"<agent-message from=\"peer\">\nDone\n</agent-message>",
		"<teammate-message teammate_id=\"peer\">Done</teammate-message>",
	} {
		t.Run(text, func(t *testing.T) {
			block, err := json.Marshal(map[string]string{"type": "text", "text": text})
			if err != nil {
				t.Fatal(err)
			}
			line := `{"type":"user","uuid":"human","origin":{"kind":"human"},"promptSource":"typed","message":{"content":[` + string(block) + `,` + claudeImage(png) + `]}}`
			s := fixture(t, vendors.ClaudeCode, line)
			if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 {
				t.Fatal("harness image labeled human")
			}
		})
	}
}

func TestClaudeHarnessEnvelopeStillKeepsToolResultProvenance(t *testing.T) {
	line := `{"type":"user","uuid":"human","isMeta":true,"message":{"content":[{"type":"text","text":"Tool result"},` + claudeImage(png) + `,{"type":"tool_result","tool_use_id":"call","content":[` + claudeImage(png) + `]}]}}`
	s := fixture(t, vendors.ClaudeCode, line)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 1 || got.Images[0].Source.Kind != ToolResult {
		t.Fatalf("images %+v", got.Images)
	}
}

func TestCodexNativeMapperReferencesResolveToRetainedImages(t *testing.T) {
	at := `"timestamp":"2026-11-06T10:00:00Z",`
	image := `{"type":"input_image","image_url":"data:image/png;base64,` + png + `"}`
	lines := []string{
		`{` + at + `"type":"session_meta","payload":{"id":"native","session_id":"native","cwd":"/repo","source":"cli"}}`,
		`{` + at + `"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}`,
		`{` + at + `"type":"turn_context","payload":{"turn_id":"turn","model":"test","cwd":"/repo"}}`,
		`{` + at + `"type":"event_msg","payload":{"type":"user_message","message":"Fix this"}}`,
		`{` + at + `"type":"response_item","payload":{"type":"message","role":"user","content":[` + image + `],"internal_chat_message_metadata_passthrough":{"turn_id":"turn"}}}`,
		`{` + at + `"type":"response_item","payload":{"type":"function_call","call_id":"call","name":"view_image","arguments":"{}"}}`,
		`{` + at + `"type":"response_item","payload":{"type":"function_call_output","call_id":"call","output":[` + image + `]}}`,
	}
	s := fixture(t, vendors.Codex, lines...)
	file, err := codextranscript.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := codextranscript.SingleFileTree(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Graph, _, err = codexsession.Build(tree)
	if err != nil {
		t.Fatal(err)
	}
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 2 {
		t.Fatalf("images %d, diagnostics %v", len(got.Images), got.Diagnostics)
	}
	if got.Images[0].Source.Kind != HumanInput || got.Images[1].Source.Kind != ToolResult {
		t.Fatal(got.Images)
	}
}

func TestCodexPastedImageCanPrecedeHumanEventAndToolOutput(t *testing.T) {
	image := `{"type":"input_image","image_url":"data:image/png;base64,` + png + `"}`
	s := fixture(t, vendors.Codex,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[`+image+`],"internal_chat_message_metadata_passthrough":{"turn_id":"turn"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage"}}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call","output":[`+image+`]}}`,
	)
	s.Graph.VendorIDs[0].VendorIDKind = "rollout.line"
	s.Graph.VendorIDs[0].VendorID = "thread:2"
	s.Graph.VendorIDs[0].SourceRef = ptr("root.jsonl#2")
	s.Graph.VendorIDs[1].VendorIDKind = "call_id"
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 2 {
		t.Fatalf("images %d diagnostics %v", len(got.Images), got.Diagnostics)
	}
	kinds := map[string]bool{}
	for _, image := range got.Images {
		kinds[image.Source.Kind] = true
		if image.Source.TurnID != "prompt" {
			t.Fatal(image.Source)
		}
	}
	if !kinds[HumanInput] || !kinds[ToolResult] {
		t.Fatal(kinds)
	}
}

func TestOnlyReachableNativeEntitiesSupplyImages(t *testing.T) {
	line := `{"type":"user","uuid":"human","message":{"content":[` + claudeImage(png) + `]}}`
	for _, vendor := range []vendors.Slug{sessions.Entire, vendors.Cursor} {
		s := fixture(t, vendor, line)
		if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 || len(got.Diagnostics) == 0 {
			t.Fatalf("%s: %+v", vendor, got)
		}
	}
	s := fixture(t, vendors.ClaudeCode, line)
	s.Graph.UserInputs[0].Text = sessions.GenerationRunMarker
	if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 {
		t.Fatal("generation images included")
	}
	s.Graph.UserInputs[0].Text = "prompt"
	s.Graph.UserInputs[0].Origin = session.OriginSystem
	if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 {
		t.Fatal("system image labeled human")
	}
	s.Graph.UserInputs[0].Origin = session.OriginHuman
	s.Graph.VendorIDs = nil
	if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 {
		t.Fatal("unmapped image included")
	}
}

func TestReachableSubagentKeepsChildAndRootReferences(t *testing.T) {
	s := fixture(t, vendors.ClaudeCode, `{"type":"user","uuid":"human","message":{"content":[{"type":"text","text":"ask"}]}}`)
	s.Graph.Sessions = append(s.Graph.Sessions, session.Session{ID: "child", ParentSessionID: ptr("graph-root"), SpawnedByToolUseID: ptr("tool")})
	s.Graph.UserInputs = append(s.Graph.UserInputs, session.UserInput{ID: "child-prompt", SessionID: "child", Origin: session.OriginAgent})
	s.Graph.ToolUses = append(s.Graph.ToolUses, session.ToolUse{ID: "child-tool", SessionID: "child", UserInputID: "child-prompt", Name: "view_image"})
	s.Graph.VendorIDs = append(s.Graph.VendorIDs,
		session.VendorID{OurID: "child-prompt", OurType: session.EntityUserInput, SessionID: "child", VendorIDKind: "uuid", VendorID: "child-ask", SourceRef: ptr("agent-a.jsonl#1")},
		session.VendorID{OurID: "child-tool", OurType: session.EntityToolUse, SessionID: "child", VendorIDKind: "tool_use.id", VendorID: "child-call", SourceRef: ptr("agent-a.jsonl#2")},
	)
	childPath := filepath.Join(filepath.Dir(s.Path), "root", "subagents", "agent-a.jsonl")
	write(t, childPath, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"child-call","content":[`+claudeImage(png)+`]}]}}`)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 1 {
		t.Fatalf("images %d, diagnostics %v", len(got.Images), got.Diagnostics)
	}
	if source := got.Images[0].Source; source.SessionID != "vendor-root" || source.ChildSessionID != "child" || source.TurnID != "child-prompt" || source.ToolUseID != "child-tool" {
		t.Fatal(source)
	}
	s.Graph.Sessions[1].SpawnedByToolUseID = ptr("absent")
	if got := Read(context.Background(), s, Limits{}); len(got.Images) != 0 {
		t.Fatal("unreachable subagent included")
	}
}

func TestCodexChildTranscriptIsResolvedOnlyForReachableGraphSources(t *testing.T) {
	s := fixture(t, vendors.Codex, `{}`)
	s.Path = filepath.Join(t.TempDir(), "sessions", "2026", "11", "06", "root.jsonl")
	write(t, s.Path, `{}`)
	s.Graph.Sessions = append(s.Graph.Sessions, session.Session{ID: "child", ParentSessionID: ptr("graph-root"), SpawnedByToolUseID: ptr("tool")})
	s.Graph.UserInputs = append(s.Graph.UserInputs, session.UserInput{ID: "child-prompt", SessionID: "child", Origin: session.OriginAgent})
	s.Graph.ToolUses = append(s.Graph.ToolUses, session.ToolUse{ID: "child-tool", SessionID: "child", UserInputID: "child-prompt", Name: "view_image"})
	s.Graph.VendorIDs = append(s.Graph.VendorIDs,
		session.VendorID{OurID: "child-prompt", OurType: session.EntityUserInput, SessionID: "child", VendorIDKind: "rollout.line", VendorID: "thread:1", SourceRef: ptr("rollout-child.jsonl#1")},
		session.VendorID{OurID: "child-tool", OurType: session.EntityToolUse, SessionID: "child", VendorIDKind: "call_id", VendorID: "child-call", SourceRef: ptr("rollout-child.jsonl#2")},
	)
	store := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(s.Path))))
	write(t, filepath.Join(store, "2026", "11", "05", "rollout-child.jsonl"), `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"child-call","output":[{"type":"image","mimeType":"image/png","data":"`+png+`"}]}}`)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 1 || got.Images[0].Source.ChildSessionID != "child" || got.Images[0].Source.ToolUseID != "child-tool" {
		t.Fatalf("images %d diagnostics %v", len(got.Images), got.Diagnostics)
	}
}

func TestMalformedAndOversizedImagesDoNotDiscardTheirSiblings(t *testing.T) {
	s := fixture(t, vendors.ClaudeCode,
		`{"type":"user","uuid":"human","message":{"content":[`+claudeImage("bad-base64")+`,`+claudeImage(png)+`]}}`,
	)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 1 || len(got.Diagnostics) != 1 || got.Images[0].Source.Position != 2 {
		t.Fatalf("images %d, diagnostics %v", len(got.Images), got.Diagnostics)
	}
	got = Read(context.Background(), s, Limits{MaxImageBytes: 10})
	if len(got.Images) != 0 || len(got.Diagnostics) < 2 {
		t.Fatalf("limits: %+v", got)
	}
	got = Read(context.Background(), s, Limits{MaxLineBytes: 30})
	if len(got.Images) != 0 || len(got.Diagnostics) == 0 {
		t.Fatal("oversized line accepted")
	}
	got = Read(context.Background(), s, Limits{MaxScanBytes: 10})
	if len(got.Images) != 0 || len(got.Diagnostics) == 0 {
		t.Fatal("scan limit ignored")
	}
}

func TestCountAndByteBudgetsBoundDecodedImages(t *testing.T) {
	s := fixture(t, vendors.ClaudeCode, `{"type":"user","uuid":"human","message":{"content":[`+claudeImage(png)+`,`+claudeImage(png)+`,`+claudeImage(png)+`]}}`)
	if got := Read(context.Background(), s, Limits{MaxImages: 1}); len(got.Images) != 1 {
		t.Fatal(len(got.Images))
	}
	image, _ := base64.StdEncoding.DecodeString(png)
	got := Read(context.Background(), s, Limits{MaxTotalBytes: int64(len(image))})
	if len(got.Images) != 1 || len(got.Diagnostics) == 0 {
		t.Fatalf("images %d diagnostic %v", len(got.Images), got.Diagnostics)
	}
}

func TestNoExternalImagePathsOrURLsAreRead(t *testing.T) {
	s := fixture(t, vendors.ClaudeCode, `{"type":"user","uuid":"human","message":{"content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/secret.png"}},{"type":"image","path":"/tmp/private.png"}]}}`)
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 0 || len(got.Diagnostics) != 2 {
		t.Fatalf("%+v", got)
	}
	encoded, _ := json.Marshal(got.Diagnostics)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private.png") {
		t.Fatal("diagnostics expose payload")
	}
}

func TestTranscriptSymlinkCannotEscapeNativeDirectory(t *testing.T) {
	s := fixture(t, vendors.ClaudeCode, `{}`)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	write(t, outside, `{"type":"user","uuid":"human","message":{"content":[`+claudeImage(png)+`]}}`)
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.Path); err != nil {
		t.Skip(err)
	}
	got := Read(context.Background(), s, Limits{})
	if len(got.Images) != 0 || len(got.Diagnostics) == 0 {
		t.Fatalf("external symlink accepted: %+v", got)
	}
}

func ExampleRead() {
	result := Read(context.Background(), sessions.Session{ID: "checkpoint", Source: sessions.Entire}, Limits{})
	fmt.Println(len(result.Images), result.Diagnostics[0].Reason)
	// Output: 0 native image source unsupported
}
