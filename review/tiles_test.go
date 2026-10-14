package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review/tilecatalogue"
)

func tileFixture() map[string]any {
	return map[string]any{"id": "tile_fix", "kind": "bug-fix", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}, "code": []any{}, "data": map[string]any{"bug": "Invalid input aborts parsing.", "fix": "Reject invalid input before parsing."}}
}

func tileJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	checkNoError(t, err)
	return data
}

func TestTileSchemaAndCatalogueAgree(t *testing.T) {
	var catalogue []struct {
		Kind string `json:"kind"`
	}
	checkNoError(t, json.Unmarshal([]byte(tilecatalogue.JSON), &catalogue))
	schema, err := decodeObject(reviewSchema, "schema")
	checkNoError(t, err)
	choices := schema["$defs"].(map[string]any)["tile"].(map[string]any)["oneOf"].([]any)
	kinds := map[string]bool{}
	for _, choice := range choices {
		kinds[choice.(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["const"].(string)] = true
	}
	for _, item := range catalogue {
		if !kinds[item.Kind] {
			t.Fatalf("unknown or duplicate catalogue kind %s", item.Kind)
		}
		delete(kinds, item.Kind)
	}
	if len(kinds) != 0 {
		t.Fatalf("kinds missing from catalogue: %v", kinds)
	}
}

func TestTileValidationRejectsMalformedAndDuplicateTiles(t *testing.T) {
	validator, err := sharedValidator()
	checkNoError(t, err)
	_, bundle, _ := refreshFixture(t)
	for _, name := range []string{"valid", "repeated kind", "duplicate id", "missing data", "unknown kind", "removed kind", "architecture", "architecture context", "restricted source"} {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeObject(authoredFixture(), "candidate")
			checkNoError(t, err)
			section := doc["sections"].([]any)[0].(map[string]any)
			tile := tileFixture()
			section["tiles"] = []any{tile}
			switch name {
			case "repeated kind":
				other := tileFixture()
				other["id"] = "tile_second"
				section["tiles"] = []any{tile, other}
			case "duplicate id":
				section["tiles"] = []any{tile, tile}
			case "missing data":
				delete(tile, "data")
			case "unknown kind":
				tile["kind"] = "ui-preview"
			case "removed kind":
				tile["kind"] = "refactor"
				tile["data"] = map[string]any{"was": "One parser.", "now": "Two parsers."}
			case "architecture", "architecture context":
				node := map[string]any{"id": "api", "label": "API", "kind": "service"}
				data := map[string]any{"product": "baum", "containers": []any{node}, "edges": []any{}, "boundary": "API only.", "runtime": "Unchanged.", "failure_path": "Unchanged."}
				if name == "architecture context" {
					data["context"] = []any{map[string]any{"id": "user", "label": "User", "kind": "person"}}
				}
				tile["kind"], tile["data"] = "architecture", data
			case "restricted source":
				tile["anchors"] = []any{map[string]any{"type": "prompt", "session": "secret", "index": 1}}
				bundle.Sessions = append(bundle.Sessions, bundlepkg.Session{RootSessionID: "secret", Restricted: true})
			}
			prepared, findings := validator.Prepare(tileJSON(t, doc), bundle, "unused")
			// A repeated tile ID is renamed and a retired architecture field
			// dropped by normalization rather than rejected.
			valid := name == "valid" || name == "repeated kind" || name == "duplicate id" || strings.HasPrefix(name, "architecture")
			if (findings == "") != valid {
				t.Fatalf("findings=%q", findings)
			}
			if name == "duplicate id" && !strings.Contains(string(prepared), `"id":"tile_fix_2"`) {
				t.Fatalf("duplicate tile was not renamed: %s", prepared)
			}
			if name == "architecture context" && strings.Contains(string(prepared), `"context":[`) {
				t.Fatalf("retired architecture context was kept: %s", prepared)
			}
		})
	}
}

func TestTileSourcesVerifyPinnedComparisonAndCarryOlderExcerpts(t *testing.T) {
	directory := t.TempDir()
	for _, root := range []string{"head", "merge-base"} {
		checkNoError(t, os.Mkdir(filepath.Join(directory, root), 0700))
		checkNoError(t, os.WriteFile(filepath.Join(directory, root, "main.go"), []byte("source"), 0600))
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: head, MergeBaseSHA: base}, Diff: []bundlepkg.Hunk{{Path: "main.go", Patch: "@@ -1 +1 @@\n-before\n+after\n"}}}
	var original tileCode
	checkNoError(t, json.Unmarshal([]byte(`{"path":"main.go","revision":"`+head+`","review_version":1,"diff":{"hunks":[{"header":"@@ -1 +1 @@","lines":[{"kind":"remove","old_line":1,"text":"before"},{"kind":"add","new_line":1,"text":"after"}]}]}}`), &original))
	for _, name := range []string{"valid", "deleted file", "wrong revision", "future version", "unsafe path", "wrong text", "wrong line", "wrong header", "inherited", "tampered inherited", "unverified older"} {
		t.Run(name, func(t *testing.T) {
			var source tileCode
			checkNoError(t, json.Unmarshal(tileJSON(t, original), &source))
			version := 1
			var inherited []tileCode
			switch name {
			case "deleted file":
				source.Revision = base
			case "wrong revision":
				source.Revision = strings.Repeat("c", 40)
			case "future version":
				source.Version = 2
			case "unsafe path":
				source.Path = "../main.go"
			case "wrong text":
				source.Diff.Hunks[0].Lines[1].Text = "invented"
			case "wrong line":
				source.Diff.Hunks[0].Lines[1].New = 2
			case "wrong header":
				source.Diff.Hunks[0].Header = "@@ -1,3 +1,3 @@"
			case "inherited":
				version = 2
				inherited = []tileCode{original}
			case "tampered inherited":
				version = 2
				inherited = []tileCode{original}
				source.Diff.Hunks[0].Lines[1].Text = "invented"
			case "unverified older":
				version = 2
			}
			result := validateTileSource(source, bundle, directory, version, inherited)
			valid := name == "valid" || name == "deleted file" || name == "inherited"
			if (result == "") != valid {
				t.Fatalf("result=%q", result)
			}
		})
	}
}

func TestTileSemanticReferences(t *testing.T) {
	cases := []struct{ kind, data string }{
		{"access-model", `{"actors":[{"id":"reader"}],"capabilities":[{"id":"read","allowed":["missing"]}]}`},
		{"access-model", `{"actors":[{"id":"reader"}],"capabilities":[{"id":"read","allowed":["reader"],"revoked":["reader"]}]}`},
		{"architecture", `{"containers":[{"id":"a"}],"edges":[{"from":"a","to":"missing"}]}`},
		{"architecture", `{"containers":[{"id":"a"}],"edges":[],"sequences":[{"steps":[{"from":"a","to":"user"}]}]}`},
		{"database-schema", `{"tables":[{"name":"a","columns":[{"name":"id"}]}],"relationships":[{"from":"a.id","to":"b.id"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			var data map[string]any
			checkNoError(t, json.Unmarshal([]byte(tc.data), &data))
			if validTileReferences(reviewTile{Kind: tc.kind, Data: data}) {
				t.Fatal("invalid cross-reference accepted")
			}
		})
	}
}
