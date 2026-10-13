package agent

// The failure reasons below are text hosts may store and match when read
// back, so it must not change.
const (
	ReasonValidationRejected  = "validation rejected the document"
	ReasonProviderUnavailable = "the model provider was unavailable"
)

// GenerationBudgetExceededReason is the reason of a run that exhausted the
// named budget.
func GenerationBudgetExceededReason(name string) string { return "generation budget exceeded: " + name }
