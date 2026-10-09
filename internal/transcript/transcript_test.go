package transcript

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/sessions/session"
)

func ptr[T any](v T) *T { return &v }

func at(minute int) *time.Time {
	t := time.Date(2026, 10, 1, 9, minute, 0, 0, time.UTC)
	return &t
}

// graph is a session of two prompts. The first reads and edits a file and
// starts a subagent that runs a failing command; a wrapper-only prompt and
// an interrupt carry no prompt of their own, and the interrupt's tool call
// is the session's.
func graph() *session.Graph {
	return &session.Graph{
		Sessions: []session.Session{
			{ID: "root", Title: ptr("Fix the parser"), WorkingDir: ptr("~/src/widgets"), GitBranch: ptr("fix/parser"),
				StartedAt: at(0), EndedAt: at(30), Outcome: session.OutcomeCommitted},
			{ID: "child", ParentSessionID: ptr("root"), SpawnedByToolUseID: ptr("spawn")},
		},
		UserInputs: []session.UserInput{
			{ID: "p2", SessionID: "root", Ordinal: 3, Text: "Now add a test", Kind: session.KindPrompt, Origin: session.OriginHuman, TriggeredAt: at(20)},
			{ID: "p1", SessionID: "root", Ordinal: 1, Text: "Fix the parser please", Kind: session.KindPrompt, Origin: session.OriginHuman, TriggeredAt: at(1)},
			{ID: "wrapper", SessionID: "root", Ordinal: 2, Text: "<command-name>/model</command-name>", Kind: session.KindSlashCommand, Origin: session.OriginHuman},
			{ID: "stop", SessionID: "root", Ordinal: 4, Text: "[Request interrupted by user]", Kind: session.KindInterrupt, Origin: session.OriginHuman},
			{ID: "task", SessionID: "child", Ordinal: 1, Text: "Run the tests", Kind: session.KindPrompt, Origin: session.OriginAgent},
		},
		ModelInvocations: []session.ModelInvocation{
			{ID: "m1", SessionID: "root", UserInputID: "p1", Model: ptr("claude-opus"), InputTokens: 100, OutputTokens: 50, StartedAt: at(2)},
			{ID: "m2", SessionID: "child", UserInputID: "task", Model: ptr("claude-haiku"), InputTokens: 10, OutputTokens: 5, TokenBasis: session.TokenBasisEstimated},
			{ID: "m3", SessionID: "root", UserInputID: "p2", Model: ptr("claude-opus"), TotalTokens: 40},
		},
		AssistantMessages: []session.AssistantMessage{
			{ID: "a2", SessionID: "root", UserInputID: "p1", ModelInvocationID: "m1", Text: ptr("Fixed."), Timestamp: at(9)},
			{ID: "a1", SessionID: "root", UserInputID: "p1", ModelInvocationID: "m1", Text: ptr("Reading the parser."), Timestamp: at(3)},
			{ID: "blank", SessionID: "root", UserInputID: "p1", ModelInvocationID: "m1", Text: ptr("  ")},
			{ID: "sub", SessionID: "child", UserInputID: "task", ModelInvocationID: "m2", Text: ptr("Tests fail.")},
		},
		ToolUses: []session.ToolUse{
			{ID: "read", SessionID: "root", UserInputID: "p1", Name: "Read", Verb: session.VerbRead, Input: json.RawMessage(`{"file_path":"parse.go"}`), Status: session.StatusOK, Timestamp: at(4)},
			{ID: "edit", SessionID: "root", UserInputID: "p1", Name: "Edit", Verb: session.VerbEdit, Input: json.RawMessage(`{"file_path":"parse.go"}`), Status: session.StatusOK, Timestamp: at(5)},
			{ID: "spawn", SessionID: "root", UserInputID: "p1", Name: "Task", Verb: session.VerbSpawnSubagent, Input: json.RawMessage(`{}`), Status: session.StatusOK, Timestamp: at(6)},
			{ID: "bash", SessionID: "child", UserInputID: "task", Name: "Bash", Verb: session.VerbExecute, Input: json.RawMessage(`{"command":"go test"}`), Status: session.StatusError, Timestamp: at(7)},
			{ID: "late", SessionID: "root", UserInputID: "stop", Name: "Bash", Verb: session.VerbExecute, Input: json.RawMessage(`{}`), Status: session.StatusInterrupted},
		},
		ToolResults: []session.ToolResult{
			{ID: "r1", ToolUseID: "read", SessionID: "root", UserInputID: "p1", Status: session.StatusOK, OutputText: ptr("package parse")},
			{ID: "r2", ToolUseID: "bash", SessionID: "child", UserInputID: "task", Status: session.StatusError, ErrorText: ptr("exit status 1")},
		},
		FileTouches: []session.FileTouch{
			{ID: "t1", ToolUseID: ptr("read"), UserInputID: "p1", SessionID: "root", Path: "parse.go", Kind: session.TouchRead},
			{ID: "t2", UserInputID: "p1", SessionID: "root", Path: "parse.go", Kind: session.TouchRead},
			{ID: "t3", ToolUseID: ptr("edit"), UserInputID: "p1", SessionID: "root", Path: "parse.go", Kind: session.TouchEdit,
				Hunks: []session.Hunk{{OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 3}}},
		},
		TokenUsage: []session.TokenUsage{
			{ID: "u1", SessionID: "root", Scope: session.UsageScopeSession, TotalTokens: ptr(int64(7))},
		},
	}
}

func TestBuildListsThePromptsWithTheirActivity(t *testing.T) {
	got := Build(graph(), Meta{ID: "root", Vendor: "claude_code", Commits: 2, Confidence: 0.9}, DefaultLimits)
	s := got.Session
	if s.RunID != "root" || s.Source != "claude_code" || s.Title != "Fix the parser" || s.GitBranch != "fix/parser" ||
		s.Outcome != "committed" || s.CommitCount != 2 || s.Confidence != 0.9 || s.UserEmail != "" || !s.HasInnerTrace {
		t.Errorf("session = %+v", s)
	}
	if len(s.Prompts) != 2 || s.Prompts[0].TurnID != "p1" || s.Prompts[1].TurnID != "p2" || s.TurnCount != 3 || s.PromptsTotal != 2 {
		t.Fatalf("prompts = %+v, turns %d", s.Prompts, s.TurnCount)
	}
	first := s.Prompts[0]
	want := Effects{FilesRead: 1, FilesEdited: 1, Reads: 2, FilesChanged: 1, DiffHunks: 1, AddedLines: 3, RemovedLines: 2,
		ToolCalls: 4, ExecuteCalls: 1, FailedCalls: 1}
	if first.Text != "Fix the parser please" || first.TextLength != 21 || *first.Effects != want {
		t.Errorf("first prompt = %+v, effects %+v", first, *first.Effects)
	}
	// The subagent's calls count toward the prompt that started it.
	if first.Tokens != 165 || first.EstimatedTokens != 15 {
		t.Errorf("first prompt tokens = %d (%d estimated)", first.Tokens, first.EstimatedTokens)
	}
	if s.Prompts[1].Tokens != 40 || *s.Prompts[1].Effects != (Effects{}) {
		t.Errorf("second prompt = %+v", s.Prompts[1])
	}
	if s.Tokens != 212 || !s.CostPerPrompt || strings.Join(s.Models, ",") != "claude-opus,claude-haiku" {
		t.Errorf("tokens %d, per prompt %v, models %v", s.Tokens, s.CostPerPrompt, s.Models)
	}
	if s.Unattributed.ToolCalls != 1 || s.Totals.ToolCalls != 5 {
		t.Errorf("unattributed %+v, totals %+v", *s.Unattributed, *s.Totals)
	}

	activity := got.Activity["p1"]
	if len(activity.Messages) != 2 || activity.Messages[0].Text != "Reading the parser." || activity.Messages[0].Model != "claude-opus" {
		t.Errorf("messages = %+v", activity.Messages)
	}
	if len(activity.Invocations) != 2 || activity.TokenUsage.APICalls != 2 {
		t.Errorf("invocations = %+v, usage %+v", activity.Invocations, activity.TokenUsage)
	}
	var names []string
	for _, call := range activity.ToolCalls {
		names = append(names, call.ID)
	}
	if strings.Join(names, ",") != "read,edit,spawn,bash" {
		t.Errorf("tool calls = %v", names)
	}
	if out := activity.ToolCalls[0].Output; out == nil || *out != "package parse" {
		t.Errorf("read output = %v", out)
	}
	if out := activity.ToolCalls[3].Output; out == nil || *out != "exit status 1" || activity.ToolCalls[3].Status != "error" {
		t.Errorf("bash = %+v", activity.ToolCalls[3])
	}
	if activity.ToolCalls[1].Output != nil || activity.ToolCalls[0].Input != `{"file_path":"parse.go"}` {
		t.Errorf("edit = %+v", activity.ToolCalls[1])
	}
	if got.Truncated || len(got.Activity) != 2 {
		t.Errorf("truncated %v, activity for %d prompts", got.Truncated, len(got.Activity))
	}
}

func TestBuildReportsUsageForTheWholeSessionOnly(t *testing.T) {
	g := graph()
	g.ModelInvocations = nil
	got := Build(g, Meta{ID: "root"}, DefaultLimits)
	if got.Session.Tokens != 7 || got.Session.CostPerPrompt {
		t.Errorf("tokens %d, per prompt %v", got.Session.Tokens, got.Session.CostPerPrompt)
	}
}

func TestBuildWithoutAnInnerTraceLeavesTheEffectsUnknown(t *testing.T) {
	g := &session.Graph{
		Sessions:   []session.Session{{ID: "root", Outcome: session.OutcomeUnknown}},
		UserInputs: []session.UserInput{{ID: "p", SessionID: "root", Text: "hello", Kind: session.KindPrompt, Origin: session.OriginHuman}},
	}
	got := Build(g, Meta{ID: "root"}, DefaultLimits)
	if got.Session.HasInnerTrace || got.Session.Totals != nil || got.Session.Prompts[0].Effects != nil {
		t.Errorf("session = %+v", got.Session)
	}
}

func TestBuildBoundsTheTranscript(t *testing.T) {
	g := graph()
	g.UserInputs[1].Text = strings.Repeat("é", 40) // 80 bytes
	limits := Limits{Bytes: 100, Prompt: 50, Message: 4, Tool: 10, Messages: 1, ToolCalls: 2}
	got := Build(g, Meta{ID: "root"}, limits)
	s := got.Session
	if !got.Truncated || got.LimitBytes != 100 {
		t.Errorf("truncated %v, limit %d", got.Truncated, got.LimitBytes)
	}
	first := s.Prompts[0]
	if !first.Truncated || first.TextLength != 40 || first.Text != strings.Repeat("é", 25) {
		t.Errorf("first prompt = %q (%d), truncated %v", first.Text, first.TextLength, first.Truncated)
	}
	activity := got.Activity["p1"]
	if len(activity.Messages) != 1 || !activity.MessagesTruncated || activity.Messages[0].Text != "Read" ||
		!activity.Messages[0].Truncated || activity.Messages[0].TextLength != 19 {
		t.Errorf("messages = %+v", activity.Messages)
	}
	if len(activity.ToolCalls) != 2 || !activity.ToolCallsTruncated || !activity.ToolCalls[0].InputTruncated ||
		len(activity.ToolCalls[0].Input) != 10 {
		t.Errorf("tool calls = %+v", activity.ToolCalls)
	}
	// 50 + 4 + 10+10 + 10 bytes leave 16: the second prompt still starts.
	if len(s.Prompts) != 2 || s.PromptsTruncated {
		t.Fatalf("prompts = %d, truncated %v", len(s.Prompts), s.PromptsTruncated)
	}

	limits.Bytes = 60
	got = Build(g, Meta{ID: "root"}, limits)
	if len(got.Session.Prompts) != 1 || !got.Session.PromptsTruncated || got.Session.PromptsTotal != 2 {
		t.Errorf("prompts = %d of %d, truncated %v", len(got.Session.Prompts), got.Session.PromptsTotal, got.Session.PromptsTruncated)
	}
	if _, ok := got.Activity["p2"]; ok {
		t.Error("a prompt left out has activity")
	}
}

func TestBuildOfAnEmptyGraph(t *testing.T) {
	got := Build(&session.Graph{}, Meta{ID: "x", Vendor: "codex"}, DefaultLimits)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"prompts":[]`) || !strings.Contains(string(encoded), `"models":[]`) {
		t.Errorf("encoded = %s", encoded)
	}
}
