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
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$1 $2\" in\n'plugin marketplace') exit 0 ;;\n'plugin list') if [ -f '" + state + "' ]; then printf '%s\\n' '" + installed + "'; else printf '%s\\n' '" + empty + "'; fi ;;\n'plugin install'|'plugin add') : > '" + state + "' ;;\n'plugin uninstall'|'plugin remove') rm -f '" + state + "' ;;\n*) exit 2 ;;\nesac\n"
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
