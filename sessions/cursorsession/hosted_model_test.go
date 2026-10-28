package cursorsession

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func TestHostedBatchBoundaries(t *testing.T) {
	thinking := hostedAssistantBubble("thinking")
	thinking.Text = ""
	thinking.Thinking = json.RawMessage(`{"text":"opaque"}`)
	tests := []struct {
		hostedName string
		bubbles    []*cursortranscript.Bubble
		want       int
	}{
		{"text thinking and tools stay together", []*cursortranscript.Bubble{hostedAssistantBubble("text"), thinking, hostedToolBubble("a", "loading", "same"), hostedToolBubble("b", "loading", "same")}, 1},
		{"different model call splits consecutive tools", []*cursortranscript.Bubble{hostedToolBubble("a", "loading", "one"), hostedToolBubble("b", "loading", "two")}, 2},
		{"non-tool after tool starts a batch", []*cursortranscript.Bubble{hostedToolBubble("a", "loading", ""), hostedAssistantBubble("text")}, 2},
		{"tool after text stays in batch", []*cursortranscript.Bubble{hostedAssistantBubble("text"), hostedToolBubble("a", "loading", "")}, 1},
		{"empty bubble joins no batch", []*cursortranscript.Bubble{hostedToolBubble("a", "loading", ""), hostedEmptyBubble("e"), hostedToolBubble("b", "loading", "")}, 1},
		{"trailing empty bubble opens nothing", []*cursortranscript.Bubble{hostedToolBubble("a", "loading", ""), hostedEmptyBubble("e")}, 1},
		{"degenerate tool is not a batch boundary", []*cursortranscript.Bubble{hostedToolBubble("a", "loading", ""), hostedDegenerateToolBubble("d"), hostedToolBubble("b", "loading", "")}, 1},
	}
	for _, test := range tests {
		t.Run(test.hostedName, func(t *testing.T) {
			bubbles := append([]*cursortranscript.Bubble{hostedInputBubble("u")}, test.bubbles...)
			graph, _, err := hostedBuildBubbles(t, "completed", "", bubbles...)
			if err != nil {
				t.Fatal(err)
			}
			if len(graph.ModelInvocations) != test.want {
				t.Fatalf("invocations = %d, want %d", len(graph.ModelInvocations), test.want)
			}
		})
	}
}

func TestHostedAmbiguousBubbleUsageStaysInputScoped(t *testing.T) {
	first, second := hostedAssistantBubble("first"), hostedAssistantBubble("second")
	first.TokenCount = &cursortranscript.TokenCount{InputTokens: 10, OutputTokens: 2}
	second.TokenCount = &cursortranscript.TokenCount{InputTokens: 20, OutputTokens: 3}
	graph, report, err := hostedBuildBubbles(t, "completed", "", hostedInputBubble("u"), first, second)
	if err != nil {
		t.Fatal(err)
	}
	if graph.ModelInvocations[0].TotalTokens != 0 || len(graph.TokenUsage) != 2 {
		t.Fatalf("invocation/usage = %+v / %+v", graph.ModelInvocations[0], graph.TokenUsage)
	}
	for _, usage := range graph.TokenUsage {
		if usage.Scope != session.UsageScopeInput || usage.UserInputID == nil || *usage.UserInputID != graph.UserInputs[0].ID {
			t.Fatalf("usage owner = %+v", usage)
		}
	}
	if report.Tokens.AmbiguousBubbles != 2 {
		t.Fatalf("token report = %+v", report.Tokens)
	}
}

func TestHostedMissingBatchUsageIsEstimatedFromStoredContext(t *testing.T) {
	input := hostedInputBubble("u")
	input.Text = "abcd"
	input.ContextWindowStatusAtCreation = &cursortranscript.ContextWindowStatus{TokensUsed: 90}
	reply := hostedAssistantBubble("a")
	reply.Text = "eight888"
	reply.Thinking = json.RawMessage(`{"text":"abcd"}`)

	graph, report, err := hostedBuildBubbles(t, "completed", "", input, reply)
	if err != nil {
		t.Fatal(err)
	}
	invocation := graph.ModelInvocations[0]
	if invocation.InputTokens != 90 || invocation.OutputTokens != 3 || invocation.TotalTokens != 93 {
		t.Fatalf("estimated invocation = %+v, want 90 input and 3 output tokens", invocation)
	}
	if invocation.TokenBasis != session.TokenBasisEstimated || report.Tokens.EstimatedInvocations != 1 {
		t.Fatalf("estimate basis/report = %q / %+v", invocation.TokenBasis, report.Tokens)
	}
}
