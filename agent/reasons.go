package agent

// The failure reasons below are stored in the database, so their text must
// not change.
const (
	ReasonValidationRejected  = "validation rejected the document"
	ReasonProviderUnavailable = "the model provider was unavailable"
)

// GenerationBudgetExceededReason is the reason of a run that exhausted the
// named budget.
func GenerationBudgetExceededReason(name string) string { return "generation budget exceeded: " + name }
