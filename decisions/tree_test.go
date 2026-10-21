package decisions

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/session"
)

// The Tree's rules are the ones Render applies, for other renders of the same
// tree.
func TestTreeSharesTheTranscriptRules(t *testing.T) {
	rejected := "The user doesn't want to proceed with this tool use. The tool use was rejected."
	c := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Review it").
		tool("u1", "spawn", "Agent", `{"prompt":"review"}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Async agent launched successfully."), Output: json.RawMessage(`{"agentId":"a1","isAsync":true}`),
		}).
		tool("u1", "w", "Write", `{}`, session.ToolResult{Status: session.StatusError, ErrorText: new(rejected)}).
		editBy("u1", "w", "/home/alice/repo/denied.go").
		edit("u1", "/home/alice/repo/kept.go").
		tool("u1", "report", "spawn_agent", `{}`, session.ToolResult{Status: session.StatusOK, OutputText: new("Codex report")}).reported().
		input("u2", 2, session.OriginSystem, "<task-notification>\n<task-id>a1</task-id>\n<result>Async report</result>\n</task-notification>")
	c.graph.Sessions = append(c.graph.Sessions, session.Session{
		ID: "child", Outcome: session.OutcomeUnknown, ParentSessionID: new("s"), SpawnedByToolUseID: new("spawn"),
	})
	tree, err := NewTree(&c.graph)
	if err != nil {
		t.Fatal(err)
	}

	if tree.turnOf("s", "u2") != "u2" || tree.turnOf("child", "anything") != "u1" {
		t.Errorf("turns: root input %q, nested %q", tree.turnOf("s", "u2"), tree.turnOf("child", "anything"))
	}
	if child, ok := tree.SpawnedSession("spawn"); !ok || child != "child" {
		t.Errorf("SpawnedSession = %q, %v", child, ok)
	}
	if _, ok := tree.SpawnedSession("w"); ok {
		t.Error("a write spawned a session")
	}
	if result := tree.Result("w"); result == nil || result.Status != session.StatusError {
		t.Errorf("Result = %+v", result)
	}
	if agent := claudeAgent(c.graph.ToolUses[0], tree.Result("spawn")); agent.AgentID != "a1" || !agent.IsAsync {
		t.Errorf("claudeAgent = %+v", agent)
	}
	if report, spawn := tree.NotificationReport(c.graph.UserInputs[1]); report != "Async report" || spawn != "spawn" {
		t.Errorf("NotificationReport = %q, %q", report, spawn)
	}
	if report, spawn := tree.NotificationReport(c.graph.UserInputs[0]); report != "" || spawn != "" {
		t.Errorf("a prompt is a notification: %q, %q", report, spawn)
	}
	if event, ok := tree.ToolEvent(c.graph.ToolUses[1]); !ok || event.Kind != EventDenial || event.Tool != "Write" {
		t.Errorf("ToolEvent(denied write) = %+v, %v", event, ok)
	}
	if !ReportOnCall(c.graph.ToolUses[2]) || ReportOnCall(c.graph.ToolUses[0]) {
		t.Error("ReportOnCall does not read the subagent detail")
	}
	var edited []string
	for _, touch := range c.graph.FileTouches {
		if path, ok := tree.EditedPath(touch); ok {
			edited = append(edited, path)
		}
	}
	if !slices.Equal(edited, []string{"kept.go"}) {
		t.Errorf("edited paths = %v, want only the edit that was not rejected", edited)
	}
	if got := tree.ScrubPaths("see /home/alice/repo/kept.go and /tmp/x on alice.example.dev"); got != "see kept.go and <local path> on user.example.dev" {
		t.Errorf("ScrubPaths = %q", got)
	}
	if names := localNames(&c.graph); !slices.Equal(names, []string{"alice"}) {
		t.Errorf("localNames = %v", names)
	}
	if HumanText(c.graph.UserInputs[0]) != "Review it" || HumanText(c.graph.UserInputs[1]) != "" {
		t.Error("HumanText keeps a system input or drops a prompt")
	}
}

func TestSortRowsOrdersTheGivenRows(t *testing.T) {
	early, late := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	c := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Go").
		say("u1", "late").
		tool("u1", "call", "Bash", `{}`, session.ToolResult{Status: session.StatusOK}).
		say("u1", "untimed")
	c.graph.AssistantMessages[0].Timestamp = &late
	c.graph.ToolUses[0].Timestamp = &early
	tree, err := NewTree(&c.graph)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(rows []Row) []string {
		var out []string
		for _, row := range rows {
			if row.Message != nil {
				out = append(out, row.Message.ID)
			} else {
				out = append(out, row.ToolUse.ID)
			}
		}
		return out
	}

	// Every given row has a timestamp, so time orders them.
	timed := []Row{{Message: &c.graph.AssistantMessages[0], Index: 0}, {ToolUse: &c.graph.ToolUses[0], Index: 0}}
	tree.SortRows(timed)
	if got := ids(timed); !slices.Equal(got, []string{"call", "alate"}) {
		t.Errorf("timed rows = %v", got)
	}
	if at := timed[0].Timestamp(); at == nil || !at.Equal(early) {
		t.Errorf("Timestamp = %v", at)
	}
	if at := (Row{}).Timestamp(); at != nil {
		t.Errorf("empty row Timestamp = %v, want nil", at)
	}

	// One untimed row: the invocation, then messages before calls, decide.
	all := append(timed, Row{Message: &c.graph.AssistantMessages[1], Index: 1})
	tree.SortRows(all)
	if got := ids(all); !slices.Equal(got, []string{"alate", "auntimed", "call"}) {
		t.Errorf("rows = %v", got)
	}
}
