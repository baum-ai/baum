package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

func contextBundle() *bundlepkg.Bundle {
	return &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}}}
}

// withContext is the authored fixture with its summary context replaced, or
// removed when context is nil.
func withContext(t *testing.T, context any) []byte {
	t.Helper()
	doc, err := decodeObject(authoredFixture(), "fixture")
	checkNoError(t, err)
	summary := doc["summary"].(map[string]any)
	if context == nil {
		delete(summary, "context")
	} else {
		summary["context"] = context
	}
	return tileJSON(t, doc)
}

func TestModelSchemaRequiresSummaryContext(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	encoded, err := v.ModelSchema(nil)
	checkNoError(t, err)
	var schema map[string]any
	checkNoError(t, json.Unmarshal(encoded, &schema))
	summary := schema["properties"].(map[string]any)["summary"].(map[string]any)
	if !containsString(stringSlice(summary["required"]), "context") {
		t.Fatal("the model schema does not require summary.context")
	}
	context := summary["properties"].(map[string]any)["context"].(map[string]any)
	if acceptsNull(context) {
		t.Errorf("the model schema lets summary.context be null: %v", context)
	}
	if description, _ := context["description"].(string); description == "" || strings.Contains(description, "Absent") {
		t.Errorf("the model is told about documents without a context: %q", description)
	}
	checkEqual(t, []string{"area"}, stringSlice(context["required"]))
	parts := context["properties"].(map[string]any)
	if acceptsNull(parts["area"].(map[string]any)) {
		t.Error("the model schema lets summary.context.area be null")
	}
	for _, name := range []string{"reached_by", "contracts", "related"} {
		if !acceptsNull(parts[name].(map[string]any)) {
			t.Errorf("the model schema does not let summary.context.%s be null", name)
		}
	}
}

func TestSummaryContextIsRequiredOfTheModelOnly(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	for name, context := range map[string]any{
		"no context": nil,
		"no area":    map[string]any{"related": map[string]any{"text": "The worker drains the queue.", "refs": []any{}}},
		"blank area": map[string]any{"area": map[string]any{"text": "  ", "refs": []any{}}},
	} {
		_, findings := v.Prepare(withContext(t, context), contextBundle(), "")
		if !strings.Contains(findings, "context") {
			t.Errorf("%s: findings %q do not name the context", name, findings)
		}
	}

	// A stored document from before the context existed still validates.
	example := storedExample(t)
	delete(example["summary"].(map[string]any), "context")
	if findings := v.ValidateStored(tileJSON(t, example), nil); len(findings) > 0 {
		t.Fatalf("a stored document without a context is invalid: %v", findings)
	}
}

func TestNormalizationCleansSummaryContext(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	checkout := t.TempDir()
	for _, file := range []string{"head/api/handler.go", "merge-base/queue/legacy.go"} {
		path := filepath.Join(checkout, file)
		checkNoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		checkNoError(t, os.WriteFile(path, []byte("package x\n"), 0o644))
	}
	checkNoError(t, os.WriteFile(filepath.Join(checkout, "secret.txt"), []byte("outside"), 0o644))
	ref := func(path string, symbol any) map[string]any { return map[string]any{"path": path, "symbol": symbol} }
	written := map[string]any{
		"area": map[string]any{"text": "  The API serves the queue.  ", "refs": []any{
			ref("./api/handler.go", "Handler"),
			ref("/api/handler.go", "Handler"),
			ref("head/api/handler.go", "  "),
			ref("merge-base/queue/legacy.go", nil),
			ref("api", nil),
			ref("api/missing.go", nil),
			ref("../secret.txt", nil),
			ref("head/../../secret.txt", nil),
			ref("head/", nil),
			map[string]any{"symbol": "NoPath"},
			"api/handler.go",
		}},
		"reached_by": map[string]any{"text": " \n ", "refs": []any{ref("api/handler.go", nil)}},
		"contracts":  nil,
		"related":    map[string]any{"text": "The worker drains it."},
	}
	for _, tc := range []struct {
		name      string
		directory string
		area      []any
	}{
		{"with a checkout", checkout, []any{
			ref("api/handler.go", "Handler"),
			ref("api/handler.go", nil),
			ref("queue/legacy.go", nil),
			ref("api", nil),
		}},
		// A stack review's directory holds no checkout: paths are only tidied.
		{"without a checkout", "", []any{
			ref("api/handler.go", "Handler"),
			ref("api/handler.go", nil),
			ref("queue/legacy.go", nil),
			ref("api", nil),
			ref("api/missing.go", nil),
			ref("head", nil),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var copied any
			encoded, err := json.Marshal(written)
			checkNoError(t, err)
			checkNoError(t, json.Unmarshal(encoded, &copied))
			prepared, findings := v.Prepare(withContext(t, copied), contextBundle(), tc.directory)
			checkEqual(t, "", findings)
			out, err := decodeObject(prepared, "prepared")
			checkNoError(t, err)
			got := out["summary"].(map[string]any)["context"].(map[string]any)
			want := map[string]any{
				"area":    map[string]any{"text": "The API serves the queue.", "refs": tc.area},
				"related": map[string]any{"text": "The worker drains it.", "refs": []any{}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("context = %v\nwant %v", got, want)
			}
		})
	}
}

func TestSummaryContextKeepsAtMostEightRefs(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	var refs []any
	for _, name := range "abcdefghij" {
		refs = append(refs, map[string]any{"path": string(name) + ".go", "symbol": nil})
	}
	prepared, findings := v.Prepare(withContext(t, map[string]any{"area": map[string]any{"text": "Many files.", "refs": refs}}), contextBundle(), "")
	checkEqual(t, "", findings)
	out, err := decodeObject(prepared, "prepared")
	checkNoError(t, err)
	kept := out["summary"].(map[string]any)["context"].(map[string]any)["area"].(map[string]any)["refs"].([]any)
	checkEqual(t, refs[:maxContextRefs], kept)
}

// The context survives completion and a model-free refresh.
func TestSummaryContextIsStoredAndRefreshed(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	context := map[string]any{
		"area":      map[string]any{"text": "The API serves the queue.", "refs": []any{map[string]any{"path": "main.go", "symbol": "main"}}},
		"contracts": map[string]any{"text": "Jobs are acknowledged once.", "refs": []any{}},
	}
	bundle := contextBundle()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	published, findings := v.Complete(withContext(t, context), bundle, "", "fixture-model", now)
	checkEqual(t, "", findings)
	if stored := v.ValidateStored(published, bundle); len(stored) > 0 {
		t.Fatalf("published document is not a valid stored document: %v", stored)
	}
	refreshed, err := v.Refresh(published, bundle, now.Add(time.Hour))
	checkNoError(t, err)
	for name, document := range map[string][]byte{"published": published, "refreshed": refreshed} {
		out, err := decodeObject(document, name)
		checkNoError(t, err)
		checkEqual(t, context, out["summary"].(map[string]any)["context"])
	}
}

func TestOutputRedactionMasksSummaryContext(t *testing.T) {
	redactor := testRedactor(t)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Change: "modified"}}}
	document, err := json.Marshal(map[string]any{"summary": map[string]any{"context": map[string]any{
		"area": map[string]any{"text": "Connects with " + leakedDatabaseURL, "refs": []any{
			map[string]any{"path": "main.go", "symbol": leakedProviderKey},
			map[string]any{"path": "deploy/" + leakedProviderKey + "/app.env", "symbol": nil},
		}},
	}}})
	checkNoError(t, err)
	redacted, err := newTestOutputRedactor(t, redactor, bundle, map[string]bool{"main.go": true}).Document(context.Background(), document)
	checkNoError(t, err)
	requireNoLeakedSecret(t, "context", redacted)
	var doc map[string]any
	checkNoError(t, json.Unmarshal(redacted, &doc))
	refs := doc["summary"].(map[string]any)["context"].(map[string]any)["area"].(map[string]any)["refs"].([]any)
	checkEqual(t, "main.go", refs[0].(map[string]any)["path"])
}
