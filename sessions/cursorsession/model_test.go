package cursorsession

import (
	"encoding/json"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func TestBatchBoundaries(t *testing.T) {
	thinking := assistantBubble("thinking")
	thinking.Text = ""
	thinking.Thinking = json.RawMessage(`{"text":"opaque"}`)
	tests := []struct {
		name    string
		bubbles []*cursortranscript.Bubble
		want    int
	}{
		{"text thinking and tools stay together", []*cursortranscript.Bubble{assistantBubble("text"), thinking, toolBubble("a", "loading", "same"), toolBubble("b", "loading", "same")}, 1},
		{"different model call splits consecutive tools", []*cursortranscript.Bubble{toolBubble("a", "loading", "one"), toolBubble("b", "loading", "two")}, 2},
		{"non-tool after tool starts a batch", []*cursortranscript.Bubble{toolBubble("a", "loading", ""), assistantBubble("text")}, 2},
		{"tool after text stays in batch", []*cursortranscript.Bubble{assistantBubble("text"), toolBubble("a", "loading", "")}, 1},
		{"empty bubble joins no batch", []*cursortranscript.Bubble{toolBubble("a", "loading", ""), emptyBubble("e"), toolBubble("b", "loading", "")}, 1},
		{"trailing empty bubble opens nothing", []*cursortranscript.Bubble{toolBubble("a", "loading", ""), emptyBubble("e")}, 1},
		{"degenerate tool is not a batch boundary", []*cursortranscript.Bubble{toolBubble("a", "loading", ""), degenerateToolBubble("d"), toolBubble("b", "loading", "")}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bubbles := append([]*cursortranscript.Bubble{inputBubble("u")}, test.bubbles...)
			graph, _, err := buildBubbles(t, "completed", "", bubbles...)
			if err != nil {
				t.Fatal(err)
			}
			if len(graph.ModelInvocations) != test.want {
				t.Fatalf("invocations = %d, want %d", len(graph.ModelInvocations), test.want)
			}
		})
	}
}

func TestAmbiguousBubbleUsageStaysInputScoped(t *testing.T) {
	first, second := assistantBubble("first"), assistantBubble("second")
	first.TokenCount = &cursortranscript.TokenCount{InputTokens: 10, OutputTokens: 2}
	second.TokenCount = &cursortranscript.TokenCount{InputTokens: 20, OutputTokens: 3}
	graph, report, err := buildBubbles(t, "completed", "", inputBubble("u"), first, second)
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

func TestMissingBatchUsageIsEstimatedFromStoredContext(t *testing.T) {
	input := inputBubble("u")
	input.Text = "abcd"
	input.ContextWindowStatusAtCreation = &cursortranscript.ContextWindowStatus{TokensUsed: 90}
	reply := assistantBubble("a")
	reply.Text = "eight888"
	reply.Thinking = json.RawMessage(`{"text":"abcd"}`)

	graph, report, err := buildBubbles(t, "completed", "", input, reply)
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
