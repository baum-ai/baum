package agent

import (
	"errors"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

// These strings are stored in the database and matched when read back.
func TestReasonTextIsStable(t *testing.T) {
	for got, want := range map[string]string{
		ReasonValidationRejected:                     "validation rejected the document",
		ReasonProviderUnavailable:                    "the model provider was unavailable",
		GenerationBudgetExceededReason("tokens"):     "generation budget exceeded: tokens",
		GenerationBudgetExceededReason("wall_clock"): "generation budget exceeded: wall_clock",
		GenerationBudgetExceededReason(""):           "generation budget exceeded: ",
		ProviderRejectedReason("refusal"):            "the model provider rejected the request: refusal",
	} {
		if got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
	}
}

func TestBackendNamesMatchConfig(t *testing.T) {
	for name, backend := range map[string]config.Backend{
		BackendClaude: config.BackendClaude, BackendCodex: config.BackendCodex,
		BackendAnthropic: config.BackendAnthropic, BackendCursorAgent: config.BackendCursorAgent,
	} {
		if name != string(backend) {
			t.Errorf("%q differs from config %q", name, backend)
		}
	}
}

func TestErrorStatus(t *testing.T) {
	for message, want := range map[string]string{
		"HTTP 429 Too Many": "rate_limit", "file does not exist": "file_not_found", "boom": "error",
	} {
		if got := ErrorStatus(message); got != want {
			t.Errorf("ErrorStatus(%q) = %q, want %q", message, got, want)
		}
	}
	events := []Event{{Kind: "error", Status: "rate_limit"}, {Kind: "document"}}
	if got := LastErrorStatus(events); got != "rate_limit" {
		t.Errorf("LastErrorStatus = %q", got)
	}
	if got := LastErrorStatus(nil); got != "" {
		t.Errorf("LastErrorStatus(nil) = %q", got)
	}
	// A class guessed from free text is not the provider's.
	read := []Event{{Kind: "error", Status: "invalid_schema", FromProvider: true}, {Kind: "error", Status: "authentication_error"}}
	if got := LastProviderErrorStatus(read); got != "" {
		t.Errorf("LastProviderErrorStatus = %q, want none for a free-text class", got)
	}
	if got := LastProviderErrorStatus(read[:1]); got != "invalid_schema" {
		t.Errorf("LastProviderErrorStatus = %q, want invalid_schema", got)
	}
}

// A provider's 4xx but 408 and 429 is a refusal no retry changes; 408, 429
// and 5xx are outages.
func TestHTTPErrorStatus(t *testing.T) {
	for code, want := range map[int]string{
		400: "invalid_request", 401: "authentication_error", 403: "authentication_error", 404: "not_found",
		408: "", 413: "invalid_request", 422: "invalid_request", 426: "upgrade_required", 429: "rate_limit",
		500: "", 502: "", 529: "provider_overloaded", 0: "", 200: "",
	} {
		got := HTTPErrorStatus(code)
		if got != want {
			t.Errorf("HTTPErrorStatus(%d) = %q, want %q", code, got, want)
		}
		if got != "" && !IsProviderErrorStatus(got) {
			t.Errorf("HTTPErrorStatus(%d) = %q is not a provider error class", code, got)
		}
	}
}

func TestProviderFailure(t *testing.T) {
	for _, status := range []string{"authentication_error", "invalid_schema", "invalid_request", "not_found", "upgrade_required"} {
		var rejected *RejectedError
		if err := ProviderFailure("codex", status, true); !errors.As(err, &rejected) || rejected.Status != status {
			t.Errorf("ProviderFailure(%q) = %v, want a rejection", status, err)
		}
		// A class guessed from free text never ends the run.
		if err := ProviderFailure("codex", status, false); err == nil || err.Error() != ReasonProviderUnavailable {
			t.Errorf("ProviderFailure(%q, text) = %v, want %q", status, err, ReasonProviderUnavailable)
		}
	}
	for _, status := range []string{"rate_limit", "provider_overloaded", "error", "", "file_not_found"} {
		if err := ProviderFailure("codex", status, true); err == nil || err.Error() != ReasonProviderUnavailable {
			t.Errorf("ProviderFailure(%q) = %v, want %q", status, err, ReasonProviderUnavailable)
		}
	}
}
