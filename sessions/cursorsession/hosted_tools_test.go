package cursorsession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

func TestHostedToolStatusesAndDecisions(t *testing.T) {
	completed := hostedToolBubble("ok", "completed", "")
	completed.Tool.Result = json.RawMessage(`"{\"saved\":true}"`)
	errored := hostedToolBubble("error", "error", "")
	errored.Tool.UserDecision = "rejected"
	errored.Tool.Error = json.RawMessage(`{"message":"denied"}`)
	cancelled := hostedToolBubble("cancelled", "cancelled", "")
	cancelled.Tool.UserDecision = "rejected"
	loading := hostedToolBubble("loading", "loading", "")
	graph, _, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), completed, errored, cancelled, loading)
	if err != nil {
		t.Fatal(err)
	}
	want := []session.ToolStatus{session.StatusOK, session.StatusError, session.StatusInterrupted, session.StatusNoResult}
	for i, status := range want {
		if graph.ToolUses[i].Status != status {
			t.Errorf("tool %d status = %q, want %q", i, graph.ToolUses[i].Status, status)
		}
	}
	if len(graph.ToolResults) != 3 || !graph.UserInputs[0].Interrupted {
		t.Fatalf("results/input = %+v / %+v", graph.ToolResults, graph.UserInputs[0])
	}
	if graph.ToolResults[0].Output == nil || graph.ToolResults[1].ErrorText == nil {
		t.Fatalf("decoded results = %+v", graph.ToolResults)
	}
}

func TestHostedUnknownToolStatusFails(t *testing.T) {
	_, _, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), hostedToolBubble("unknown", "new-status", ""))
	if err == nil || !strings.Contains(err.Error(), `unknown tool status "new-status"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestHostedToolInputFallsBackFromParamsToRawArgsThenString(t *testing.T) {
	fromRaw := hostedToolBubble("raw", "loading", "")
	fromRaw.Tool.Params = json.RawMessage(`"not json"`)
	fromRaw.Tool.RawArgs = json.RawMessage(`"{\"from\":\"rawArgs\"}"`)
	plain := hostedToolBubble("plain", "loading", "")
	plain.Tool.Params = json.RawMessage(`"plain text"`)
	plain.Tool.RawArgs = nil
	graph, _, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), fromRaw, plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(graph.ToolUses[0].Input); got != `{"from":"rawArgs"}` {
		t.Fatalf("rawArgs input = %s", got)
	}
	if got := string(graph.ToolUses[1].Input); got != `"plain text"` {
		t.Fatalf("string fallback input = %s", got)
	}
}

func TestHostedMixedBubbleQualifiesVendorIDs(t *testing.T) {
	mixed := hostedToolBubble("mixed", "completed", "")
	mixed.Text = "text and tool"
	graph, _, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), mixed)
	if err != nil {
		t.Fatal(err)
	}
	want := map[session.EntityType]string{
		session.EntityAssistantMessage: "mixed:0",
		session.EntityToolUse:          "mixed:1",
		session.EntityToolResult:       "mixed:1",
	}
	for entityType, vendorID := range want {
		found := false
		for _, mapping := range graph.VendorIDs {
			if mapping.OurType == entityType && mapping.VendorIDKind == "bubbleId.block" && mapping.VendorID == vendorID {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s mapping for %s", entityType, vendorID)
		}
	}
}

func TestHostedDeleteFileDoesNotInventAnEditTouch(t *testing.T) {
	deleted := hostedToolBubble("delete", "completed", "")
	deleted.Tool.Name = hostedName("delete_file")
	deleted.Tool.Params = json.RawMessage(`"{\"relativeWorkspacePath\":\"old.go\"}"`)
	graph, _, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), deleted)
	if err != nil {
		t.Fatal(err)
	}
	if deleted := graph.ToolUses[0]; deleted.Verb != session.VerbOther || len(graph.FileTouches) != 0 {
		t.Fatalf("delete mapping = %+v / %+v", deleted, graph.FileTouches)
	}
}
