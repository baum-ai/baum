//go:build unix

package cli

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcli"
)

// desktop-setup reports the agents on the terminal's PATH and records where
// they are; login-path, which the desktop app reads, then puts that directory
// first.
func TestDesktopSetupRecordsTheTerminalsAgents(t *testing.T) {
	terminal := t.TempDir()
	writeScript(t, filepath.Join(terminal, "codex"), `[ "$1" = --version ] && echo "codex-cli 0.166.0"`, 0o755)
	writeScript(t, filepath.Join(terminal, "claude"), `[ "$1" = --version ] && echo "2.1.100 (Claude Code)"`, 0o755)
	stubLoginShell(t, "/usr/bin:/bin", nil)
	t.Setenv("PATH", terminal)

	stdout, _, err := run(t, "desktop-setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Claude Code: " + filepath.Join(terminal, "claude") + ", version 2.1.100: not supported. baum needs " + agentcliMinimum(t, "claude-code"),
		"Codex: " + filepath.Join(terminal, "codex") + ", version 0.166.0: supported.",
		"terminal.json",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	file, err := agentcli.RecordFile()
	if err != nil {
		t.Fatal(err)
	}
	record, err := agentcli.Load(file)
	if err != nil || len(record.Agents) != 2 || record.Agents[1].Version != "0.166.0" {
		t.Fatalf("%+v, %v", record, err)
	}
	dir, _ := filepath.EvalSymlinks(terminal)
	if report := loginPathJSON(t); !slices.Equal(report.Path, []string{dir, "/usr/bin", "/bin"}) {
		t.Fatalf("login PATH %q", report.Path)
	}

	// A terminal without the agents records nothing.
	t.Setenv("PATH", t.TempDir())
	stdout, _, err = run(t, "desktop-setup", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report desktopSetup
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || len(report.Agents) != 2 || report.Agents[0].Path != "" || report.Record != file {
		t.Fatalf("%+v, %v: %s", report, err, stdout)
	}
	if record, _ := agentcli.Load(file); len(record.Agents) != 0 {
		t.Fatalf("%+v", record)
	}
}

func agentcliMinimum(t *testing.T, slug string) string {
	t.Helper()
	agent, ok := agentcli.Lookup(slug)
	if !ok {
		t.Fatal(slug)
	}
	return agent.Minimum
}
