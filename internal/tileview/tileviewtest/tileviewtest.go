// Package tileviewtest gives tests of the tile routes a synthetic review.
package tileviewtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Base returns the synthetic baum.review/1 document of the tiles package's
// tests (a pull request of acme/widgets), decoded, so a test can change it.
func Base(t testing.TB) map[string]any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "tiles", "testdata", "base.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Encode returns the document as JSON.
func Encode(t testing.TB, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
