package pricing

import (
	"testing"

	"github.com/baum-ai/baum/agent"
)

func TestLocalEstimateRequiresKnownExplicitModel(t *testing.T) {
	for _, model := range []string{"", "not-a-model"} {
		if Embedded().Estimate(model, 1000, 3) != nil {
			t.Fatal("invented model price", model)
		}
	}
	if estimate := Embedded().Estimate("claude-sonnet-4-5", 1000, 3); estimate == nil || *estimate <= 0 {
		t.Fatal(estimate)
	}
}
func TestOutcomeCostKeepsMissingAndAmbiguousUsageUnknown(t *testing.T) {
	list := Embedded()
	for _, usage := range []agent.Usage{{}, {Input: 10, Unreported: true}, {Input: 10}, {Input: 10, CacheCreate: 5, Speed: new("standard")}} {
		if list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage}) != nil {
			t.Fatal("invented cost", usage)
		}
	}
	usage := agent.Usage{Input: 100, Output: 20, CacheRead: 50, Speed: new("standard"), MinPrompt: new(int64(150)), MaxPrompt: new(int64(150))}
	cost := list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage})
	want, ok := list.Cost(list.Normalize("claude-sonnet-4-5", ""), Usage{Vendor: VendorClaudeCode, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, Speed: SpeedStandard, PromptRangeKnown: true, MinPromptTokens: 150, MaxPromptTokens: 150})
	if !ok || cost == nil || *cost != want {
		t.Fatal("disjoint usage not preserved", cost, want)
	}
}
