package agent

import "strings"

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
