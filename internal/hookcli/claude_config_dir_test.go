//go:build !windows

package hookcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/hookevents"
)

// otherClaudeHome gives the test a home with a stub claude on PATH, its own
// record of Claude Code configuration directories, and a second
// configuration directory, which it returns with the stub's call log.
func otherClaudeHome(t *testing.T) (dir, log string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	t.Setenv(agentdirs.Codex.Env, "")
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	log = stubPluginAgent(t, bin, "claude-code")
	dir = filepath.Join(home, ".claude.personal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

// Installing with --claude-config-dir runs claude with that directory, and
// baum remembers it so it reads the sessions kept there.
func TestHooksInstallIntoAnotherClaudeConfigDirectoryRemembersIt(t *testing.T) {
	dir, _ := otherClaudeHome(t)
	seen := filepath.Join(t.TempDir(), "seen")
	// The stub's own script is replaced by one that also records the
	// directory claude was given.
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	stub := readHookTestFile(t, filepath.Join(bin, "claude"))
	writeExecutable(t, filepath.Join(bin, "claude"), strings.Replace(stub, "#!/bin/sh\n", "#!/bin/sh\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\" >> '"+seen+"'\n", 1))

	for _, bad := range []string{"relative/dir", ".claude.personal"} {
		if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code", "--claude-config-dir", bad); err == nil || !strings.Contains(err.Error(), "--claude-config-dir") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("a refused directory still ran claude")
	}
	stdout, stderr, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code", "--claude-config-dir", "~/.claude.personal")
	if err != nil || !strings.Contains(stdout, "Connected Claude Code ("+dir+")") {
		t.Fatalf("install = %v %q %q", err, stdout, stderr)
	}
	for _, line := range strings.Fields(readHookTestFile(t, seen)) {
		if line != dir {
			t.Fatalf("claude ran with CLAUDE_CONFIG_DIR=%q, want %q", line, dir)
		}
	}
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}

	// The status names the directory it is of, in words and in JSON.
	stdout, _, err = runCLI(t, "hooks", "status", "--user", "--claude-config-dir", dir)
	if err != nil || !strings.Contains(stdout, "Claude Code ("+dir+"): baum@baum installed, enabled") {
		t.Fatalf("status = %v %q", err, stdout)
	}
	stdout, _, err = runCLI(t, "hooks", "status", "--user", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Agents []userHooksStatus }
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatal(err)
	}
	if body.Agents[0].Agent != "claude-code" || body.Agents[0].ConfigDir != dir || body.Agents[1].ConfigDir != "" {
		t.Fatalf("status = %+v", body.Agents)
	}

	// Removing the plugin keeps the directory: its sessions are still there.
	if stdout, _, err := runCLI(t, "hooks", "uninstall", "--user", "--agent", "claude-code"); err != nil || !strings.Contains(stdout, "Claude Code ("+dir+")") {
		t.Fatalf("uninstall = %v %q", err, stdout)
	}
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded after uninstall %v", got)
	}
}

// With CLAUDE_CONFIG_DIR set as the person runs claude, no flag is needed;
// the default directory is neither named nor recorded.
func TestHooksInstallFollowsTheEnvironmentsClaudeConfigDirectory(t *testing.T) {
	dir, _ := otherClaudeHome(t)
	stdout, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code")
	if err != nil || !strings.Contains(stdout, "Connected Claude Code (baum plugin") {
		t.Fatalf("default install = %v %q", err, stdout)
	}
	if got := agentdirs.Claude.Recorded(); len(got) != 0 {
		t.Fatalf("the default directory was recorded: %v", got)
	}
	t.Setenv(agentdirs.Claude.Env, dir)
	stdout, _, err = runCLI(t, "hooks", "install", "--user", "--agent", "claude-code")
	if err != nil || !strings.Contains(stdout, "Connected Claude Code ("+dir+")") {
		t.Fatalf("install = %v %q", err, stdout)
	}
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}
}

// A session of a directory the plugin was installed into by hand records the
// directory when it starts or takes a prompt, so the daemon can read it.
func TestClaudeHookRemembersItsSessionsConfigDirectory(t *testing.T) {
	dir, _ := otherClaudeHome(t)
	useTestHookDaemon(t)
	old := captureTerminal
	captureTerminal = func() *hookevents.Terminal { return nil }
	t.Cleanup(func() { captureTerminal = old })
	t.Setenv(agentdirs.Claude.Env, dir)
	run := func(agent, event string) {
		t.Helper()
		if _, _, err := runCLIWithInput(t, `{"session_id":"s-1","cwd":"/work"}`, "hooks", "agent", "--user", agent, event); err != nil {
			t.Fatalf("%s %s: %v", agent, event, err)
		}
	}
	// Tool events, the most frequent, and other agents' hooks do not.
	run("claude-code", "stop")
	run("codex", "session-start")
	if got := agentdirs.Claude.Recorded(); len(got) != 0 {
		t.Fatalf("recorded %v", got)
	}
	run("claude-code", "user-prompt-submit")
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}
	// A directory that cannot be recorded never fails the hook.
	t.Setenv(agentdirs.Claude.Env, filepath.Join(dir, "missing"))
	run("claude-code", "session-start")
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}
}
