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

// DraftOutline returns Base as a staged review's in-memory draft is before any
// details are written: every section cut back to its outline with pending
// details, and no context or reading time (the tiles package's draft-outline
// fixture).
func DraftOutline(t testing.TB) map[string]any {
	t.Helper()
	doc := Base(t)
	summary := doc["summary"].(map[string]any)
	delete(summary, "context")
	delete(summary, "review_minutes")
	summary["recommendation"] = nil
	summary["attention"] = nil
	summary["risk_score"].(map[string]any)["reasons"] = []any{}
	for _, raw := range doc["sections"].([]any) {
		s := raw.(map[string]any)
		s["details"] = "pending"
		s["summary"] = nil
		s["attention"] = nil
		s["risks"], s["evidence"], s["blocks"] = []any{}, []any{}, []any{}
		delete(s["derived"].(map[string]any), "review_reason")
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
