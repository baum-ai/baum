package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// validGraph is a two-level tree with one of every entity: a top-level session
// whose second ask spawned a nested session. Each test below breaks exactly one
// rule in it, so a failure names the rule rather than the fixture.
func validGraph() *Graph {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return &Graph{
		Sessions: []Session{
			{ID: "s-root", Outcome: OutcomeUnknown, StartedAt: &at},
			{ID: "s-nested", ParentSessionID: new("s-root"), SpawnedByToolUseID: new("t-spawn"), Outcome: OutcomeUnknown},
		},
		Attributes: []SessionAttribute{
			{ID: "a-1", SessionID: "s-root", Key: "permissionMode", Value: json.RawMessage(`"default"`), ValidFrom: at},
		},
		UserInputs: []UserInput{
			{ID: "u-1", SessionID: "s-root", Ordinal: 1, Text: "read it", Kind: KindPrompt, Origin: OriginHuman, Metadata: json.RawMessage(`{}`)},
			{ID: "u-2", SessionID: "s-root", Ordinal: 2, Text: "delegate it", Kind: KindPrompt, Origin: OriginHuman, Metadata: json.RawMessage(`{}`)},
			{ID: "u-3", SessionID: "s-nested", Ordinal: 1, Text: "audit it", Kind: KindPrompt, Origin: OriginHuman, Metadata: json.RawMessage(`{}`)},
		},
		ModelInvocations: []ModelInvocation{
			{ID: "m-1", SessionID: "s-root", UserInputID: "u-1"},
			{ID: "m-2", SessionID: "s-root", UserInputID: "u-1", ParentModelInvocationID: new("m-1")},
			{ID: "m-3", SessionID: "s-root", UserInputID: "u-2"},
			{ID: "m-4", SessionID: "s-nested", UserInputID: "u-3"},
		},
		AssistantMessages: []AssistantMessage{
			{ID: "am-1", SessionID: "s-root", UserInputID: "u-1", ModelInvocationID: "m-1", Text: new("reading")},
			{ID: "am-2", SessionID: "s-nested", UserInputID: "u-3", ModelInvocationID: "m-4", Content: json.RawMessage(`{"type":"thinking"}`)},
		},
		ToolUses: []ToolUse{
			{ID: "t-read", SessionID: "s-root", UserInputID: "u-1", ModelInvocationID: "m-1", Name: "Read",
				Verb: VerbRead, Input: json.RawMessage(`{"file_path":"/a.go"}`), Status: StatusOK, Metadata: json.RawMessage(`{}`)},
			{ID: "t-spawn", SessionID: "s-root", UserInputID: "u-2", ModelInvocationID: "m-3", Name: "Agent",
				Verb: VerbSpawnSubagent, Input: json.RawMessage(`{}`), Status: StatusOK, Metadata: json.RawMessage(`{}`)},
			{ID: "t-open", SessionID: "s-root", UserInputID: "u-2", ModelInvocationID: "m-3", Name: "Bash",
				Verb: VerbExecute, Input: json.RawMessage(`{}`), Status: StatusNoResult, Metadata: json.RawMessage(`{}`)},
		},
		ToolResults: []ToolResult{
			{ID: "r-read", ToolUseID: "t-read", SessionID: "s-root", UserInputID: "u-1", Status: StatusOK},
			{ID: "r-spawn", ToolUseID: "t-spawn", SessionID: "s-root", UserInputID: "u-2", Status: StatusOK},
		},
		FileTouches: []FileTouch{
			{ID: "f-1", ToolUseID: new("t-read"), UserInputID: "u-1", SessionID: "s-root", Path: "/a.go",
				Kind: TouchRead, StartLine: new(1), EndLine: new(20)},
		},
		TokenUsage: []TokenUsage{
			{ID: "usage-input", SessionID: "s-root", UserInputID: new("u-1"), Scope: UsageScopeInput,
				InputTokens: new(int64(10)), OutputTokens: new(int64(3)), TotalTokens: new(int64(13))},
			{ID: "usage-session", SessionID: "s-root", Scope: UsageScopeSession,
				CacheReadTokens: new(int64(20))},
		},
		VendorIDs: []VendorID{
			{OurID: "s-root", OurType: EntitySession, SessionID: "s-root",
				Vendor: "claude_code", VendorIDKind: "sessionId", VendorID: "S"},
			{OurID: "s-nested", OurType: EntitySession, SessionID: "s-nested",
				Vendor: "claude_code", VendorIDKind: "agentId", VendorID: "A"},
			{OurID: "t-read", OurType: EntityToolUse, SessionID: "s-root",
				Vendor: "claude_code", VendorIDKind: "tool_use.id", VendorID: "toolu_1"},
		},
	}
}

func TestValidateAcceptsAWellFormedTree(t *testing.T) {
	if err := Validate(validGraph()); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}

func TestValidateAcceptsCacheTTLSplitAndSpeed(t *testing.T) {
	g := validGraph()
	g.ModelInvocations[0].CacheWriteTokens = 5
	g.ModelInvocations[0].CacheWrite1hTokens = new(int64(5))
	g.ModelInvocations[0].Speed = new(SpeedFast)
	g.ModelInvocations[1].CacheWrite1hTokens = new(int64(0))
	g.ModelInvocations[1].Speed = new(SpeedStandard)
	if err := Validate(g); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateAcceptsUnknownStatusWithResult(t *testing.T) {
	g := validGraph()
	g.ToolUses[0].Status = StatusUnknown
	g.ToolResults[0].Status = StatusUnknown
	if err := Validate(g); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}

func TestValidateAcceptsFileTouchWithoutToolUse(t *testing.T) {
	g := validGraph()
	g.FileTouches[0].ToolUseID = nil
	if err := Validate(g); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}

func TestValidateAcceptsScalarToolInput(t *testing.T) {
	g := validGraph()
	g.ToolUses[0].Input = json.RawMessage(`"free-form input"`)
	if err := Validate(g); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*Graph)
		want   string
	}{
		{
			name:   "a nested session with no spawning tool use",
			break_: func(g *Graph) { g.Sessions[1].SpawnedByToolUseID = nil },
			want:   "sets only one of parent_session_id",
		},
		{
			name:   "a spawn link to a tool use that is not a spawn",
			break_: func(g *Graph) { g.Sessions[1].SpawnedByToolUseID = new("t-read") },
			want:   `whose verb is "read"`,
		},
		{
			name: "a spawn link to a tool use in another session",
			break_: func(g *Graph) {
				g.ToolUses[1].SessionID = "s-nested"
				g.ToolUses[1].UserInputID = "u-3"
				g.ToolResults[1].SessionID = "s-nested"
				g.ToolResults[1].UserInputID = "u-3"
				g.ToolUses[1].ModelInvocationID = "m-4"
			},
			want: "but its parent is s-root",
		},
		{
			name: "two sessions spawned by one tool use",
			break_: func(g *Graph) {
				g.Sessions = append(g.Sessions, Session{ID: "s-other", ParentSessionID: new("s-root"), SpawnedByToolUseID: new("t-spawn"), Outcome: OutcomeUnknown})
			},
			want: "spawns both session",
		},
		{
			name:   "a child session listed before its parent",
			break_: func(g *Graph) { g.Sessions[0], g.Sessions[1] = g.Sessions[1], g.Sessions[0] },
			want:   "must be ordered parent-first",
		},
		{
			name: "no top-level session",
			break_: func(g *Graph) {
				g.Sessions[0].ParentSessionID = new("s-nested")
				g.Sessions[0].SpawnedByToolUseID = new("t-spawn")
			},
			want: "top-level sessions, want exactly 1",
		},
		{
			name:   "an unknown outcome",
			break_: func(g *Graph) { g.Sessions[0].Outcome = "finished" },
			want:   `unknown outcome "finished"`,
		},
		{
			name:   "a duplicate id",
			break_: func(g *Graph) { g.UserInputs[1].ID = "u-1" },
			want:   "appears more than once",
		},
		{
			name:   "an ordinal that does not increase",
			break_: func(g *Graph) { g.UserInputs[1].Ordinal = 1 },
			want:   "ordinals must strictly increase",
		},
		{
			name:   "a zero ordinal",
			break_: func(g *Graph) { g.UserInputs[0].Ordinal = 0 },
			want:   "want a positive integer",
		},
		{
			name:   "metadata that is not an object",
			break_: func(g *Graph) { g.UserInputs[0].Metadata = json.RawMessage(`"bag"`) },
			want:   "is not a JSON object",
		},
		{
			name:   "an unknown input origin",
			break_: func(g *Graph) { g.UserInputs[0].Origin = "operator" },
			want:   `unknown origin "operator"`,
		},
		{
			// The check that matters most for nested sessions: nothing in the
			// subagent's transcript may be attributed to the parent's ask.
			name:   "a consequence in one session under another session's ask",
			break_: func(g *Graph) { g.ModelInvocations[3].UserInputID = "u-2" },
			want:   "model invocation m-4 is in session s-nested but its user input is in s-root",
		},
		{
			name: "a 1-hour cache write above the cache writes",
			break_: func(g *Graph) {
				g.ModelInvocations[0].CacheWriteTokens = 5
				g.ModelInvocations[0].CacheWrite1hTokens = new(int64(6))
			},
			want: "has cache_write_1h_tokens 6",
		},
		{
			name:   "a negative 1-hour cache write",
			break_: func(g *Graph) { g.ModelInvocations[0].CacheWrite1hTokens = new(int64(-1)) },
			want:   "has cache_write_1h_tokens -1",
		},
		{
			name:   "an unknown speed",
			break_: func(g *Graph) { g.ModelInvocations[0].Speed = new("turbo") },
			want:   `has unknown speed "turbo"`,
		},
		{
			name:   "a negative token count",
			break_: func(g *Graph) { g.ModelInvocations[0].OutputTokens = -1 },
			want:   "want a non-negative count",
		},
		{
			name:   "an unknown token usage scope",
			break_: func(g *Graph) { g.TokenUsage[0].Scope = "turn" },
			want:   `unknown scope "turn"`,
		},
		{
			name:   "input token usage without an input",
			break_: func(g *Graph) { g.TokenUsage[0].UserInputID = nil },
			want:   "has no user input",
		},
		{
			name:   "session token usage with an input",
			break_: func(g *Graph) { g.TokenUsage[1].UserInputID = new("u-1") },
			want:   "session-scoped token usage usage-session names user input u-1",
		},
		{
			name:   "token usage under an input from another session",
			break_: func(g *Graph) { g.TokenUsage[0].UserInputID = new("u-3") },
			want:   "its user input is in s-nested",
		},
		{
			name:   "negative aggregate token usage",
			break_: func(g *Graph) { g.TokenUsage[0].InputTokens = new(int64(-1)) },
			want:   "want a non-negative count",
		},
		{
			name: "token usage without a count",
			break_: func(g *Graph) {
				g.TokenUsage[1].CacheReadTokens = nil
			},
			want: "has no reported counts",
		},
		{
			name: "negative token usage API call count",
			break_: func(g *Graph) {
				g.TokenUsage[0].APICallCount = new(int64(-1))
			},
			want: "has api_call_count -1, want a non-negative count",
		},
		{
			name: "API call count without a token counter",
			break_: func(g *Graph) {
				g.TokenUsage[1].CacheReadTokens = nil
				g.TokenUsage[1].APICallCount = new(int64(1))
			},
			want: "has no reported counts",
		},
		{
			name:   "an invocation chained to one under a different ask",
			break_: func(g *Graph) { g.ModelInvocations[2].ParentModelInvocationID = new("m-1") },
			want:   "chained to parent m-1 under a different user input",
		},
		{
			name:   "an invocation chained to one that comes later",
			break_: func(g *Graph) { g.ModelInvocations[0].ParentModelInvocationID = new("m-2") },
			want:   "does not precede it in the graph",
		},
		{
			name:   "an assistant message with no body",
			break_: func(g *Graph) { g.AssistantMessages[0].Text = nil; g.AssistantMessages[0].Content = nil },
			want:   "has neither text nor content",
		},
		{
			name:   "an assistant message off its invocation's ask",
			break_: func(g *Graph) { g.AssistantMessages[0].UserInputID = "u-2" },
			want:   "its model invocation is under u-1",
		},
		{
			name:   "an unknown verb",
			break_: func(g *Graph) { g.ToolUses[0].Verb = "peek" },
			want:   `unknown verb "peek"`,
		},
		{
			name:   "a tool use with a result but status no_result",
			break_: func(g *Graph) { g.ToolUses[0].Status = StatusNoResult; g.ToolResults[0].Status = StatusNoResult },
			want:   `has status "no_result" but has a tool result`,
		},
		{
			name:   "a tool use with no result but a settled status",
			break_: func(g *Graph) { g.ToolUses[2].Status = StatusOK },
			want:   `has status "ok" but has no tool result`,
		},
		{
			name:   "a tool use with no result but unknown status",
			break_: func(g *Graph) { g.ToolUses[2].Status = StatusUnknown },
			want:   `has status "unknown" but has no tool result`,
		},
		{
			name: "two results for one tool use",
			break_: func(g *Graph) {
				g.ToolResults = append(g.ToolResults, ToolResult{ID: "r-again", ToolUseID: "t-read", SessionID: "s-root", UserInputID: "u-1", Status: StatusOK})
			},
			want: "has two tool results",
		},
		{
			name:   "a result whose status disagrees with its tool use",
			break_: func(g *Graph) { g.ToolResults[0].Status = StatusError },
			want:   `has status "error" but its tool use has "ok"`,
		},
		{
			name:   "a result attributed to another ask than its tool use",
			break_: func(g *Graph) { g.ToolResults[0].UserInputID = "u-2" },
			want:   "its tool use is under u-1",
		},
		{
			name:   "a file touch with no path",
			break_: func(g *Graph) { g.FileTouches[0].Path = "" },
			want:   "has no path",
		},
		{
			name:   "a file touch with a zero start line",
			break_: func(g *Graph) { g.FileTouches[0].StartLine = new(0) },
			want:   "start_line 0",
		},
		{
			name:   "a file touch in another session than its tool use",
			break_: func(g *Graph) { g.FileTouches[0].SessionID = "s-nested" },
			want:   "file touch f-1 is in session s-nested but its tool use is in s-root",
		},
		{
			name: "two attribute rows with one key and instant",
			break_: func(g *Graph) {
				g.Attributes = append(g.Attributes, SessionAttribute{ID: "a-2", SessionID: "s-root", Key: "permissionMode", Value: json.RawMessage(`"plan"`), ValidFrom: g.Attributes[0].ValidFrom})
			},
			want: "share key",
		},
		{
			name:   "an attribute with no valid_from",
			break_: func(g *Graph) { g.Attributes[0].ValidFrom = time.Time{} },
			want:   "has no valid_from",
		},
		{
			name:   "an attribute whose value is not JSON",
			break_: func(g *Graph) { g.Attributes[0].Value = json.RawMessage(`{oops`) },
			want:   "no valid JSON value",
		},
		{
			name:   "a vendor id on an entity that is not in the graph",
			break_: func(g *Graph) { g.VendorIDs[0].OurID = "s-gone" },
			want:   "which is not in the graph",
		},
		{
			// Two asks in one session cannot both be identified by one prompt id.
			// Across sessions they can: a subagent's root ask carries the prompt id
			// of the turn that spawned it.
			name: "one vendor id claimed by two entities in one session",
			break_: func(g *Graph) {
				g.VendorIDs = append(g.VendorIDs,
					VendorID{OurID: "u-1", OurType: EntityUserInput, SessionID: "s-root",
						Vendor: "claude_code", VendorIDKind: "promptId", VendorID: "P1"},
					VendorID{OurID: "u-2", OurType: EntityUserInput, SessionID: "s-root",
						Vendor: "claude_code", VendorIDKind: "promptId", VendorID: "P1"},
				)
			},
			want: "vendor id claude_code/promptId/P1 maps to both",
		},
		{
			// A mapping row is deleted with the session it is filed under, so a
			// row filed under the wrong one outlives what it describes.
			name:   "a vendor id filed under the wrong session",
			break_: func(g *Graph) { g.VendorIDs[2].SessionID = "s-nested" },
			want:   "is filed under session s-nested but that entity is in s-root",
		},
		{
			name:   "an unknown entity type",
			break_: func(g *Graph) { g.VendorIDs[0].OurType = "run" },
			want:   `unknown entity type "run"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := validGraph()
			tt.break_(g)
			err := Validate(g)
			if err == nil {
				t.Fatalf("want a violation mentioning %q, got none", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want a violation mentioning %q, got:\n%v", tt.want, err)
			}
		})
	}
}

// Validate reports everything wrong at once so one run of the ingest tells the
// whole story, rather than one problem per re-run.
func TestValidateReportsEveryViolation(t *testing.T) {
	g := validGraph()
	g.UserInputs[0].Ordinal = 0
	g.AssistantMessages[0].Text = nil
	g.AssistantMessages[0].Content = nil
	g.FileTouches[0].Path = ""

	err := Validate(g)
	if err == nil {
		t.Fatal("want violations, got none")
	}
	for _, want := range []string{"want a positive integer", "has neither text nor content", "has no path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q from:\n%v", want, err)
		}
	}
}

// A vendor id may repeat across ingests — a forked Claude transcript carries its
// origin's line ids — and across sessions within one tree, because a subagent's
// root ask carries the prompt id of the turn that spawned it. Two entity types
// sharing an id string is likewise fine.
func TestValidateAllowsOneVendorIDAcrossEntityTypes(t *testing.T) {
	g := validGraph()
	g.VendorIDs = append(g.VendorIDs,
		VendorID{OurID: "r-read", OurType: EntityToolResult, SessionID: "s-root",
			Vendor: "claude_code", VendorIDKind: "tool_use.id", VendorID: "toolu_1"},
		VendorID{OurID: "t-read", OurType: EntityToolUse, SessionID: "s-root",
			Vendor: "claude_code", VendorIDKind: "uuid", VendorID: "line-9"},
	)
	if err := Validate(g); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}

func TestValidateAllowsOneVendorIDAcrossSessions(t *testing.T) {
	g := validGraph()
	g.VendorIDs = append(g.VendorIDs,
		VendorID{OurID: "u-1", OurType: EntityUserInput, SessionID: "s-root",
			Vendor: "claude_code", VendorIDKind: "promptId", VendorID: "P1"},
		VendorID{OurID: "u-3", OurType: EntityUserInput, SessionID: "s-nested",
			Vendor: "claude_code", VendorIDKind: "promptId", VendorID: "P1"},
	)
	if err := Validate(g); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}
