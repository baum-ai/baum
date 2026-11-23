package generate_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
)

// The app's Claude Code configuration directory reaches Claude Code alone, as
// CLAUDE_CONFIG_DIR, over one agent_env sets.
func TestDefaultBackendsGiveClaudeItsConfigDirectory(t *testing.T) {
	cfg := config.Config{AgentEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/from/agent-env", "OTHER": "kept"}}
	backends := generate.DefaultBackendsFor(cfg)
	if got := backends.Claude("m").(*claude.Backend).Env; !slices.Equal(got, []string{"CLAUDE_CONFIG_DIR=/from/agent-env", "OTHER=kept"}) {
		t.Fatalf("without a choice: %v", got)
	}

	cfg.ClaudeConfigDir = "/home/dev/.claude.personal"
	backends = generate.DefaultBackendsFor(cfg)
	if got := backends.Claude("m").(*claude.Backend).Env; !slices.Equal(got, []string{"OTHER=kept", "CLAUDE_CONFIG_DIR=/home/dev/.claude.personal"}) {
		t.Fatalf("claude: %v", got)
	}
	if got := backends.Codex("m").(*codex.Backend).Env; !slices.Equal(got, []string{"CLAUDE_CONFIG_DIR=/from/agent-env", "OTHER=kept"}) {
		t.Fatalf("codex: %v", got)
	}

	backends = generate.DefaultBackendsFor(config.Config{ClaudeConfigDir: "/home/dev/.claude.personal"})
	if got := backends.Claude("m").(*claude.Backend).Env; !slices.Equal(got, []string{"CLAUDE_CONFIG_DIR=/home/dev/.claude.personal"}) {
		t.Fatalf("without agent_env: %v", got)
	}
}
