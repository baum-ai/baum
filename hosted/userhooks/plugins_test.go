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
			if err := userhooks.RestoreLegacy(home, agent); err != nil {
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
