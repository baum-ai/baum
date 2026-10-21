package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventPath(t *testing.T) {
	real := t.TempDir()
	// The run directory as the caller names it reaches the CLI resolved, as
	// $TMPDIR's /var/folders/... reaches it as /private/var/folders/... on
	// macOS.
	directory := filepath.Join(t.TempDir(), "run")
	if err := os.Symlink(real, directory); err != nil {
		t.Skipf("symlink: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{"", ""},
		{filepath.Join(directory, "bundle.json"), "bundle.json"},
		{filepath.Join(resolved, "head", "main.go"), filepath.Join("head", "main.go")},
		{filepath.Join("head", "main.go"), filepath.Join("head", "main.go")},
		{directory, "."},
		{filepath.Dir(directory), OutsideRun},
		{filepath.Join(filepath.Dir(resolved), "other", "main.go"), OutsideRun},
		{filepath.Join(directory, "a\nb"), "a?b"},
		{filepath.Join(directory, strings.Repeat("x", 300)), strings.Repeat("x", 256)},
	} {
		if got := EventPath(directory, tc.path); got != tc.want {
			t.Errorf("EventPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
