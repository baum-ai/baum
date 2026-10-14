package locallink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/baum-ai/baum/redact"
	"github.com/baum-ai/baum/sessions/session"
)

// A secret's keyword at the end of one string must not mask the ids, kinds
// and statuses in the strings after it: the graph reads back the same.
func TestRedactGraphKeepsTheGraphsStructure(t *testing.T) {
	e, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	endings := []string{
		"password", "password:", "password=", "PASSWORD=", "Authorization: Bearer", "token", "api_key=", "secret:", "Authorization: Basic",
		"export AWS_SECRET_ACCESS_KEY=", "x-api-key:", "passwd ", "pwd=", "-p", "Bearer",
	}
	for _, end := range endings {
		out := "line\n" + end
		g := &session.Graph{
			Sessions: []session.Session{{ID: "root"}},
			UserInputs: []session.UserInput{{ID: "p1", SessionID: "root", Ordinal: 1, Text: "set the " + end,
				TriggeredAt: &ts, Kind: session.KindPrompt, Origin: session.OriginHuman}},
			ToolUses: []session.ToolUse{{ID: "u1", SessionID: "root", UserInputID: "p1", Name: "Bash", Verb: session.VerbExecute, Input: json.RawMessage(`{"command":"echo ` + end + `"}`), Timestamp: &ts}},
			ToolResults: []session.ToolResult{{ID: "r1", ToolUseID: "u1", SessionID: "root", UserInputID: "p1", Status: session.StatusOK,
				OutputText: &out, Timestamp: &ts}},
		}
		for _, indent := range []bool{false, true} {
			red, err := RedactGraph(context.Background(), e, g, indent)
			if err != nil {
				t.Fatal(err)
			}
			var back session.Graph
			if err := json.Unmarshal(red, &back); err != nil {
				t.Errorf("%q indent=%v: unmarshal: %v", end, indent, err)
				continue
			}
			changed := []string{}
			if back.UserInputs[0].ID != "p1" {
				changed = append(changed, "input id "+back.UserInputs[0].ID)
			}
			if back.UserInputs[0].Kind != session.KindPrompt || back.UserInputs[0].Origin != session.OriginHuman {
				changed = append(changed, "kind/origin")
			}
			if back.ToolUses[0].UserInputID != "p1" || back.ToolResults[0].ToolUseID != "u1" {
				changed = append(changed, "links")
			}
			if back.ToolResults[0].Status != session.StatusOK {
				changed = append(changed, "status "+string(back.ToolResults[0].Status))
			}
			if len(changed) > 0 {
				t.Errorf("%q indent=%v: structure masked: %v", end, indent, changed)
			}
		}
	}
}
