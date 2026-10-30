//go:build !windows

package baumcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubPluginAgent(t *testing.T, dir, agent string) string {
	t.Helper()
	binary := "claude"
	empty := "[]"
	installed := `[{"id":"baum@baum","scope":"user","version":"1.0.0","enabled":true}]`
	if agent == "codex" {
		binary = "codex"
		empty = `{"installed":[],"available":[]}`
		installed = `{"installed":[{"pluginId":"baum@baum","installed":true,"enabled":true,"version":"1.0.0"}],"available":[]}`
	}
	state := filepath.Join(t.TempDir(), "installed")
	log := state + ".log"
	// An update records the newer version the list then reports.
	installed = strings.Replace(installed, "1.0.0", `'"$(if [ -s '`+state+`' ]; then cat '`+state+`'; else echo 1.0.0; fi)"'`, 1)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$1 $2\" in\n'plugin marketplace') exit 0 ;;\n'plugin list') if [ -f '" + state + "' ]; then printf '%s\\n' '" + installed + "'; else printf '%s\\n' '" + empty + "'; fi ;;\n'plugin install'|'plugin add') : > '" + state + "' ;;\n'plugin update') echo 1.2.0 > '" + state + "' ;;\n'plugin uninstall'|'plugin remove') rm -f '" + state + "' ;;\n*) exit 2 ;;\nesac\n"
	writeExecutable(t, filepath.Join(dir, binary), script)
	return log
}

func TestHooksCommandsUseNativeMarketplaceAndReportTrustSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	log := stubPluginAgent(t, bin, "claude-code")
	writeAgentSettings(t, home, ".claude/settings.json", `{"custom":"keep"}`)
	if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code", "--agent", "unknown"); err == nil {
		t.Fatal("accepted unknown agent")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("validation changed an earlier agent")
	}
	stdout, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code")
	if err != nil || !strings.Contains(stdout, "Installed and enabled baum@baum") || !strings.Contains(stdout, "Review hook trust") {
		t.Fatalf("install=%v %s", err, stdout)
	}
	if got := readHookTestFile(t, filepath.Join(home, ".claude", "settings.json")); got != `{"custom":"keep"}` {
		t.Fatalf("edited user hook settings: %s", got)
	}
	stdout, _, err = runCLI(t, "hooks", "status", "--user", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Agents []userHooksStatus }
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Agents[0].Installed || !body.Agents[0].Enabled || body.Agents[0].Plugin != "baum@baum" {
		t.Fatalf("status=%+v", body)
	}
	if _, _, err := runCLI(t, "hooks", "uninstall", "--user", "--agent", "claude-code"); err != nil {
		t.Fatal(err)
	}
	calls := readHookTestFile(t, log)
	for _, want := range []string{"plugin marketplace add baum-ai/baum", "plugin install baum@baum --scope user --json", "plugin uninstall baum@baum --scope user --json"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in %s", want, calls)
		}
	}
}

func TestHooksUpdateBringsInstalledPluginsToTheLatestVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	log := stubPluginAgent(t, bin, "claude-code")
	stubPluginAgent(t, bin, "codex")
	if _, _, err := runCLI(t, "hooks", "update", "--user"); err == nil || !strings.Contains(err.Error(), "hooks install --user") {
		t.Fatalf("update without a plugin=%v", err)
	}
	if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code"); err != nil {
		t.Fatal(err)
	}
	// Without --agent only the agent with the plugin updates; Codex has none.
	stdout, _, err := runCLI(t, "hooks", "update", "--user")
	if err != nil || strings.TrimSpace(stdout) != "Updated baum@baum for Claude Code from 1.0.0 to 1.2.0. Restart running Claude Code sessions to use the new hooks." {
		t.Fatalf("update=%v %q", err, stdout)
	}
	stdout, _, err = runCLI(t, "hooks", "update", "--user", "--agent", "claude-code")
	if err != nil || !strings.Contains(stdout, "already the latest version (1.2.0)") {
		t.Fatalf("second update=%v %q", err, stdout)
	}
	if _, _, err := runCLI(t, "hooks", "update", "--user", "--agent", "codex"); err == nil {
		t.Fatal("updated a Codex without the plugin")
	}
	calls := readHookTestFile(t, log)
	for _, want := range []string{"plugin marketplace update baum", "plugin update baum@baum --scope user --json"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in %s", want, calls)
		}
	}
}

func TestConfigDirectoryWithoutCLIIsNotDetectedAsInstallable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "hooks", "install", "--user"); err == nil || !strings.Contains(err.Error(), "CLI on PATH") {
		t.Fatalf("error=%v", err)
	}
	statuses := userHooksStatuses(t.Context())
	for _, s := range statuses {
		if s.Detected || s.Installed {
			t.Fatalf("status=%+v", s)
		}
	}
}

func TestSetupInstallsPluginsOnlyForHostedUsersAndRespectsConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		login, skip, decline, installs bool
	}{
		{name: "open source"}, {name: "logged in", login: true, installs: true}, {name: "skipped", login: true, skip: true}, {name: "declined", login: true, decline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := workingEntire(t)
			setTempConfigHome(t)
			log := stubPluginAgent(t, stub.dir, "claude-code")
			t.Setenv(serverEnvVar, failingServer(t))
			if tc.login {
				t.Setenv(tokenEnvVar, "provisioned")
			}
			parent := t.TempDir()
			newGitHubRepo(t, parent, "demo")
			t.Chdir(parent)
			args := []string{"setup", "--all"}
			input := "yes\n"
			if tc.decline {
				input = "no\n"
			}
			if tc.skip {
				args = append(args, "--skip-user-hooks")
			}
			stdout, _, err := runCLIWithInput(t, input, args...)
			if err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(log)
			installed := strings.Contains(string(calls), "plugin install baum@baum")
			if installed != tc.installs {
				t.Fatalf("installed=%v want=%v output=%s calls=%s", installed, tc.installs, stdout, calls)
			}
			if tc.login && !tc.skip && !strings.Contains(stdout, "public baum-ai/baum Git marketplace") {
				t.Fatalf("plan omitted network/marketplace scope: %s", stdout)
			}
		})
	}
}
