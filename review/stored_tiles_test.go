package review

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
	reviewschema "github.com/baum-ai/baum/schema"
)

// storedTiles are synthetic tiles shaped like those releases before tiles
// were removed stored on a section or a risk: one of a kind the catalogue
// offered last, one of a kind it had retired, and one of a kind it never had.
func storedTiles() []any {
	anchors := []any{map[string]any{"type": "hunk", "id": "h_141757b19901"}}
	return []any{
		map[string]any{"id": "tile_retry_fix", "kind": "bug-fix", "anchors": anchors, "code": []any{}, "data": map[string]any{"bug": "A failed job is retried forever.", "fix": "Stop after five attempts."}},
		map[string]any{"id": "tile_store_split", "kind": "refactor", "anchors": anchors, "code": []any{}, "data": map[string]any{"was": "One store for every queue.", "now": "A store per queue."}},
		map[string]any{"id": "tile_preview", "kind": "ui-preview", "anchors": anchors, "code": []any{}, "data": map[string]any{"screenshot": "before.png"}},
	}
}

// storedTilesDocument is the example document with storedTiles on its first
// section and that section's first risk.
func storedTilesDocument(t *testing.T) map[string]any {
	t.Helper()
	doc := storedExample(t)
	section := firstSection(doc)
	section["tiles"] = storedTiles()
	section["risks"].([]any)[0].(map[string]any)["tiles"] = storedTiles()
	return doc
}

// A stored document's section and risk tiles are dropped when it is read:
// it passes stored validation, and the document read is the one without
// them, whatever their kind.
func TestStoredTilesAreDropped(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	stored := encodeJSON(t, storedTilesDocument(t))
	if reviewschema.Validate(stored) == nil {
		t.Fatal("the schema accepts a document with tiles")
	}
	if findings := validator.ValidateStored(stored, nil); findings != nil {
		t.Fatalf("stored document rejected: %q", findings)
	}
	_, normalized, err := decodeStored(stored, "stored review")
	if err != nil {
		t.Fatal(err)
	}
	want := decodedJSON(t, encodeJSON(t, storedExample(t)))
	if !reflect.DeepEqual(decodedJSON(t, normalized), want) {
		t.Fatalf("normalization changed more than the tiles:\n%s", normalized)
	}
	if _, again, err := decodeStored(normalized, "stored review"); err != nil || !bytes.Equal(again, normalized) {
		t.Fatalf("normalizing again changed the document (%v)", err)
	}
	dropped, err := DropStoredTiles(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedJSON(t, dropped), want) {
		t.Fatalf("DropStoredTiles changed more than the tiles:\n%s", dropped)
	}
}

// A document without tiles is returned byte for byte, even when "tiles"
// appears in it as text.
func TestDropStoredTilesLeavesOtherDocumentsUnchanged(t *testing.T) {
	doc := storedExample(t)
	firstSection(doc)["title"] = "tiles"
	var indented bytes.Buffer
	if err := json.Indent(&indented, encodeJSON(t, doc), "", "  "); err != nil {
		t.Fatal(err)
	}
	if got, err := DropStoredTiles(indented.Bytes()); err != nil || !bytes.Equal(got, indented.Bytes()) {
		t.Fatalf("a document without tiles was re-encoded (%v)", err)
	}
	if _, got, err := decodeStored(indented.Bytes(), "stored review"); err != nil || !bytes.Equal(got, indented.Bytes()) {
		t.Fatalf("a document no rule applies to was re-encoded (%v)", err)
	}
	if _, err := DropStoredTiles([]byte(`["tiles"]`)); err == nil {
		t.Fatal("a document that is not an object was accepted")
	}
}

// A refresh of a stored document with tiles succeeds and stores the document
// without them.
func TestRefreshStoredDocumentWithTiles(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := storedTilesDocument(t)
	header := doc["review"].(map[string]any)
	bundle := &bundlepkg.Bundle{Repository: "example-org/taskline", PullRequest: bundlepkg.PullRequest{Number: 212, State: "open", HeadSHA: header["head"].(string), BaseSHA: header["base"].(string), MergeBaseSHA: header["merge_base"].(string)}, Sources: bundlepkg.Sources{"diff": true}}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	refreshed, err := validator.Refresh(encodeJSON(t, doc), bundle, now)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	expected, err := validator.Refresh(encodeJSON(t, storedExample(t)), bundle, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(refreshed, expected) {
		t.Fatalf("refresh differs from the refresh of the document without tiles:\n%s\n%s", refreshed, expected)
	}
	if reviewschema.Validate(refreshed) != nil {
		t.Fatal("the refreshed document does not pass the schema")
	}
}

// The previous version a generation carries has no tiles, on its sections or
// their risks.
func TestPreviousVersionOfDropsTiles(t *testing.T) {
	previous, err := PreviousVersionOf(encodeJSON(t, storedTilesDocument(t)), 2)
	if err != nil {
		t.Fatal(err)
	}
	want, err := PreviousVersionOf(encodeJSON(t, storedExample(t)), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previous, want) {
		t.Fatal("the previous version differs from the one of the document without tiles")
	}
	for _, section := range previous.Sections {
		if bytes.Contains(section.Content, []byte(`"tiles"`)) {
			t.Fatalf("section %s carries tiles: %s", section.ID, section.Content)
		}
	}
}

// A previous section that carries tiles, as a host that read an earlier
// release's stored document may still hand it in, completes without them
// when the model leaves the section out, so the new version is restored
// from it and passes the schema.
func TestCompletionDropsTilesOfAnOmittedPreviousSection(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	gone := historySection("gone", testHunk, "rsk_gone")
	gone["tiles"] = storedTiles()
	gone["risks"].([]any)[0].(map[string]any)["tiles"] = storedTiles()
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: "code", Change: "added"}}, Sources: bundlepkg.Sources{"diff": true}}
	bundle.Previous = &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_gone", Title: "Gone", Kind: "logical", Content: encodeJSON(t, gone)}}}
	completed, findings := validator.Complete(authoredFixture(), bundle, t.TempDir(), "fixture-model", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	if findings != "" {
		t.Fatal(findings)
	}
	if !strings.Contains(string(completed), `"sec_gone"`) {
		t.Fatalf("the omitted previous section was not restored:\n%s", completed)
	}
	if strings.Contains(string(completed), `"tiles"`) {
		t.Fatalf("the completed document carries tiles:\n%s", completed)
	}
	if err := reviewschema.Validate(completed); err != nil {
		t.Fatalf("the completed document does not pass the schema: %v", err)
	}
}

// The model is never offered tiles, and a document it returns with tiles is
// prepared without them.
func TestGenerationOffersAndKeepsNoTiles(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	details, err := validator.DetailsSchema()
	if err != nil {
		t.Fatal(err)
	}
	for name, schema := range map[string][]byte{"model": validator.Schema, "details": details, "contract": encodeJSON(t, validator.Contract())} {
		if bytes.Contains(schema, []byte(`"tiles"`)) {
			t.Errorf("the %s schema offers tiles", name)
		}
	}
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := firstSection(doc)
	section["tiles"] = storedTiles()
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: "code", Change: "added"}}, Sources: bundlepkg.Sources{"diff": true}}
	prepared, findings := validator.Prepare(encodeJSON(t, doc), bundle, t.TempDir())
	if findings != "" {
		t.Fatal(findings)
	}
	if bytes.Contains(prepared, []byte(`"tiles"`)) {
		t.Fatalf("the prepared document keeps tiles:\n%s", prepared)
	}
}

// Only a section's and a risk's tiles are dropped, whatever their value: a
// tiles key elsewhere is the document's own, and a document with none comes
// back byte for byte.
func TestDropStoredTilesShapes(t *testing.T) {
	for _, c := range []struct {
		name, stored, want string
	}{
		{"null tiles", `{"sections":[{"id":"sec_a","tiles":null,"risks":[{"id":"rsk_a","tiles":null}]}]}`, `{"sections":[{"id":"sec_a","risks":[{"id":"rsk_a"}]}]}`},
		{"tiles that are not a list", `{"sections":[{"id":"sec_a","tiles":{"kind":"bug-fix"},"risks":[{"id":"rsk_a","tiles":"x"}]}]}`, `{"sections":[{"id":"sec_a","risks":[{"id":"rsk_a"}]}]}`},
		{"a risk of a stale section", `{"sections":[{"id":"sec_a","derived":{"state":"stale"},"risks":[{"id":"rsk_a","state":"active","tiles":[{"kind":"refactor"}]}]}]}`, `{"sections":[{"id":"sec_a","derived":{"state":"stale"},"risks":[{"id":"rsk_a","state":"active"}]}]}`},
		{"tiles in a block or a table", `{"sections":[{"id":"sec_a","blocks":[{"kind":"code","tiles":[1]},{"kind":"table","columns":["tiles"],"rows":[["a"]],"data":{"tiles":[2]}}],"tiles":[]}]}`, `{"sections":[{"id":"sec_a","blocks":[{"kind":"code","tiles":[1]},{"kind":"table","columns":["tiles"],"rows":[["a"]],"data":{"tiles":[2]}}]}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := DropStoredTiles([]byte(c.stored))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodedJSON(t, got), decodedJSON(t, []byte(c.want))) {
				t.Fatalf("dropped %s, want %s", got, c.want)
			}
		})
	}
	for name, document := range map[string]string{
		"no sections":           `{"schema":"baum.review/1", "summary": {"text": "x"}}`,
		"the word in prose":     `{"sections": [{"id": "sec_a", "summary": {"text": "Adds \"tiles\" to the page."}}]}`,
		"the word as a value":   `{"sections": [{"id": "sec_a", "title": "tiles"}]}`,
		"a block's own tiles":   `{"sections": [{"id": "sec_a", "blocks": [{"kind": "code", "tiles": []}]}]}`,
		"sections that are odd": `{"sections": "tiles"}`,
	} {
		if got, err := DropStoredTiles([]byte(document)); err != nil || string(got) != document {
			t.Errorf("%s: got %s (%v), want the bytes unchanged", name, got, err)
		}
	}
}
