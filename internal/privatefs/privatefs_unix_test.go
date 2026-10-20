//go:build !windows

package privatefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "private")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := ProtectDirectory(dir); err == nil {
		t.Fatal("accepted a group-readable directory")
	}
	file := filepath.Join(dir, "f")
	for mode, want := range map[os.FileMode]bool{0o600: true, 0o640: false, 0o604: false} {
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(file)
		if err != nil {
			t.Fatal(err)
		}
		if PrivateFile(file, info) != want {
			t.Errorf("mode %o: want %v", mode, want)
		}
	}
}
