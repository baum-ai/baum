// Package goldentest compares test output with committed golden files.
//
// Regenerate goldens for one package with `go test ./sessions/<pkg> -update`
// and review the diff by hand: a refreshed golden is not proof of correctness.
package goldentest

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files with current output")

// Golden marshals got as indented JSON and compares it byte-for-byte with
// the golden file at path, rewriting the file when -update is set.
func Golden(t *testing.T, path string, got any) {
	t.Helper()
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden value: %v", err)
	}
	data = append(data, '\n')

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("output differs from golden %s (run with -update to regenerate)\ngot:\n%s\nwant:\n%s", path, data, want)
	}
}

// Text compares got with the golden file at path byte for byte, rewriting
// the file when -update is set. Use it for output a reviewer reads as text.
func Text(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("output differs from golden %s (run with -update to regenerate)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}
