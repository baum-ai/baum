package agent

import (
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
}
