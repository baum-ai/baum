package jobs

import (
	"testing"

	"github.com/baum-ai/baum/agent"
)

// Only a local Claude Code or Codex session can be resumed by a chat.
func TestOnlyClaudeAndCodexReviewsKeepTheirSession(t *testing.T) {
	for backend, want := range map[string]bool{
		agent.BackendClaude: true, agent.BackendCodex: true,
		agent.BackendCursorAgent: false, agent.BackendAnthropic: false, "": false,
	} {
		if got := keepsSession(backend); got != want {
			t.Errorf("keepsSession(%q) = %v, want %v", backend, got, want)
		}
	}
}
