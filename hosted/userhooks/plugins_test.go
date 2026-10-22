package userhooks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/hosted/userhooks"
)

func TestPluginMigrationPreservesSettingsAndPreventsLegacyReinstall(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, agent := range userhooks.Agents() {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			settings, _ := agenthooks.LookupUserWide(agent)
			path := filepath.Join(home, filepath.FromSlash(settings.SettingsFile))
			original := []byte("{\r\n  \"custom\": true,\r\n  \"hooks\": {\"Stop\": [{\"hooks\": [{\"type\":\"command\",\"command\":\"my-hook\"}]}]}\r\n}\r\n")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := userhooks.Install(home, agent); err != nil {
				t.Fatal(err)
			}
			if legacy, err := userhooks.HasLegacyEvent(home, agent, "stop"); err != nil || !legacy {
				t.Fatalf("legacy = %v, %v", legacy, err)
			}
			if _, err := userhooks.Migrate(home, agent); err != nil {
				t.Fatal(err)
			}
			if !userhooks.PluginManaged(home, agent) {
				t.Fatal("migration not remembered")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("settings = %q, %v", got, err)
			}
			if _, err := userhooks.Install(home, agent); err == nil {
				t.Fatal("legacy install recreated hooks")
			}
			if _, err := userhooks.Migrate(home, agent); err != nil {
				t.Fatal(err)
			}
			if legacy, err := userhooks.HasLegacyEvent(home, agent, "stop"); err != nil || legacy {
				t.Fatalf("legacy after migration = %v, %v", legacy, err)
			}
			if _, err := userhooks.RestoreLegacy(home, agent); err != nil {
				t.Fatal(err)
			}
			if _, err := userhooks.Install(home, agent); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPluginMigrationRefusesInvalidSettingsWithoutMarkingMigrated(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := userhooks.Migrate(home, "codex"); err == nil {
		t.Fatal("accepted invalid settings")
	}
	if userhooks.PluginManaged(home, "codex") {
		t.Fatal("remembered failed migration")
	}
	if _, err := userhooks.HasLegacyEvent(home, "codex", "stop"); err == nil {
		t.Fatal("unreadable hooks must suppress plugin")
	}
}

func TestLegacyRollbackRetainsPluginChoiceUntilSettingsAreWritten(t *testing.T) {
	for _, failure := range []string{"invalid settings", "backup write failure"} {
		t.Run(failure, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			home := t.TempDir()
			const agent = "claude-code"
			if _, err := userhooks.Migrate(home, agent); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			contents := []byte("{\"custom\":true}\n")
			if failure == "invalid settings" {
				contents = []byte("invalid JSON\n")
			}
			if err := os.WriteFile(path, contents, 0600); err != nil {
				t.Fatal(err)
			}
			blocked := filepath.Join(state, "baum", "backups", "claude-code-settings.json")
			if failure == "backup write failure" {
				if err := os.MkdirAll(blocked, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := userhooks.RestoreLegacy(home, agent); err == nil {
				t.Fatal("rollback unexpectedly succeeded")
			}
			if !userhooks.PluginManaged(home, agent) {
				t.Fatal("failed rollback lost plugin choice")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(contents) {
				t.Fatalf("settings changed on failure: %q %v", got, err)
			}
			if _, err := userhooks.Install(home, agent); err == nil {
				t.Fatal("ordinary install bypassed retained plugin choice")
			}
			if failure == "invalid settings" {
				if err := os.WriteFile(path, []byte("{\"custom\":true}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := userhooks.RestoreLegacy(home, agent); err != nil {
				t.Fatal(err)
			}
			if userhooks.PluginManaged(home, agent) {
				t.Fatal("successful rollback retained plugin choice")
			}
			if installed, err := userhooks.HasLegacyEvent(home, agent, "stop"); err != nil || !installed {
				t.Fatalf("legacy hook missing: %v %v", installed, err)
			}
			if _, err := userhooks.RestoreLegacy(home, agent); err != nil {
				t.Fatalf("retry not idempotent: %v", err)
			}
		})
	}
}
