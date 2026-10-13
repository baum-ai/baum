package codex

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unsandboxed Container profile exists for a host that supplies its own
// boundary. Nothing a user runs locally (the CLI and its internals) may name
// it or ask Codex for the unsandboxed mode.
func TestLocalCodePathsNeverUseTheUnsandboxedProfile(t *testing.T) {
	scanned := 0
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for _, forbidden := range []string{"codex.Container", "danger-full-access"} {
				if strings.Contains(string(body), forbidden) {
					t.Errorf("%s references %s", path, forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("no files scanned; the guard walks the wrong directories")
	}
}
