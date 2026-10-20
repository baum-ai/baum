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
	"invalid_schema": true, "invalid_request": true,
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

// HTTPErrorStatus classifies the HTTP status code of a failed provider
// response as the anthropic backend does: 401 and 403 are a rejected key, 429
// a rate limit, 529 an overloaded provider, and any other 4xx but 408 a request
// the provider refused. Any other code gives "".
func HTTPErrorStatus(code int) string {
	switch {
	case code == 401 || code == 403:
		return "authentication_error"
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
// retry cannot change: a rejected key, schema or request.
func IsRejectedStatus(status string) bool {
	return status == "authentication_error" || status == "invalid_schema" || status == "invalid_request"
}

// ProviderFailure is the error of a harness call whose provider failed with
// the error class status: a RejectedError, which the loop does not retry, for
// a refusal, and otherwise ReasonProviderUnavailable.
func ProviderFailure(harness, status string) error {
	if IsRejectedStatus(status) {
		return &RejectedError{Status: status, Err: errors.New(harness + " reported the provider's refusal")}
	}
	return errors.New(ReasonProviderUnavailable)
}
