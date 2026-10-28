package cursorsession

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func hostedName(value string) *string { return &value }

func hostedInputBubble(id string) *cursortranscript.Bubble {
	return &cursortranscript.Bubble{Key: "bubbleId:test:" + id, ComposerID: "test", BubbleID: id, Type: cursortranscript.BubbleTypeUser, Text: "Do it", CreatedAt: "2026-01-01T00:00:00Z"}
}

func hostedAssistantBubble(id string) *cursortranscript.Bubble {
	return &cursortranscript.Bubble{Key: "bubbleId:test:" + id, ComposerID: "test", BubbleID: id, Type: cursortranscript.BubbleTypeAssistant, Text: "text", CreatedAt: "2026-01-01T00:00:01Z"}
}

func hostedToolBubble(id, status, modelCall string) *cursortranscript.Bubble {
	bubble := hostedAssistantBubble(id)
	bubble.Text = ""
	bubble.Tool = &cursortranscript.ToolRecord{
		Name: hostedName("other_tool"), ToolCallID: "call-" + id, ModelCallID: modelCall,
		Status: status, Params: json.RawMessage(`{}`), Tool: json.RawMessage(`1`),
	}
	return bubble
}

func hostedEmptyBubble(id string) *cursortranscript.Bubble {
	bubble := hostedAssistantBubble(id)
	bubble.Text = ""
	return bubble
}

func hostedDegenerateToolBubble(id string) *cursortranscript.Bubble {
	bubble := hostedEmptyBubble(id)
	bubble.Tool = &cursortranscript.ToolRecord{Tool: json.RawMessage(`0`)}
	return bubble
}

func hostedBuildBubbles(t *testing.T, status, committed string, bubbles ...*cursortranscript.Bubble) (*session.Graph, *Report, error) {
	t.Helper()
	refs := make([]cursortranscript.BubbleRef, 0, len(bubbles))
	for _, bubble := range bubbles {
		refs = append(refs, cursortranscript.BubbleRef{BubbleID: bubble.BubbleID, Type: bubble.Type})
	}
	return hostedBuild(&cursortranscript.Tree{Composer: &cursortranscript.Composer{Data: &cursortranscript.ComposerData{
		ComposerID: "test", Status: status, CommittedToBranch: committed,
		CreatedAt: 1767225600000, Headers: refs,
	}, Bubbles: bubbles}})
}

func TestHostedOutcomePrecedence(t *testing.T) {
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
		graph, _, err := hostedBuildBubbles(t, test.status, test.committed, hostedInputBubble("u"))
		if err != nil {
			t.Fatal(err)
		}
		if graph.Root().Outcome != test.want {
			t.Errorf("status=%s committed=%q outcome=%q, want %q", test.status, test.committed, graph.Root().Outcome, test.want)
		}
	}
}

func TestHostedPreInputUsageBecomesSessionScoped(t *testing.T) {
	orphan := hostedEmptyBubble("orphan")
	orphan.TokenCount = &cursortranscript.TokenCount{InputTokens: 5, OutputTokens: 1}
	graph, report, err := hostedBuildBubbles(t, "completed", "", orphan, hostedInputBubble("u"))
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

func TestHostedWorkingDirFallsBackToComposerData(t *testing.T) {
	input := hostedInputBubble("u")
	graph, _, err := hostedBuild(&cursortranscript.Tree{Composer: &cursortranscript.Composer{Data: &cursortranscript.ComposerData{
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

func TestHostedLeadingSlashRemainsAPrompt(t *testing.T) {
	input := hostedInputBubble("u")
	input.Text = "/review"
	graph, _, err := hostedBuildBubbles(t, "completed", "", input)
	if err != nil {
		t.Fatal(err)
	}
	if graph.UserInputs[0].Kind != session.KindPrompt {
		t.Fatalf("input kind = %q", graph.UserInputs[0].Kind)
	}
}
