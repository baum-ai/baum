package entiresession

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/baumai/baum/sessions/entirecheckpoint"
	"github.com/baumai/baum/sessions/session"
)

func TestCompactStatusVerbTouchAndOriginRules(t *testing.T) {
	compact := strings.Join([]string{
		`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"do it"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}`,
		`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"assistant","content":[` +
			`{"type":"text","text":"working"},` +
			`{"type":"tool_use","name":"Read","input":{"path":"/repo/src/a.go"},"result":{"file":{"filePath":"/repo/src/a.go"},"output":"ok","status":"success"}},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/src/b.go"},"result":{"output":"no","status":"error"}},` +
			`{"type":"tool_use","name":"Task","input":{"prompt":"check"}},` +
			`{"type":"tool_use","name":"apply_patch","input":{"input":"*** Begin Patch\n*** Add File: /repo/src/c.go\n+x\n*** End Patch"},"result":{"output":"done","status":"success"}}]}`,
	}, "\n")
	checkpoint := syntheticCheckpoint(t, "Cursor", compact, []string{"src/a.go", "src/missing.go"}, true)
	results, report, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	graph := results[0].Graph
	if graph.UserInputs[0].Origin != session.OriginHuman || graph.UserInputs[0].Kind != session.KindPrompt {
		t.Fatalf("input = %+v", graph.UserInputs[0])
	}
	if !strings.Contains(string(graph.UserInputs[0].Metadata), `"image":true`) {
		t.Fatalf("input metadata = %s", graph.UserInputs[0].Metadata)
	}
	wantStatus := []session.ToolStatus{session.StatusOK, session.StatusError, session.StatusNoResult, session.StatusOK}
	for index, want := range wantStatus {
		if graph.ToolUses[index].Status != want {
			t.Errorf("tool %d status = %q, want %q", index, graph.ToolUses[index].Status, want)
		}
	}
	if graph.ToolUses[2].Verb != session.VerbOther {
		t.Fatalf("Task verb = %q; compact sessions cannot claim a nested spawn", graph.ToolUses[2].Verb)
	}
	paths := map[string]session.TouchKind{}
	for _, touch := range graph.FileTouches {
		paths[touch.Path] = touch.Kind
	}
	for path, want := range map[string]session.TouchKind{
		"/repo/src/a.go": session.TouchRead,
		"/repo/src/b.go": session.TouchEdit,
		"/repo/src/c.go": session.TouchEdit,
	} {
		if paths[path] != want {
			t.Errorf("touch %s = %q, want %q; all=%+v", path, paths[path], want, paths)
		}
	}
	if report.Envelope.FilesAlreadyCovered != 1 || report.Envelope.FilesUnrepresentable != 1 {
		t.Fatalf("file envelope report = %+v", report.Envelope)
	}
	if results[0].Fidelity != FidelityCompact || graph.Root().Outcome != session.OutcomeCommitted || graph.Root().GitBranch == nil {
		t.Fatalf("result/root = %+v / %+v", results[0], graph.Root())
	}
	if graph.Root().StartedAt == nil || graph.Root().EndedAt == nil || !graph.Root().StartedAt.Equal(results[0].CreatedAt) || !graph.Root().EndedAt.Equal(results[0].CreatedAt) {
		t.Fatalf("timestamp-less compact root did not use created_at fallback: %+v", graph.Root())
	}
}

func TestCursorUsesPortableDelegateWhenReadable(t *testing.T) {
	compact := `{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"compact"}]}`
	checkpoint := syntheticCheckpoint(t, "Cursor", compact, nil, false)
	checkpoint.Sessions[0].Full = []byte(strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>portable</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
	}, "\n"))
	checkpoint.Sessions[0].FullPresent = true

	results, report, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Fidelity != FidelityDelegated || results[0].Graph.UserInputs[0].Text != "portable" {
		t.Fatalf("result = %+v", results[0])
	}
	if len(report.Delegated) != 1 || report.Delegated[0].Cursor == nil || !report.Balanced() {
		t.Fatalf("report = %+v", report)
	}
}

func TestCursorFallsBackToCompactWhenPortableParsingFails(t *testing.T) {
	compact := `{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"compact"}]}`
	checkpoint := syntheticCheckpoint(t, "Cursor", compact, nil, false)
	checkpoint.Sessions[0].Full = []byte(`{"role":"unknown"}`)
	checkpoint.Sessions[0].FullPresent = true

	results, report, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Fidelity != FidelityCompact || results[0].Graph.UserInputs[0].Text != "compact" ||
		len(results[0].Warnings) != 1 || !strings.Contains(results[0].Warnings[0], "used compact fallback") {
		t.Fatalf("result = %+v", results[0])
	}
	if len(report.Delegated) != 0 || !report.Balanced() {
		t.Fatalf("report = %+v", report)
	}
}

func TestCompactVerbTable(t *testing.T) {
	tests := map[string]session.ToolVerb{
		"Read": session.VerbRead, "ReadFile": session.VerbRead, "ListDir": session.VerbRead, "ReadLints": session.VerbRead, "NotebookRead": session.VerbRead,
		"Edit": session.VerbEdit, "Write": session.VerbEdit, "NotebookEdit": session.VerbEdit, "apply_patch": session.VerbEdit,
		"StrReplace": session.VerbEdit, "ApplyPatch": session.VerbEdit, "EditNotebook": session.VerbEdit, "Delete": session.VerbEdit,
		"Grep": session.VerbSearch, "Glob": session.VerbSearch, "WebSearch": session.VerbSearch, "WebFetch": session.VerbSearch, "ToolSearch": session.VerbSearch,
		"rg": session.VerbSearch, "Codebase": session.VerbSearch,
		"Bash": session.VerbExecute, "BashOutput": session.VerbExecute, "exec": session.VerbExecute, "exec_command": session.VerbExecute,
		"write_stdin": session.VerbExecute, "run": session.VerbExecute, "Shell": session.VerbExecute, "AwaitShell": session.VerbExecute,
		"Agent": session.VerbOther, "Task": session.VerbOther, "spawn_agent": session.VerbOther, "mcp__server__tool": session.VerbOther,
		"ExitPlanMode": session.VerbOther,
	}
	for name, want := range tests {
		if got := compactVerb(name); got != want {
			t.Errorf("%s verb = %q, want %q", name, got, want)
		}
	}
}

func TestCompactAssistantIDsAreScopedToTheirUserInput(t *testing.T) {
	compact := strings.Join([]string{
		`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"user","content":[{"text":"first"}]}`,
		`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","id":"msg_repeat","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"part one"}]}`,
		`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","id":"msg_repeat","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"part two"}]}`,
		`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"user","content":[{"text":"second"}]}`,
		`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","id":"msg_repeat","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"resumed copy"}]}`,
	}, "\n")
	results, _, err := Build(syntheticCheckpoint(t, "Other Claude Wrapper", compact, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	graph := results[0].Graph
	if len(graph.ModelInvocations) != 2 || len(graph.AssistantMessages) != 3 {
		t.Fatalf("invocations/messages = %d/%d, want 2/3", len(graph.ModelInvocations), len(graph.AssistantMessages))
	}
	if graph.AssistantMessages[0].ModelInvocationID != graph.AssistantMessages[1].ModelInvocationID {
		t.Fatal("streamed records with one assistant id were split within a user input")
	}
	if graph.AssistantMessages[1].ModelInvocationID == graph.AssistantMessages[2].ModelInvocationID {
		t.Fatal("a repeated assistant id was merged across user inputs")
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("scoped assistant ids produced an invalid graph: %v", err)
	}
}

func TestCompactStructuredTouchesKeepTheirEvidenceKind(t *testing.T) {
	compact := strings.Join([]string{
		`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"edit"}]}`,
		`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"assistant","content":[` +
			`{"type":"tool_use","name":"StrReplace","input":{"path":"/repo/replace.go"}},` +
			`{"type":"tool_use","name":"ApplyPatch","input":"*** Begin Patch\n*** Update File: /repo/old.go\n*** Move to: /repo/new.go\n@@\n-old\n+new\n*** End Patch"},` +
			`{"type":"tool_use","name":"ListDir","input":{"path":"/repo"}},` +
			`{"type":"tool_use","name":"ExitPlanMode","input":{"planFilePath":"/repo/not-touched.md"}},` +
			`{"type":"tool_use","name":"ExitPlanMode","input":{},"result":{"file":{"filePath":"/repo/written-plan.md"},"output":"ok","status":"success"}}]}`,
	}, "\n")
	results, _, err := Build(syntheticCheckpoint(t, "Cursor", compact, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	graph := results[0].Graph
	if graph.ToolUses[0].Verb != session.VerbEdit || graph.ToolUses[1].Verb != session.VerbEdit {
		t.Fatalf("Cursor edit verbs = %q/%q", graph.ToolUses[0].Verb, graph.ToolUses[1].Verb)
	}
	if graph.ToolUses[2].Verb != session.VerbRead {
		t.Fatalf("ListDir verb = %q, want read", graph.ToolUses[2].Verb)
	}
	if graph.ToolUses[3].Verb != session.VerbOther || graph.ToolUses[4].Verb != session.VerbOther {
		t.Fatalf("ExitPlanMode verbs = %q/%q, want other", graph.ToolUses[3].Verb, graph.ToolUses[4].Verb)
	}
	paths := map[string]session.TouchKind{}
	for _, touch := range graph.FileTouches {
		paths[touch.Path] = touch.Kind
	}
	for _, path := range []string{"/repo/replace.go", "/repo/old.go", "/repo/new.go", "/repo/written-plan.md"} {
		if paths[path] != session.TouchEdit {
			t.Errorf("touch %s = %q, want edit; all=%+v", path, paths[path], paths)
		}
	}
	if _, exists := paths["/repo/not-touched.md"]; exists {
		t.Fatal("ExitPlanMode input alone created a file touch")
	}
	if _, exists := paths["/repo"]; exists {
		t.Fatal("ListDir created a file touch for a directory")
	}
}

func TestAggregateUsageAttributionBothWays(t *testing.T) {
	t.Run("invocation usage keeps aggregate as attribute", func(t *testing.T) {
		compact := `{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"user","ts":"2026-08-05T10:00:00Z","content":[{"text":"go"}]}` + "\n" +
			`{"v":1,"agent":"claude-code","cli_version":"0.9.0","type":"assistant","ts":"2026-08-05T10:00:01Z","id":"msg_1","input_tokens":3,"output_tokens":4,"content":[{"type":"text","text":"ok"}]}`
		results, report, err := Build(syntheticCheckpoint(t, "Other Claude Wrapper", compact, nil, true))
		if err != nil {
			t.Fatal(err)
		}
		if len(results[0].Graph.TokenUsage) != 0 || report.Envelope.AggregateUsageAttributed != 1 || !hasAttribute(results[0].Graph, "token_usage") {
			t.Fatalf("graph/report = %+v / %+v", results[0].Graph, report.Envelope)
		}
		root := results[0].Graph.Root()
		if root.StartedAt == nil || root.EndedAt == nil || root.StartedAt.Format(time.RFC3339) != "2026-08-05T10:00:00Z" || root.EndedAt.Format(time.RFC3339) != "2026-08-05T10:00:01Z" {
			t.Fatalf("compact time range = %v to %v", root.StartedAt, root.EndedAt)
		}
	})

	t.Run("no invocation usage emits a session aggregate", func(t *testing.T) {
		compact := `{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"go"}]}` + "\n" +
			`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"assistant","content":[{"type":"text","text":"ok"}]}`
		results, report, err := Build(syntheticCheckpoint(t, "Cursor", compact, nil, true))
		if err != nil {
			t.Fatal(err)
		}
		graph := results[0].Graph
		if len(graph.TokenUsage) != 1 || graph.TokenUsage[0].Scope != session.UsageScopeSession || graph.TokenUsage[0].APICallCount == nil || *graph.TokenUsage[0].APICallCount != 2 || graph.TokenUsage[0].Model == nil || *graph.TokenUsage[0].Model != "model" || report.Envelope.AggregateUsageAdded != 1 {
			t.Fatalf("graph/report = %+v / %+v", graph, report.Envelope)
		}
		if !hasAttribute(graph, "token_usage") {
			t.Fatal("raw token_usage attribute needed to preserve api_call_count is absent")
		}
	})
}

func TestCompactLocatorsAreDeterministic(t *testing.T) {
	compact := `{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"go"}]}` + "\n" +
		`{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Task","input":{}}]}`
	checkpoint := syntheticCheckpoint(t, "Cursor", compact, nil, false)
	one, _, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(graphIDs(one[0].Graph)) != fmt.Sprint(graphIDs(two[0].Graph)) {
		t.Fatal("rebuilding the same compact source changed row ids")
	}
}

func TestMetadataOnlyPromptSegments(t *testing.T) {
	checkpoint := syntheticCheckpoint(t, "Unknown Agent", "", nil, false)
	checkpoint.Sessions[0].PromptPresent = true
	checkpoint.Sessions[0].Prompt = []byte("first\n---\nsecond")
	results, report, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Fidelity != FidelityMetadataOnly || len(results[0].Graph.UserInputs) != 2 || report.Sessions != 1 {
		t.Fatalf("results/report = %+v / %+v", results, report)
	}
}

func TestBuildIsolatesSnapshotFailures(t *testing.T) {
	checkpoint := syntheticCheckpoint(t, "Cursor", `{"v":1,"agent":"cursor","cli_version":"0.9.0","type":"user","content":[{"text":"go"}]}`, nil, false)
	good := checkpoint.Sessions[0]
	bad := *good
	bad.Metadata = good.Metadata
	bad.Metadata.SessionID = "bad-session"
	bad.Metadata.CreatedAt = "not-a-timestamp"
	checkpoint.Sessions = []*entirecheckpoint.SessionSnapshot{&bad, good}

	results, report, err := Build(checkpoint)
	if err != nil {
		t.Fatalf("checkpoint-level build error: %v", err)
	}
	if len(results) != 2 || results[0].Err == nil || results[1].Err != nil || results[1].Graph == nil {
		t.Fatalf("results = %+v", results)
	}
	if report.Sessions != 2 || report.Rows.Sessions != 1 || report.FailedSnapshots != 1 || !report.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if report.Metadata.Discarded["failed/metadata"] == 0 || report.Compact.Lines.Discarded["failed/metadata"] == 0 {
		t.Fatalf("failed snapshot records were not accounted by reason: %+v", report)
	}
}

func TestBuildRejectsNilCheckpointAtCheckpointLevel(t *testing.T) {
	results, report, err := Build(nil)
	if err == nil || results != nil || report != nil {
		t.Fatalf("results/report/error = %+v / %+v / %v", results, report, err)
	}
}

func hasAttribute(graph *session.Graph, key string) bool {
	for _, attribute := range graph.Attributes {
		if attribute.Key == key {
			return true
		}
	}
	return false
}

// Entire hands every session in a checkpoint the checkpoint's own diff often
// enough that an identical list on two sessions cannot be read as either one's
// edits. Build flags it so the ingest layer keeps it out of the evidence.
func TestBuildFlagsFilesTouchedSharedAcrossSessions(t *testing.T) {
	shared := metadataOnlyCheckpoint(t, [][]string{{"a.go", "b.go"}, {"b.go", "a.go"}})
	results, report, err := Build(shared)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Err != nil || !result.Envelope.MetadataFilesShared {
			t.Fatalf("result = %+v, want the shared list flagged", result)
		}
	}
	if report.Envelope.FilesShared != 4 || !containsFinding(report.Findings, "attributed to no session") {
		t.Fatalf("report = %+v", report.Envelope)
	}

	own := metadataOnlyCheckpoint(t, [][]string{{"a.go", "b.go"}, {"b.go"}, {}, {}})
	results, report, err = Build(own)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Err != nil || result.Envelope.MetadataFilesShared {
			t.Fatalf("result = %+v, want distinct or empty lists left alone", result)
		}
	}
	if report.Envelope.FilesShared != 0 {
		t.Fatalf("report = %+v", report.Envelope)
	}
}

func containsFinding(findings []string, substring string) bool {
	for _, finding := range findings {
		if strings.Contains(finding, substring) {
			return true
		}
	}
	return false
}

// metadataOnlyCheckpoint builds one checkpoint holding a metadata-only session
// per files_touched list, so the only file evidence in play is the metadata.
func metadataOnlyCheckpoint(t *testing.T, lists [][]string) *entirecheckpoint.Checkpoint {
	t.Helper()
	fsys := fstest.MapFS{}
	refs := make([]string, len(lists))
	for index, files := range lists {
		filesJSON, err := json.Marshal(files)
		if err != nil {
			t.Fatal(err)
		}
		refs[index] = fmt.Sprintf(`{"metadata":"/ab/cdef123456/%d/metadata.json"}`, index)
		fsys[fmt.Sprintf("%d/metadata.json", index)] = &fstest.MapFile{Data: []byte(fmt.Sprintf(
			`{"cli_version":"0.8.42","checkpoint_id":"abcdef123456","session_id":"11111111-2222-4333-8444-55555555555%d","strategy":"manual-commit","created_at":"2026-08-05T10:01:00Z","branch":"main","checkpoints_count":1,"files_touched":%s,"agent":"Codex","model":"model","turn_id":"123456abcdef"}`,
			index, filesJSON))}
	}
	fsys["metadata.json"] = &fstest.MapFile{Data: []byte(fmt.Sprintf(`{"checkpoint_id":"abcdef123456","sessions":[%s]}`, strings.Join(refs, ",")))}
	checkpoint, err := entirecheckpoint.ParseCheckpoint(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func syntheticCheckpoint(t *testing.T, agent, compact string, files []string, usage bool) *entirecheckpoint.Checkpoint {
	t.Helper()
	filesJSON, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	usageJSON := ""
	if usage {
		usageJSON = `,"token_usage":{"input_tokens":10,"cache_creation_tokens":20,"cache_read_tokens":30,"output_tokens":40,"api_call_count":2}`
	}
	root := `{"checkpoint_id":"abcdef123456","sessions":[{"metadata":"/ab/cdef123456/0/metadata.json","compact_transcript":"/ab/cdef123456/0/transcript.jsonl"}]}`
	metadata := fmt.Sprintf(`{"cli_version":"0.9.0","checkpoint_id":"abcdef123456","session_id":"11111111-2222-4333-8444-555555555555","strategy":"manual-commit","created_at":"2026-08-05T10:01:00Z","branch":"main","checkpoints_count":1,"files_touched":%s,"agent":%q,"model":"model","turn_id":"123456abcdef"%s}`, filesJSON, agent, usageJSON)
	fsys := fstest.MapFS{
		"metadata.json":   &fstest.MapFile{Data: []byte(root)},
		"0/metadata.json": &fstest.MapFile{Data: []byte(metadata)},
	}
	if compact != "" {
		fsys["0/transcript.jsonl"] = &fstest.MapFile{Data: []byte(compact)}
	}
	checkpoint, err := entirecheckpoint.ParseCheckpoint(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
