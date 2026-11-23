package reviewruns

import (
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/agentdirs"
)

// A run's agent writes its sessions to the directory its environment names,
// which Settings can choose, so the sessions are removed from every Claude
// Code configuration directory and Codex home baum knows.
func TestRemoveSessionsLooksInEveryKnownAgentDirectory(t *testing.T) {
	env := fakeHomes(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	recorded := homes{claude: filepath.Join(t.TempDir(), "claude-work"), codex: filepath.Join(t.TempDir(), "codex-work")}
	root := t.TempDir()
	run := makeRun(t, root, agent.RunDirPrefix+"1")
	paths := []string{
		env.claudeSession(t, run, "e-1"), env.codexSession(t, run, "e-2"),
		recorded.claudeSession(t, run, "r-1"), recorded.codexSession(t, run, "r-2"),
	}
	if _, err := agentdirs.Claude.Remember(recorded.claude); err != nil {
		t.Fatal(err)
	}
	if _, err := agentdirs.Codex.Remember(recorded.codex); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSessions(root, run, "e-1", "e-2", "r-1", "r-2"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if exists(path) {
			t.Errorf("%s stayed", path)
		}
	}
}
