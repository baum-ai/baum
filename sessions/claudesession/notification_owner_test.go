package claudesession

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func buildNotificationLines(t *testing.T, lines ...string) (*session.Graph, *Report) {
	t.Helper()
	for i := range lines {
		lines[i] = strings.Replace(lines[i], "{", fmt.Sprintf(`{"timestamp":"2026-09-09T10:%02d:00Z",`, i), 1)
	}
	return buildClaudeLines(t, lines...)
}

func TestNotificationActivityFollowsOriginalPrompt(t *testing.T) {
	g, _ := buildNotificationLines(t,
		`{"type":"user","uuid":"u1","promptId":"p1","message":{"role":"user","content":"Run the original task"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","model":"claude-test","usage":{"input_tokens":10,"output_tokens":1},"content":[{"type":"tool_use","id":"background","name":"Bash","input":{"command":"run"}}]}}`,
		`{"type":"user","uuid":"u2","promptId":"p2","message":{"role":"user","content":"An unrelated request"}}`,
		`{"type":"user","uuid":"u3","promptId":"p3","promptSource":"system","message":{"role":"user","content":"<task-notification><tool-use-id>background</tool-use-id></task-notification>"}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"m2","role":"assistant","model":"claude-test","usage":{"input_tokens":20,"output_tokens":2},"content":[{"type":"text","text":"Continue the task"},{"type":"tool_use","id":"read","name":"Read","input":{"file_path":"/repo/a.go"}}]}}`,
		`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read","content":"file contents"}]}}`,
		`{"type":"user","uuid":"u4","promptId":"p4","promptSource":"system","message":{"role":"user","content":"<task-notification><tool-use-id>read</tool-use-id></task-notification>"}}`,
		`{"type":"assistant","uuid":"a3","message":{"id":"m3","role":"assistant","model":"claude-test","usage":{"input_tokens":30,"output_tokens":3},"content":[{"type":"tool_use","id":"edit","name":"Edit","input":{"file_path":"/repo/a.go","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"user","uuid":"u5","promptId":"p5","promptSource":"system","message":{"role":"user","content":"<task-notification><tool-use-id>missing</tool-use-id></task-notification>"}}`,
		`{"type":"assistant","uuid":"a4","message":{"id":"m4","role":"assistant","model":"claude-test","usage":{"input_tokens":40,"output_tokens":4},"content":[{"type":"tool_use","id":"unknown","name":"Bash","input":{"command":"other"}}]}}`,
	)
	if len(g.UserInputs) != 5 || len(g.ToolUses) != 4 || len(g.ModelInvocations) != 4 || len(g.FileTouches) != 2 || len(g.ToolResults) != 1 {
		t.Fatalf("lost or duplicated mapped rows: inputs=%d tools=%d replies=%d touches=%d results=%d",
			len(g.UserInputs), len(g.ToolUses), len(g.ModelInvocations), len(g.FileTouches), len(g.ToolResults))
	}
	first, unresolved := g.UserInputs[0].ID, g.UserInputs[4].ID
	for _, input := range g.UserInputs[2:] {
		if input.Kind != session.KindOther || input.Origin != session.OriginSystem {
			t.Fatalf("notification changed classification: %+v", input)
		}
	}
	for i, tool := range g.ToolUses {
		want := first
		if i == 3 {
			want = unresolved
		}
		if tool.UserInputID != want || g.ModelInvocations[i].UserInputID != want {
			t.Fatalf("call/reply %d: want owner %s, got %s/%s", i, want, tool.UserInputID, g.ModelInvocations[i].UserInputID)
		}
	}
	for _, touch := range g.FileTouches {
		if touch.UserInputID != first {
			t.Fatalf("file activity owner = %s", touch.UserInputID)
		}
	}
	if g.ToolResults[0].UserInputID != first || g.AssistantMessages[0].UserInputID != first {
		t.Fatal("result/message attribution disagrees with call attribution")
	}
	var tokens int64
	for _, reply := range g.ModelInvocations {
		tokens += reply.InputTokens + reply.OutputTokens
	}
	if tokens != 110 {
		t.Fatalf("tokens = %d, want unchanged 110", tokens)
	}
}

func TestNotificationOwnershipBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*session.Graph)
		want string
	}{
		{"direct", func(*session.Graph) {}, "prompt"},
		{"missing", func(g *session.Graph) { g.VendorIDs = nil }, "notification"},
		{"self cycle", func(g *session.Graph) { g.ToolUses[0].UserInputID = "notification" }, "notification"},
		{"later input", func(g *session.Graph) { g.UserInputs[0].Ordinal = 3 }, "notification"},
		{"future tool", func(g *session.Graph) {
			at := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
			g.UserInputs[1].TriggeredAt = &at
			g.ToolUses[0].Timestamp = new(at.Add(time.Minute))
		}, "notification"},
		{"unresolved chain", func(g *session.Graph) {
			g.UserInputs[0].Kind = session.KindOther
			g.UserInputs[0].Origin = session.OriginSystem
			g.UserInputs[0].Text = "<task-notification><tool-use-id>missing</tool-use-id></task-notification>"
		}, "notification"},
		{"not system", func(g *session.Graph) { g.UserInputs[1].Origin = session.OriginUnknown }, "notification"},
		{"multiple notifications", func(g *session.Graph) { g.UserInputs[1].Text += g.UserInputs[1].Text }, "notification"},
		{"ambiguous tool ID", func(g *session.Graph) {
			g.ToolUses = append(g.ToolUses, session.ToolUse{ID: "duplicate", SessionID: "root", UserInputID: "prompt"})
			v := g.VendorIDs[0]
			v.OurID = "duplicate"
			g.VendorIDs = append(g.VendorIDs, v)
		}, "notification"},
		{"foreign root", func(g *session.Graph) { g.ToolUses[0].SessionID = "foreign" }, "notification"},
		{"descendant", func(g *session.Graph) {
			g.Sessions = append(g.Sessions, session.Session{ID: "child", ParentSessionID: new("root"), SpawnedByToolUseID: new("spawn")})
			g.ToolUses[0].SessionID = "child"
			g.ToolUses = append(g.ToolUses, session.ToolUse{ID: "spawn", SessionID: "root", UserInputID: "prompt"})
		}, "prompt"},
		{"broken spawn cycle", func(g *session.Graph) {
			g.Sessions = append(g.Sessions, session.Session{ID: "child", ParentSessionID: new("child"), SpawnedByToolUseID: new("tool")})
			g.ToolUses[0].SessionID = "child"
		}, "notification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &session.Graph{
				Sessions: []session.Session{{ID: "root"}},
				UserInputs: []session.UserInput{
					{ID: "prompt", SessionID: "root", Ordinal: 1, Kind: session.KindPrompt, Origin: session.OriginHuman},
					{ID: "notification", SessionID: "root", Ordinal: 2, Kind: session.KindOther, Origin: session.OriginSystem,
						Text: "<task-notification><tool-use-id>vendor-tool</tool-use-id></task-notification>"},
				},
				ToolUses:   []session.ToolUse{{ID: "tool", SessionID: "root", UserInputID: "prompt"}},
				VendorIDs:  []session.VendorID{{OurID: "tool", OurType: session.EntityToolUse, Vendor: Vendor, VendorIDKind: "tool_use.id", VendorID: "vendor-tool"}},
				TokenUsage: []session.TokenUsage{{UserInputID: new("notification"), Scope: session.UsageScopeInput}, {Scope: session.UsageScopeSession}},
			}
			tc.edit(g)
			attributeNotifications(g)
			if got := *g.TokenUsage[0].UserInputID; got != tc.want {
				t.Fatalf("owner = %s, want %s", got, tc.want)
			}
			if g.TokenUsage[1].UserInputID != nil {
				t.Fatal("session token usage acquired a prompt")
			}
			// A second pass must not change the result or start chasing mutated calls.
			attributeNotifications(g)
			if got := *g.TokenUsage[0].UserInputID; got != tc.want {
				t.Fatalf("second pass owner = %s, want %s", got, tc.want)
			}
		})
	}
}
