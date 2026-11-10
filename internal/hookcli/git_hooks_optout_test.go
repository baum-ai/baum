//go:build !windows

package hookcli

import (
	"os/exec"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

// A repository that sets baum.hooks to false declines the dispatcher check, so
// a dispatcher another tool moved aside stops running baum too.
func TestDispatcherCheckDeclinesWhenTheRepositoryOptedOut(t *testing.T) {
	paths := useTestHookDaemon(t)
	markDaemonRan(t, paths)
	repo := t.TempDir()
	init := exec.Command("git", "init", "-q")
	init.Dir = repo
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(repo)
	if _, _, err := runCLI(t, "hooks", "git", "--check-dispatcher", "post-commit"); err != nil {
		t.Fatalf("declined without an opt-out: %v", err)
	}
	for value, declines := range map[string]bool{"false": true, "no": true, "true": false} {
		cmd := exec.Command("git", "config", "baum.hooks", value)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		_, _, err := runCLI(t, "hooks", "git", "--check-dispatcher", "post-commit")
		if (err != nil) != declines {
			t.Errorf("baum.hooks=%s: err=%v, want declined=%v", value, err, declines)
		}
	}
	// The agent checks do not read it.
	if _, _, err := runCLI(t, "hooks", "agent", "--check", "--user", "claude-code", "stop"); err != nil {
		t.Errorf("agent check: %v", err)
	}
}

// A pid the agent exported counts only for a process that can be the agent.
func TestEnvironmentSessionNeedsAnAgentProcess(t *testing.T) {
	env := map[string]string{"CLAUDE_CODE_SESSION_ID": "abc", "CLAUDE_PID": "7"}
	for name, want := range map[string]bool{"claude": true, "Claude": true, "node": true, "bun": true, "bash": false, "tmux: server": false, "": false, "2.1.34": true, "2.1": false, "v2.1.34": false} {
		processes := []hookevents.Process{{PID: 9, Name: "git"}, {PID: 7, Start: "1", Name: name}}
		agent, session := environmentSession(func(k string) string { return env[k] }, processes)
		if (session != nil) != want || (want && agent != "claude-code") {
			t.Errorf("process %q: agent=%q session=%+v, want named=%v", name, agent, session, want)
		}
	}
}
