package agent_test

import (
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/anthropic"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/agent/cursor"
)

// Only the backends that write nothing per call to a shared path in the run
// directory say their calls may run at once.
func TestBackendsThatMayRunCallsAtOnce(t *testing.T) {
	for name, test := range map[string]struct {
		backend agent.Backend
		want    bool
	}{
		"claude user":     {&claude.Backend{Profile: claude.User}, true},
		"claude isolated": {&claude.Backend{Profile: claude.Isolated}, false},
		"codex local":     {&codex.Backend{Profile: codex.Local}, true},
		"codex container": {&codex.Backend{Profile: codex.Container}, false},
		"anthropic":       {&anthropic.Backend{}, true},
		"cursor-agent":    {&cursor.Backend{}, false},
	} {
		caller, ok := test.backend.(agent.ConcurrentCaller)
		if got := ok && caller.ConcurrentCalls(); got != test.want {
			t.Errorf("%s: concurrent calls %v, want %v", name, got, test.want)
		}
	}
}
