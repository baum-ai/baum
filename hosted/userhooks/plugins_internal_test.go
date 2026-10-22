package userhooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRollbackMarkerFailureReturnsCompletedSettingsChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := Migrate(home, "codex"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateDir(home), "user-hooks", "codex.plugin")
	denied := errors.New("marker removal denied")
	result, err := restoreLegacy(home, "codex", func(path string) error {
		if path != marker {
			t.Fatalf("removed unexpected path %q", path)
		}
		if installed, err := HasLegacyEvent(home, "codex", "stop"); err != nil || !installed {
			t.Fatalf("marker removal ran before settings installation: %v %v", installed, err)
		}
		return denied
	})
	if !errors.Is(err, denied) || !result.Changed || result.Agent != "codex" {
		t.Fatalf("partial result=%+v error=%v", result, err)
	}
	if !PluginManaged(home, "codex") {
		t.Fatal("failed removal lost plugin choice")
	}
	before, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := RestoreLegacy(home, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Changed || PluginManaged(home, "codex") {
		t.Fatalf("retry=%+v still managed=%v", retry, PluginManaged(home, "codex"))
	}
	after, err := os.ReadFile(result.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("retry changed installed hooks: %v", err)
	}
}
