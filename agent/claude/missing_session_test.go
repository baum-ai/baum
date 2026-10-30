//go:build unix

package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

const missingSession = "00000000-0000-4000-8000-000000000000"

// missingSessionScript is what Claude Code 2.1.312 prints for --resume of a
// session it does not have: one failed result naming it on stdout, the same
// line on stderr, exit 1. With --fork-session the result's session_id is a
// fresh one that holds nothing.
func missingSessionScript(resultSession string) string {
	event := `{"type":"result","subtype":"error_during_execution","duration_ms":0,"is_error":true,"num_turns":0,"session_id":"` + resultSession + `","total_cost_usd":0,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0},"errors":["No conversation found with session ID: ` + missingSession + `"]}`
	return "cat >/dev/null\ncat <<'EVENTS'\n" + event + "\nEVENTS\necho 'No conversation found with session ID: " + missingSession + "' >&2\nexit 1\n"
}

type countingBackend struct {
	*Backend
	calls *int
}

func (b countingBackend) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	*b.calls++
	return b.Backend.Run(ctx, r)
}

func TestMissingSessionIsReportedAsSuch(t *testing.T) {
	for _, c := range []struct {
		name          string
		fork          bool
		resultSession string
	}{
		{"resume", false, missingSession},
		{"fork", true, "7b1f0c55-2d0e-4b8e-9a43-5f4a9a1c2e10"},
	} {
		script := fakeClaude(t, "2.1.312", missingSessionScript(c.resultSession))
		r := request(t.TempDir(), nil)
		r.ResumeSession, r.ForkSession = missingSession, c.fork
		got, err := (&Backend{Binary: script}).Run(context.Background(), r)
		if !errors.Is(err, agent.ErrSessionNotFound) || !strings.Contains(err.Error(), "No conversation found") {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if got.SessionID != "" || got.Calls != 0 {
			t.Errorf("%s: result session %q, %d calls", c.name, got.SessionID, got.Calls)
		}
		// The loop fails it at once instead of retrying a provider failure.
		calls := 0
		g := agent.Generator{Backend: countingBackend{&Backend{Binary: script}, &calls}, RetryDelay: func(int) time.Duration { return 0 }}
		var outcome agent.Outcome
		if _, err := g.Continue(context.Background(), agent.Budgets{MaxCalls: 2, MaxTokens: 100, MaxProviderAttempts: 3}, r, &outcome); !errors.Is(err, agent.ErrSessionNotFound) || calls != 1 {
			t.Errorf("%s: Continue err %v after %d calls", c.name, err, calls)
		}
	}
	// The same failure of a fresh session is a provider failure.
	script := fakeClaude(t, "2.1.312", missingSessionScript(missingSession))
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir(), nil)); err == nil || errors.Is(err, agent.ErrSessionNotFound) {
		t.Errorf("a fresh session: %v", err)
	}
}
