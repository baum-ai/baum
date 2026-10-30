package cursorsession

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func hostedDynamicCallInput(namespace, tool, arguments string) string {
	return fmt.Sprintf(`{"namespace":%q,"toolName":%q,"arguments":%s}`, namespace, tool, arguments)
}

func hostedBuildDynamic(t *testing.T, inputs ...string) *session.Graph {
	t.Helper()
	lines := []string{`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>go</user_query>"}]}}`}
	var blocks []string
	for _, input := range inputs {
		blocks = append(blocks, fmt.Sprintf(`{"type":"tool_use","name":"CallDynamicTool","input":%s}`, input))
	}
	lines = append(lines,
		`{"role":"assistant","message":{"content":[`+strings.Join(blocks, ",")+`]}}`,
		`{"type":"turn_ended","status":"success"}`)
	records, err := cursortranscript.DecodePortable(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	graph, report, err := hostedBuildPortable("composer-dynamic", "full.jsonl", records, time.Time{})
	if err != nil || !report.Balanced() {
		t.Fatalf("BuildPortable = %v / %+v", err, report)
	}
	return graph
}

func TestHostedDynamicToolsMapToTheirRealNamesAndVerbs(t *testing.T) {
	tests := []struct {
		family, namespace, tool, arguments string
		verb                               session.ToolVerb
	}{
		{"shell", "cursor", "AwaitShell", `{"shell_id":"1","block_until_ms":1000}`, session.VerbExecute},
		{"edit", "cursor", "Delete", `{"path":"/repo/old.go"}`, session.VerbEdit},
		{"web", "cursor", "WebFetch", `{"url":"https://example.com"}`, session.VerbSearch},
		{"web", "cursor", "WebSearch", `{"search_term":"q","explanation":"e"}`, session.VerbSearch},
		{"lint", "cursor", "ReadLints", `{"paths":["/repo/a.go"]}`, session.VerbRead},
		{"subagent", "cursor", "Task", `{"description":"d","prompt":"p","subagent_type":"generalPurpose"}`, session.VerbOther},
		{"plan", "cursor", "TodoWrite", `{"merge":false,"todos":[]}`, session.VerbOther},
		{"meta", "cursor", "GetDynamicTools", `{"pattern":"x"}`, session.VerbOther},
		{"meta", "cursor", "SwitchMode", `{"target_mode_id":"plan"}`, session.VerbOther},
		{"meta", "cursor", "SearchConversations", `{"query":"q"}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_navigate", `{"url":"http://localhost:3000"}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_click", `{"ref":"e1"}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_snapshot", `{}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_take_screenshot", `{"fullPage":true}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_cdp", `{"method":"Page.reload"}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_tabs", `{"action":"list"}`, session.VerbOther},
		{"browser", "cursor-ide-browser", "browser_lock", `{"action":"lock"}`, session.VerbOther},
		{"browser prefix in builtin namespace", "cursor", "browser_hover", `{}`, session.VerbOther},
		{"mcp", "user-tracker", "save_issue", `{"title":"t"}`, session.VerbOther},
		{"mcp", "user-tracker", "list_issues", `{}`, session.VerbOther},
		{"mcp", "user-tracker", "prepare_attachment_upload", `{"name":"a.png"}`, session.VerbOther},
	}
	var inputs []string
	for _, test := range tests {
		inputs = append(inputs, hostedDynamicCallInput(test.namespace, test.tool, test.arguments))
	}
	graph := hostedBuildDynamic(t, inputs...)
	if len(graph.ToolUses) != len(tests) {
		t.Fatalf("tool uses = %d, want %d", len(graph.ToolUses), len(tests))
	}
	for i, test := range tests {
		use := graph.ToolUses[i]
		if use.Name != test.tool || use.Verb != test.verb || string(use.Input) != test.arguments {
			t.Errorf("%s %s/%s = name %q verb %q input %s", test.family, test.namespace, test.tool, use.Name, use.Verb, use.Input)
		}
		var metadata struct {
			Dynamic map[string]string `json:"dynamic_tool"`
		}
		if err := json.Unmarshal(use.Metadata, &metadata); err != nil ||
			metadata.Dynamic["wrapper"] != "CallDynamicTool" || metadata.Dynamic["toolName"] != test.tool || metadata.Dynamic["namespace"] != test.namespace {
			t.Errorf("%s metadata = %s", test.tool, use.Metadata)
		}
	}
}

func TestHostedMCPToolsSharingABuiltinNameAreQualified(t *testing.T) {
	graph := hostedBuildDynamic(t,
		hostedDynamicCallInput("user-tracker", "Task", `{"id":"1"}`),
		hostedDynamicCallInput("user-tracker", "Agent", `{}`),
		hostedDynamicCallInput("user-tracker", "Delete", `{"id":"1"}`),
		hostedDynamicCallInput("user-tracker", "save_issue", `{}`),
	)
	want := []string{"mcp__user-tracker__Task", "mcp__user-tracker__Agent", "mcp__user-tracker__Delete", "save_issue"}
	for i, hostedName := range want {
		if use := graph.ToolUses[i]; use.Name != hostedName || use.Verb != session.VerbOther {
			t.Errorf("tool %d = %q / %q, want %q / other", i, use.Name, use.Verb, hostedName)
		}
		var metadata struct {
			Dynamic map[string]string `json:"dynamic_tool"`
		}
		wantTool := []string{"Task", "Agent", "Delete", "save_issue"}[i]
		if err := json.Unmarshal(graph.ToolUses[i].Metadata, &metadata); err != nil ||
			metadata.Dynamic["wrapper"] != "CallDynamicTool" || metadata.Dynamic["namespace"] != "user-tracker" || metadata.Dynamic["toolName"] != wantTool {
			t.Errorf("tool %d metadata = %s", i, graph.ToolUses[i].Metadata)
		}
	}
}

func TestHostedDynamicDeleteKeepsItsEditTouch(t *testing.T) {
	graph := hostedBuildDynamic(t,
		hostedDynamicCallInput("cursor", "Delete", `{"path":"/repo/old.go"}`),
		hostedDynamicCallInput("user-tracker", "Delete", `{"path":"/repo/not-a-file-tool.go"}`),
	)
	if len(graph.FileTouches) != 1 ||
		graph.FileTouches[0].Path != "/repo/old.go" || graph.FileTouches[0].Kind != session.TouchEdit {
		t.Fatalf("touches = %+v", graph.FileTouches)
	}
}

func TestHostedDynamicReadLintsTouchesEveryPath(t *testing.T) {
	graph := hostedBuildDynamic(t, hostedDynamicCallInput("cursor", "ReadLints", `{"paths":["/repo/a.go","/repo/b.go","/repo/a.go"]}`))
	if len(graph.FileTouches) != 2 ||
		graph.FileTouches[0].Path != "/repo/a.go" || graph.FileTouches[1].Path != "/repo/b.go" ||
		graph.FileTouches[0].Kind != session.TouchRead || graph.FileTouches[1].Kind != session.TouchRead {
		t.Fatalf("touches = %+v", graph.FileTouches)
	}
}

func TestHostedUnknownDynamicToolKeepsTheWrapper(t *testing.T) {
	whole := hostedDynamicCallInput("cursor", "FutureTool", `{"a":1}`)
	graph := hostedBuildDynamic(t, whole,
		`{"namespace":"cursor","arguments":{}}`,
		`"not an object"`,
	)
	for i, use := range graph.ToolUses {
		if use.Name != "CallDynamicTool" || use.Verb != session.VerbOther {
			t.Errorf("tool %d = %q / %q, want the wrapper with verb other", i, use.Name, use.Verb)
		}
	}
	if string(graph.ToolUses[0].Input) != whole {
		t.Errorf("unknown input = %s, want the whole wrapper input", graph.ToolUses[0].Input)
	}
	var metadata struct {
		Dynamic map[string]string `json:"dynamic_tool"`
	}
	if err := json.Unmarshal(graph.ToolUses[0].Metadata, &metadata); err != nil ||
		metadata.Dynamic["toolName"] != "FutureTool" || metadata.Dynamic["namespace"] != "cursor" {
		t.Errorf("unknown metadata = %s", graph.ToolUses[0].Metadata)
	}
	if strings.Contains(string(graph.ToolUses[1].Metadata), "dynamic_tool") {
		t.Errorf("a wrapper with no inner name has dynamic metadata: %s", graph.ToolUses[1].Metadata)
	}
}

func TestHostedDynamicToolWithoutArgumentsStoresAnEmptyObject(t *testing.T) {
	graph := hostedBuildDynamic(t, `{"namespace":"cursor","toolName":"GetDynamicTools"}`)
	if got := string(graph.ToolUses[0].Input); got != `{}` || graph.ToolUses[0].Name != "GetDynamicTools" {
		t.Fatalf("tool = %q %s", graph.ToolUses[0].Name, got)
	}
}
