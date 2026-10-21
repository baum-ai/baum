package agent

import (
	"errors"
	"strings"
)

// IsProviderErrorStatus reports whether an ErrorStatus class is a provider
// failure, as opposed to a tool error. Each class is a fixed word, so a host may
// report it where the message it classifies may not be reported.
func IsProviderErrorStatus(status string) bool { return providerErrorStatuses[status] }

var providerErrorStatuses = map[string]bool{
	"authentication_error": true, "rate_limit": true, "provider_overloaded": true,
	"invalid_schema": true, "invalid_request": true, "not_found": true, "upgrade_required": true,
}

// ErrorStatus classifies an error message into a fixed word.
func ErrorStatus(message string) string {
	message = strings.ToLower(message)
	for _, check := range []struct{ Text, Status string }{
		{"exceeds maximum allowed tokens", "read_token_limit"},
		{"permission", "permission_denied"}, {"not exist", "file_not_found"}, {"not found", "file_not_found"},
		{"401", "authentication_error"}, {"authentication", "authentication_error"},
		{"429", "rate_limit"}, {"rate limit", "rate_limit"}, {"overloaded", "provider_overloaded"},
		{"invalid_json_schema", "invalid_schema"}, {"400", "invalid_request"}, {"invalid_request", "invalid_request"},
	} {
		if strings.Contains(message, check.Text) {
			return check.Status
		}
	}
	return "error"
}

// LastErrorStatus is the classified status of an attempt's last error event,
// or empty when the backend recorded none.
func LastErrorStatus(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "error" {
			return events[i].Status
		}
	}
	return ""
}

// LastProviderErrorStatus is LastErrorStatus when the last error event's class
// was read from the provider (Event.FromProvider), and otherwise empty: a class
// guessed from free text may be a request ID or quoted content that reads
// like a status.
func LastProviderErrorStatus(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "error" {
			if events[i].FromProvider {
				return events[i].Status
			}
			return ""
		}
	}
	return ""
}

// HTTPErrorStatus classifies the HTTP status code of a failed provider
// response: 401 and 403 are a rejected key, 404 a model or route that does not
// exist, 426 a protocol the endpoint refuses, 429 a rate limit, 529 an
// overloaded provider, and any other 4xx but 408 a request the provider
// refused. Any other code, 408 and 5xx included, gives "".
func HTTPErrorStatus(code int) string {
	switch {
	case code == 401 || code == 403:
		return "authentication_error"
	case code == 404:
		return "not_found"
	case code == 426:
		return "upgrade_required"
	case code == 429:
		return "rate_limit"
	case code == 529:
		return "provider_overloaded"
	case code >= 400 && code < 500 && code != 408:
		return "invalid_request"
	}
	return ""
}

// IsRejectedStatus reports whether a provider error class is a refusal a
// retry cannot change: a rejected key, schema or request, a missing model or
// route, or a refused protocol.
func IsRejectedStatus(status string) bool {
	switch status {
	case "authentication_error", "invalid_schema", "invalid_request", "not_found", "upgrade_required":
		return true
	}
	return false
}

// ProviderFailure is the error of a harness call whose provider failed with
// the error class status. refused says the class was read from the provider's
// HTTP status or a structured field of its response, not guessed from free
// text: only then is a refusal a RejectedError, which the loop does not retry.
// Any other failure is ReasonProviderUnavailable.
func ProviderFailure(harness, status string, refused bool) error {
	if refused && IsRejectedStatus(status) {
		return &RejectedError{Status: status, Err: errors.New(harness + " reported the provider's refusal")}
	}
	return errors.New(ReasonProviderUnavailable)
}
