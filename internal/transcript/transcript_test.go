package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	got := build(t, graph(), Meta{ID: "root", Vendor: "claude_code", Commits: 2, Confidence: 0.9}, DefaultLimits)
	s := got.Session
	if s.RunID != "root" || s.Source != "claude_code" || s.Title != "Fix the parser" || s.GitBranch != "fix/parser" ||
		s.Outcome != "committed" || s.CommitCount != 2 || s.Confidence != 0.9 || !s.HasInnerTrace {
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
	// Every prompt carries its text replies, oldest first; a subagent's are
	// its own, and a blank message is none.
	if !s.RepliesIncluded || s.UserEmail != "" || s.CostUSD != nil || first.RepliesOmitted != 0 || len(first.Replies) != 2 ||
		first.Replies[0].Text != "Reading the parser." || first.Replies[1].Text != "Fixed." || first.Replies[1].Model != "claude-opus" {
		t.Errorf("replies = %+v (%d omitted), included %v", first.Replies, first.RepliesOmitted, s.RepliesIncluded)
	}
	if len(s.Prompts[1].Replies) != 0 || s.Prompts[1].Replies == nil {
		t.Errorf("second prompt's replies = %#v", s.Prompts[1].Replies)
	}

	activity := got.Activity["p1"]
	if len(activity.Messages) != 2 || activity.Messages[0].Text != "Reading the parser." || activity.Messages[0].Model != "claude-opus" {
		t.Errorf("messages = %+v", activity.Messages)
	}
	if len(activity.Invocations) != 1 || activity.InvocationsTotal != 2 || activity.TokenUsage.APICalls != 2 {
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
	got := build(t, g, Meta{ID: "root"}, DefaultLimits)
	if got.Session.Tokens != 7 || got.Session.CostPerPrompt {
		t.Errorf("tokens %d, per prompt %v", got.Session.Tokens, got.Session.CostPerPrompt)
	}
}

func TestBuildWithoutAnInnerTraceLeavesTheEffectsUnknown(t *testing.T) {
	g := &session.Graph{
		Sessions:   []session.Session{{ID: "root", Outcome: session.OutcomeUnknown}},
		UserInputs: []session.UserInput{{ID: "p", SessionID: "root", Text: "hello", Kind: session.KindPrompt, Origin: session.OriginHuman}},
	}
	got := build(t, g, Meta{ID: "root"}, DefaultLimits)
	if got.Session.HasInnerTrace || got.Session.Totals != nil || got.Session.Prompts[0].Effects != nil {
		t.Errorf("session = %+v", got.Session)
	}
}

func build(t *testing.T, g *session.Graph, meta Meta, limits Limits) Transcript {
	t.Helper()
	got, err := Build(g, meta, limits, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func encodedLen(t *testing.T, tr Transcript) int {
	t.Helper()
	body, err := Encode(tr)
	if err != nil {
		t.Fatal(err)
	}
	return len(body)
}

func TestBuildCutsLongTextsAndLongLists(t *testing.T) {
	g := graph()
	g.UserInputs[1].Text = strings.Repeat("é", 40) // 80 bytes
	limits := Limits{Bytes: 1 << 20, Prompt: 50, Message: 4, Tool: 10, Messages: 1, ToolCalls: 2, Reply: 3, Replies: 1}
	got := build(t, g, Meta{ID: "root"}, limits)
	s := got.Session
	if !got.Truncated || got.LimitBytes != 1<<20 || len(s.Prompts) != 2 || s.PromptsTruncated {
		t.Errorf("truncated %v, limit %d, prompts %d", got.Truncated, got.LimitBytes, len(s.Prompts))
	}
	first := s.Prompts[0]
	if !first.Truncated || first.TextLength != 40 || first.Text != strings.Repeat("é", 25) {
		t.Errorf("first prompt = %q (%d), truncated %v", first.Text, first.TextLength, first.Truncated)
	}
	// The last reply, previewed; the earlier one is counted.
	if len(first.Replies) != 1 || first.RepliesOmitted != 1 || first.Replies[0].Text != "Fix" ||
		!first.Replies[0].Truncated || first.Replies[0].TextLength != 6 {
		t.Errorf("replies = %+v (%d omitted)", first.Replies, first.RepliesOmitted)
	}
	activity := got.Activity["p1"]
	if len(activity.Messages) != 1 || !activity.MessagesTruncated || activity.Messages[0].Text != "Read" ||
		!activity.Messages[0].Truncated || activity.Messages[0].TextLength != 19 {
		t.Errorf("messages = %+v", activity.Messages)
	}
	if len(activity.ToolCalls) != 2 || !activity.ToolCallsTruncated || !activity.ToolCalls[0].InputTruncated ||
		len(activity.ToolCalls[0].Input) != 10 || !activity.ToolCalls[0].OutputTruncated || *activity.ToolCalls[0].Output != "package pa" {
		t.Errorf("tool calls = %+v", activity.ToolCalls)
	}
}

// manyCalls is one prompt with n tool calls, each printing size bytes.
func manyCalls(n, size int) *session.Graph {
	g := &session.Graph{
		Sessions:   []session.Session{{ID: "root"}},
		UserInputs: []session.UserInput{{ID: "p1", SessionID: "root", Ordinal: 1, Text: "go", Kind: session.KindPrompt, Origin: session.OriginHuman}},
	}
	for i := range n {
		id := fmt.Sprintf("call-%02d", i)
		g.ToolUses = append(g.ToolUses, session.ToolUse{ID: id, SessionID: "root", UserInputID: "p1", Name: "Bash",
			Verb: session.VerbExecute, Input: json.RawMessage(`{}`), Status: session.StatusOK, Timestamp: at(i)})
		g.ToolResults = append(g.ToolResults, session.ToolResult{ID: "r" + id, ToolUseID: id, SessionID: "root",
			UserInputID: "p1", Status: session.StatusOK, OutputText: ptr(strings.Repeat("o", size))})
	}
	return g
}

// The budget cuts within a prompt: the calls that fit are listed in order,
// and the rest are reported as left out.
func TestBuildStopsAtTheBudgetWithinAPrompt(t *testing.T) {
	limits := DefaultLimits
	whole := build(t, manyCalls(5, 1000), Meta{ID: "root"}, limits)
	if whole.Truncated || len(whole.Activity["p1"].ToolCalls) != 5 {
		t.Fatalf("unbounded: truncated %v", whole.Truncated)
	}
	limits.Bytes = encodedLen(t, whole) + budgetReserve - 1500
	got := build(t, manyCalls(5, 1000), Meta{ID: "root"}, limits)
	activity := got.Activity["p1"]
	if len(activity.ToolCalls) != 3 || !activity.ToolCallsTruncated || !got.Truncated || got.Session.PromptsTruncated {
		t.Errorf("calls %d, calls cut %v, truncated %v", len(activity.ToolCalls), activity.ToolCallsTruncated, got.Truncated)
	}
	if activity.ToolCalls[2].ID != "call-02" {
		t.Errorf("calls = %+v", activity.ToolCalls)
	}
	if n := encodedLen(t, got); n > limits.Bytes {
		t.Errorf("encoded %d bytes over a budget of %d", n, limits.Bytes)
	}
}

func TestBuildLeavesOutThePromptsPastTheBudget(t *testing.T) {
	g := graph()
	limits := DefaultLimits
	whole := build(t, g, Meta{ID: "root"}, limits)
	// Room for the first prompt, not the second (some 700 bytes with its
	// activity).
	limits.Bytes = encodedLen(t, whole) + budgetReserve - 300
	got := build(t, g, Meta{ID: "root"}, limits)
	if len(got.Session.Prompts) != 1 || !got.Session.PromptsTruncated || got.Session.PromptsTotal != 2 || !got.Truncated {
		t.Errorf("prompts = %d of %d, truncated %v", len(got.Session.Prompts), got.Session.PromptsTotal, got.Session.PromptsTruncated)
	}
	if _, ok := got.Activity["p2"]; ok {
		t.Error("a prompt left out has activity")
	}
}

// Large synthetic sessions stay within the budget as encoded, angle
// brackets and ampersands included, and say they were cut.
func TestBuildKeepsTheEncodedSizeWithinTheBudget(t *testing.T) {
	for _, c := range []struct {
		prompts, calls int
		out            string
	}{
		{300, 200, strings.Repeat("x", 60)},
		{300, 200, strings.Repeat("<&>", 20)},
		{300, 200, ""},
		{20, 1000, strings.Repeat("<", 4000)},
	} {
		g := bigGraph(c.prompts, c.calls, c.out)
		got := build(t, g, Meta{ID: "root"}, DefaultLimits)
		if n := encodedLen(t, got); n > DefaultLimits.Bytes || !got.Truncated {
			t.Errorf("%d prompts of %d calls of %d bytes: %d bytes encoded, truncated %v",
				c.prompts, c.calls, len(c.out), n, got.Truncated)
		}
	}
}

func bigGraph(prompts, calls int, out string) *session.Graph {
	g := &session.Graph{Sessions: []session.Session{{ID: "root"}}}
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	model := "claude-opus"
	for p := range prompts {
		in := fmt.Sprintf("p%d", p)
		g.UserInputs = append(g.UserInputs, session.UserInput{ID: in, SessionID: "root", Ordinal: p + 1, Text: "do it",
			Kind: session.KindPrompt, Origin: session.OriginHuman})
		for c := range calls {
			id := fmt.Sprintf("toolu_01ABCDEFGHIJKLMNOPQRSTUV%d_%d", p, c)
			when := ts.Add(time.Duration(p*calls+c) * time.Second)
			g.ModelInvocations = append(g.ModelInvocations, session.ModelInvocation{ID: "m" + id, SessionID: "root",
				UserInputID: in, Model: &model, InputTokens: 1, StartedAt: &when})
			g.ToolUses = append(g.ToolUses, session.ToolUse{ID: id, SessionID: "root", UserInputID: in, Name: "Bash",
				Verb: session.VerbExecute, Input: json.RawMessage(`{"command":"a && b <c>"}`), Status: session.StatusOK, Timestamp: &when})
			output := out
			g.ToolResults = append(g.ToolResults, session.ToolResult{ID: "r" + id, ToolUseID: id, SessionID: "root",
				UserInputID: in, Status: session.StatusOK, OutputText: &output})
		}
	}
	return g
}

// Clean reads past the cut, so a secret the cut runs through is masked in
// the part that is shown.
func TestBuildCleansTextsBeforeCuttingThem(t *testing.T) {
	g := graph()
	g.UserInputs[1].Text = strings.Repeat("a", 45) + "SECRETVALUE"
	g.AssistantMessages[0].Text = ptr(strings.Repeat("b", 45) + "SECRETVALUE")
	limits := DefaultLimits
	limits.Prompt, limits.Reply = 50, 50
	var seen []string
	got, err := Build(g, Meta{ID: "root"}, limits, func(text string) (string, error) {
		seen = append(seen, text)
		return strings.ReplaceAll(text, "SECRETVALUE", "XXXXXXXXXXX"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.Prompts[0].Text != strings.Repeat("a", 45)+"XXXXX" || !slices.Contains(seen, "package parse") {
		t.Errorf("prompt = %q, cleaned %q", got.Session.Prompts[0].Text, seen)
	}
	if reply := got.Session.Prompts[0].Replies[1].Text; reply != strings.Repeat("b", 45)+"XXXXX" {
		t.Errorf("reply = %q", reply)
	}
	if _, err := Build(g, Meta{ID: "root"}, limits, func(string) (string, error) { return "", errors.New("no") }); err == nil {
		t.Error("an error from clean was dropped")
	}
}

func TestClipCutsAtACharacter(t *testing.T) {
	for _, c := range []struct {
		text  string
		limit int
		want  string
	}{
		{"ab€", 2, "ab"},
		{"ab€", 3, "ab"},
		{"ab€", 4, "ab"},
		{"ab€", 5, "ab€"},
		{"a😀b", 2, "a"},
		{"a😀b", 4, "a"},
		{"a😀b", 5, "a😀"},
		{"é", 1, ""},
	} {
		got, cut := clip(c.text, c.limit)
		if got != c.want || cut != (len(c.text) > c.limit) || !utf8.ValidString(got) {
			t.Errorf("clip(%q, %d) = %q, %v; want %q", c.text, c.limit, got, cut, c.want)
		}
	}
}

// A reply's preview is not a loss: its whole text is in the activity.
func TestBuildDoesNotCountAReplyPreviewAsACut(t *testing.T) {
	limits := DefaultLimits
	limits.Reply = 3
	got := build(t, graph(), Meta{ID: "root"}, limits)
	if reply := got.Session.Prompts[0].Replies[0]; !reply.Truncated || got.Truncated {
		t.Errorf("reply %+v, truncated %v", reply, got.Truncated)
	}
}

func TestBuildOfAnEmptyGraph(t *testing.T) {
	got := build(t, &session.Graph{}, Meta{ID: "x", Vendor: "codex"}, DefaultLimits)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"prompts":[]`) || !strings.Contains(string(encoded), `"models":[]`) {
		t.Errorf("encoded = %s", encoded)
	}
}

// The session's own texts are cut too, so a huge title or many models do not
// carry the body past the budget.
func TestBuildCutsTheSessionsOwnTexts(t *testing.T) {
	g := graph()
	huge := strings.Repeat("t", 6<<20)
	g.Sessions[0].Title, g.Sessions[0].WorkingDir, g.Sessions[0].GitBranch = &huge, &huge, &huge
	for i := range 200 {
		model := fmt.Sprintf("model-%03d-%s", i, strings.Repeat("m", 2000))
		g.ModelInvocations = append(g.ModelInvocations, session.ModelInvocation{ID: fmt.Sprintf("x%d", i), SessionID: "root", UserInputID: "stop", Model: &model})
	}
	got := build(t, g, Meta{ID: "root", Vendor: huge}, DefaultLimits)
	s := got.Session
	for _, field := range []string{s.Title, s.WorkingDir, s.GitBranch, s.Source} {
		if len(field) != DefaultLimits.Field {
			t.Errorf("a field of %d bytes", len(field))
		}
	}
	if len(s.Models) != DefaultLimits.Models || len(s.Models[49]) != DefaultLimits.Field || !got.Truncated {
		t.Errorf("%d models, truncated %v", len(s.Models), got.Truncated)
	}
	if n := encodedLen(t, got); n > DefaultLimits.Bytes {
		t.Errorf("encoded %d bytes", n)
	}
}
