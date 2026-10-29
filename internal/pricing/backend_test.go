package pricing

import (
	"testing"

	"github.com/baum-ai/baum/agent"
)

// A cursor-agent run is never priced from the list, even under a model name
// the list knows and with usage it could price.
func TestCursorAgentIsNeverPricedFromTheList(t *testing.T) {
	list := Embedded()
	const model = "claude-opus-5-5"

	speed, hour, prompt := "standard", int64(0), int64(1000)
	usage := agent.Usage{Input: 1000, Output: 100, Speed: &speed, CacheCreate1h: &hour, MinPrompt: &prompt, MaxPrompt: &prompt}
	accounting := &agent.Accounting{Usage: usage, Attempts: []agent.UsageAttempt{{ModelSource: "provider_reported", Model: model, Usage: usage, Calls: 1, ChargedTokens: 1100}}}
	if cost, reason := list.BackendAccountingCost(agent.BackendClaude, accounting); cost == nil {
		t.Fatalf("claude cost = nil (%q)", reason)
	}
	if cost, reason := list.BackendAccountingCost(agent.BackendCursorAgent, accounting); cost != nil || reason != ReasonBilledByCursor {
		t.Errorf("cursor-agent cost = %v (%q)", cost, reason)
	}
	outcome := agent.Outcome{Usage: usage}
	if list.BackendOutcomeCost(agent.BackendClaude, model, outcome) == nil || list.BackendOutcomeCost(agent.BackendCursorAgent, model, outcome) != nil {
		t.Error("outcome cost: claude must be priced, cursor-agent must not")
	}
}
