package pricing

import "github.com/baum-ai/baum/agent"

// ReasonBilledByCursor is why a cursor-agent run has no estimate or recorded
// cost: Cursor bills it through the user's own subscription, so no list price
// describes it, and pricing its tokens at an API rate would mislead.
const ReasonBilledByCursor = "billed_by_cursor_subscription"

// subscriptionReason is the reason a backend's runs are never priced from the
// list, empty for a backend they are.
func subscriptionReason(backend string) string {
	if backend == agent.BackendCursorAgent {
		return ReasonBilledByCursor
	}
	return ""
}

// BackendEstimate is Estimate for a run of backend: nil with the reason for a
// backend the list does not price.
func (l *List) BackendEstimate(backend, model string, maxTokens, maxCalls int) (*float64, string) {
	if reason := subscriptionReason(backend); reason != "" {
		return nil, reason
	}
	return l.Estimate(model, maxTokens, maxCalls), ""
}

// BackendOutcomeCost is OutcomeCost for a run of backend: nil for a backend
// the list does not price.
func (l *List) BackendOutcomeCost(backend, model string, outcome agent.Outcome) *float64 {
	if subscriptionReason(backend) != "" {
		return nil
	}
	return l.OutcomeCost(model, outcome)
}

// BackendAccountingCost is AccountingCost for a run of backend: nil with the
// reason for a backend the list does not price.
func (l *List) BackendAccountingCost(backend string, a *agent.Accounting) (*float64, string) {
	if reason := subscriptionReason(backend); reason != "" {
		return nil, reason
	}
	return l.AccountingCost(a)
}
