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

// otherCodexHome gives the test a home with a stub codex on PATH, its own
// record of Codex homes, and a second Codex home, which it returns with the
// file the stub records each CODEX_HOME it was run with in.
func otherCodexHome(t *testing.T) (dir, seen string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	t.Setenv(agentdirs.Codex.Env, "")
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	stubPluginAgent(t, bin, "codex")
	seen = filepath.Join(t.TempDir(), "seen")
	stub := readHookTestFile(t, filepath.Join(bin, "codex"))
	writeExecutable(t, filepath.Join(bin, "codex"), strings.Replace(stub, "#!/bin/sh\n", "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME\" >> '"+seen+"'\n", 1))
	dir = filepath.Join(home, ".codex-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, seen
}

// Installing with --codex-home runs codex with that home, configures the
// terminal title there, and baum remembers it so it reads the sessions kept
// there.
func TestHooksInstallIntoAnotherCodexHomeRemembersIt(t *testing.T) {
	dir, seen := otherCodexHome(t)
	for _, bad := range []string{"relative/dir", ".codex-work"} {
		if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "codex", "--codex-home", bad); err == nil || !strings.Contains(err.Error(), "--codex-home") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("a refused home still ran codex")
	}
	stdout, stderr, err := runCLI(t, "hooks", "install", "--user", "--agent", "codex", "--codex-home", "~/.codex-work")
	if err != nil || !strings.Contains(stdout, "Connected Codex ("+dir+")") || !strings.Contains(stdout, "/hooks") {
		t.Fatalf("install = %v %q %q", err, stdout, stderr)
	}
	for _, line := range strings.Fields(readHookTestFile(t, seen)) {
		if line != dir {
			t.Fatalf("codex ran with CODEX_HOME=%q, want %q", line, dir)
		}
	}
	if !strings.Contains(readHookTestFile(t, filepath.Join(dir, "config.toml")), "thread-id") {
		t.Fatal("the terminal title was not configured in the chosen home")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatal("the default home's configuration was written")
	}
	if got := agentdirs.Codex.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}
	if got := agentdirs.Claude.Recorded(); len(got) != 0 {
		t.Fatalf("a Codex home was recorded for Claude Code: %v", got)
	}

	// The status names the home it is of, in words and in JSON.
	stdout, _, err = runCLI(t, "hooks", "status", "--user", "--codex-home", dir)
	if err != nil || !strings.Contains(stdout, "Codex ("+dir+"): baum@baum installed, enabled") {
		t.Fatalf("status = %v %q", err, stdout)
	}
	t.Setenv(agentdirs.Codex.Env, dir)
	stdout, _, err = runCLI(t, "hooks", "status", "--user", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Agents []userHooksStatus }
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatal(err)
	}
	if body.Agents[1].Agent != "codex" || body.Agents[1].ConfigDir != dir || body.Agents[0].ConfigDir != "" {
		t.Fatalf("status = %+v", body.Agents)
	}
}

// A Codex session run with another CODEX_HOME records it when it starts or
// takes a prompt; the default home is never recorded.
func TestCodexHookRemembersItsSessionsHome(t *testing.T) {
	dir, _ := otherCodexHome(t)
	useTestHookDaemon(t)
	old := captureTerminal
	captureTerminal = func() *hookevents.Terminal { return nil }
	t.Cleanup(func() { captureTerminal = old })
	run := func(agent, event string) {
		t.Helper()
		if _, _, err := runCLIWithInput(t, `{"session_id":"s-1","cwd":"/work"}`, "hooks", "agent", "--user", agent, event); err != nil {
			t.Fatalf("%s %s: %v", agent, event, err)
		}
	}
	t.Setenv(agentdirs.Codex.Env, filepath.Join(os.Getenv("HOME"), ".codex"))
	run("codex", "session-start")
	if got := agentdirs.Codex.Recorded(); len(got) != 0 {
		t.Fatalf("the default home was recorded: %v", got)
	}
	t.Setenv(agentdirs.Codex.Env, dir)
	run("codex", "stop")
	run("claude-code", "session-start")
	if got := agentdirs.Codex.Recorded(); len(got) != 0 {
		t.Fatalf("recorded %v", got)
	}
	run("codex", "user-prompt-submit")
	if got := agentdirs.Codex.Recorded(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("recorded %v", got)
	}
	if got := agentdirs.Claude.Recorded(); len(got) != 0 {
		t.Fatalf("Claude Code's record: %v", got)
	}
}
