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

// ProviderRejectedReason is the reason of a run the model provider refused in
// a way a retry cannot change. status is the fixed word of the refusal:
// authentication_error, invalid_request, refusal or context_window_exceeded.
func ProviderRejectedReason(status string) string {
	return "the model provider rejected the request: " + status
}

// RejectedError is a backend failure the loop does not retry: a rejected key,
// a request the provider refused, a declined prompt or a conversation over
// the model's context window. Its text starts with ProviderRejectedReason.
type RejectedError struct {
	// Status is the fixed word of the refusal.
	Status string
	Err    error
}

func (e *RejectedError) Error() string {
	return ProviderRejectedReason(e.Status) + ": " + e.Err.Error()
}
func (e *RejectedError) Unwrap() error { return e.Err }
