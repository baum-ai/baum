package generate_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
)

// The app's Codex home reaches Codex alone, as CODEX_HOME, over one agent_env
// sets; a Claude Code directory chosen beside it never reaches Codex, nor the
// Codex home Claude Code.
func TestDefaultBackendsGiveCodexItsHome(t *testing.T) {
	cfg := config.Config{AgentEnv: map[string]string{"CODEX_HOME": "/from/agent-env", "OTHER": "kept"}}
	backends := generate.DefaultBackendsFor(cfg)
	if got := backends.Codex("m").(*codex.Backend).Env; !slices.Equal(got, []string{"CODEX_HOME=/from/agent-env", "OTHER=kept"}) {
		t.Fatalf("without a choice: %v", got)
	}

	cfg.CodexHome = "/home/dev/.codex-work"
	cfg.ClaudeConfigDir = "/home/dev/.claude.personal"
	backends = generate.DefaultBackendsFor(cfg)
	if got := backends.Codex("m").(*codex.Backend).Env; !slices.Equal(got, []string{"OTHER=kept", "CODEX_HOME=/home/dev/.codex-work"}) {
		t.Fatalf("codex: %v", got)
	}
	if got := backends.Claude("m").(*claude.Backend).Env; !slices.Equal(got, []string{"CODEX_HOME=/from/agent-env", "OTHER=kept", "CLAUDE_CONFIG_DIR=/home/dev/.claude.personal"}) {
		t.Fatalf("claude: %v", got)
	}

	backends = generate.DefaultBackendsFor(config.Config{CodexHome: "/home/dev/.codex-work"})
	if got := backends.Codex("m").(*codex.Backend).Env; !slices.Equal(got, []string{"CODEX_HOME=/home/dev/.codex-work"}) {
		t.Fatalf("without agent_env: %v", got)
	}
}
