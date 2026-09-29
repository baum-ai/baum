package cursorsession

import (
	"encoding/json"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func name(value string) *string { return &value }

func inputBubble(id string) *cursortranscript.Bubble {
	return &cursortranscript.Bubble{Key: "bubbleId:test:" + id, ComposerID: "test", BubbleID: id, Type: cursortranscript.BubbleTypeUser, Text: "Do it", CreatedAt: "2026-01-01T00:00:00Z"}
}

func assistantBubble(id string) *cursortranscript.Bubble {
	return &cursortranscript.Bubble{Key: "bubbleId:test:" + id, ComposerID: "test", BubbleID: id, Type: cursortranscript.BubbleTypeAssistant, Text: "text", CreatedAt: "2026-01-01T00:00:01Z"}
}

func toolBubble(id, status, modelCall string) *cursortranscript.Bubble {
	bubble := assistantBubble(id)
	bubble.Text = ""
	bubble.Tool = &cursortranscript.ToolRecord{
		Name: name("other_tool"), ToolCallID: "call-" + id, ModelCallID: modelCall,
		Status: status, Params: json.RawMessage(`{}`), Tool: json.RawMessage(`1`),
	}
	return bubble
}

func emptyBubble(id string) *cursortranscript.Bubble {
	bubble := assistantBubble(id)
	bubble.Text = ""
	return bubble
}

func degenerateToolBubble(id string) *cursortranscript.Bubble {
	bubble := emptyBubble(id)
	bubble.Tool = &cursortranscript.ToolRecord{Tool: json.RawMessage(`0`)}
	return bubble
}

func buildBubbles(t *testing.T, status, committed string, bubbles ...*cursortranscript.Bubble) (*session.Graph, *Report, error) {
	t.Helper()
	refs := make([]cursortranscript.BubbleRef, 0, len(bubbles))
	for _, bubble := range bubbles {
		refs = append(refs, cursortranscript.BubbleRef{BubbleID: bubble.BubbleID, Type: bubble.Type})
	}
	return Build(&cursortranscript.Tree{Composer: &cursortranscript.Composer{Data: &cursortranscript.ComposerData{
		ComposerID: "test", Status: status, CommittedToBranch: committed,
		CreatedAt: 1767225600000, Headers: refs,
	}, Bubbles: bubbles}})
}

func TestOutcomePrecedence(t *testing.T) {
	tests := []struct {
		status, committed string
		want              session.Outcome
	}{
		{"aborted", "branch", session.OutcomeCommitted},
		{"aborted", "", session.OutcomeInterrupted},
		{"completed", "", session.OutcomeUnknown},
		{"none", "", session.OutcomeUnknown},
	}
	for _, test := range tests {
		graph, _, err := buildBubbles(t, test.status, test.committed, inputBubble("u"))
		if err != nil {
			t.Fatal(err)
		}
		if graph.Root().Outcome != test.want {
			t.Errorf("status=%s committed=%q outcome=%q, want %q", test.status, test.committed, graph.Root().Outcome, test.want)
		}
	}
}

func TestPreInputUsageBecomesSessionScoped(t *testing.T) {
	orphan := emptyBubble("orphan")
	orphan.TokenCount = &cursortranscript.TokenCount{InputTokens: 5, OutputTokens: 1}
	graph, report, err := buildBubbles(t, "completed", "", orphan, inputBubble("u"))
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.ModelInvocations) != 0 || len(graph.TokenUsage) != 1 {
		t.Fatalf("invocations/usage = %d/%d", len(graph.ModelInvocations), len(graph.TokenUsage))
	}
	usage := graph.TokenUsage[0]
	if usage.Scope != session.UsageScopeSession || usage.UserInputID != nil || *usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	if report.Tokens.AmbiguousBubbles != 1 {
		t.Fatalf("token report = %+v", report.Tokens)
	}
}

func TestWorkingDirFallsBackToComposerData(t *testing.T) {
	input := inputBubble("u")
	graph, _, err := Build(&cursortranscript.Tree{Composer: &cursortranscript.Composer{Data: &cursortranscript.ComposerData{
		ComposerID: "test", Status: "completed", CreatedAt: 1767225600000,
		Headers: []cursortranscript.BubbleRef{{BubbleID: input.BubbleID, Type: input.Type}},
		WorkspaceIdentifier: &cursortranscript.WorkspaceIdentifier{
			URI: cursortranscript.ResourceURI{FSPath: "/workspace/project"},
			Raw: json.RawMessage(`{"uri":{"fsPath":"/workspace/project"}}`),
		},
	}, Bubbles: []*cursortranscript.Bubble{input}}})
	if err != nil {
		t.Fatal(err)
	}
	if dir := graph.Root().WorkingDir; dir == nil || *dir != "/workspace/project" {
		t.Fatalf("working_dir = %v", dir)
	}
}

func TestLeadingSlashRemainsAPrompt(t *testing.T) {
	input := inputBubble("u")
	input.Text = "/review"
	graph, _, err := buildBubbles(t, "completed", "", input)
	if err != nil {
		t.Fatal(err)
	}
	if graph.UserInputs[0].Kind != session.KindPrompt {
		t.Fatalf("input kind = %q", graph.UserInputs[0].Kind)
	}
}
