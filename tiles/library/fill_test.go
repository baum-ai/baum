package library_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// diffOf returns a sample's hunks as a bundle's diff gives them.
func diffOf(s library.Sample) []bundle.Hunk {
	var diff []bundle.Hunk
	for id, h := range s.Hunks {
		diff = append(diff, bundle.Hunk{ID: id, Path: h.Path, Change: h.Change, StartLine: h.Range[0], LineCount: h.Range[1] - h.Range[0] + 1})
	}
	return diff
}

// The fill schema is the data schema without what the host writes, and
// stands on its own: a JSON Schema implementation that knows nothing of
// tiles resolves it and accepts every sample a fill could write.
func TestFillSchemaStandsAlone(t *testing.T) {
	for _, entry := range library.All() {
		raw, err := entry.FillSchema()
		if err != nil {
			t.Fatalf("%s: %v", entry.Definition.Name, err)
		}
		if strings.Contains(string(raw), `"`+library.Related+`"`) || strings.Contains(string(raw), "itemRef") {
			t.Errorf("%s: the fill schema names what the host writes", entry.Definition.Name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: %v", entry.Definition.Name, err)
		}
		for _, s := range entry.Samples {
			var data map[string]any
			if err := json.Unmarshal(s.Data, &data); err != nil {
				t.Fatal(err)
			}
			delete(data, library.Related)
			if err := resolved.Validate(data); err != nil {
				t.Errorf("%s %s: %v", entry.Definition.Name, s.Name, err)
			}
		}
	}
}

func TestCheckFill(t *testing.T) {
	v := tiles.NewValidator()
	entry, ok := library.Get("schema_change")
	if !ok {
		t.Fatal("no schema_change")
	}
	var s library.Sample
	for _, candidate := range entry.Samples {
		if candidate.Name == "sqlite-store" {
			s = candidate
		}
	}
	diff := diffOf(s)
	if problems := library.CheckFill(v, entry, s.Data, diff); len(problems) > 0 {
		t.Fatalf("the sample fails: %v", problems)
	}
	change := func(edit func(map[string]any)) json.RawMessage {
		var data map[string]any
		_ = json.Unmarshal(s.Data, &data)
		edit(data)
		out, _ := json.Marshal(data)
		return out
	}
	cases := map[string]struct {
		data json.RawMessage
		want string
	}{
		"related": {change(func(d map[string]any) {
			d[library.Related] = []any{map[string]any{"type": "risk", "id": "rsk_x"}}
		}), "written by the host"},
		"unknown hunk": {change(func(d map[string]any) {
			table(d, 0)["cites"] = []any{map[string]any{"type": "hunk", "id": "h_000000000000"}}
		}), "/tables/0/cites/0 names a hunk that is not in the diff"},
		"lines outside": {change(func(d map[string]any) {
			table(d, 0)["cites"] = []any{map[string]any{"type": "hunk", "id": "h_5a0c31e7b2d4", "lines": []any{30, 44}}}
		}), "outside the hunk, whose new lines are 36 to 62"},
		"reversed lines": {change(func(d map[string]any) {
			table(d, 0)["cites"] = []any{map[string]any{"type": "hunk", "id": "h_5a0c31e7b2d4", "lines": []any{50, 44}}}
		}), "end before they start"},
		"schema": {change(func(d map[string]any) { d["engine"] = "oracle" }), "does not match its schema"},
	}
	for name, tc := range cases {
		problems := library.CheckFill(v, entry, tc.data, diff)
		if !strings.Contains(strings.Join(problems, "; "), tc.want) {
			t.Errorf("%s: problems %v, want %q", name, problems, tc.want)
		}
	}
	// A hunk with no new lines is cited without lines.
	removed := []bundle.Hunk{{ID: "h_5a0c31e7b2d4", StartLine: 36}}
	if problems := library.CheckFill(v, entry, s.Data, removed); !strings.Contains(strings.Join(problems, "; "), "adds no lines") {
		t.Errorf("lines in a hunk without new lines: %v", problems)
	}
}

// HunkFromPatch numbers a patch's lines as the diff does.
func TestHunkFromPatch(t *testing.T) {
	h := library.HunkFromPatch("a.go", "modified", 10, 4, 2, 1, "@@ -9,3 +10,4 @@ func a() {\n x\n-y\n+z\n+w\n v\n")
	want := []library.Line{
		{Kind: "context", Old: 9, New: 10, Text: "x"},
		{Kind: "remove", Old: 10, Text: "y"},
		{Kind: "add", New: 11, Text: "z"},
		{Kind: "add", New: 12, Text: "w"},
		{Kind: "context", Old: 11, New: 13, Text: "v"},
	}
	if h.Range != [2]int{10, 13} || h.Header != "@@ -9,3 +10,4 @@ func a() {" || len(h.Lines) != len(want) {
		t.Fatalf("got %+v", h)
	}
	for i := range want {
		if h.Lines[i] != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, h.Lines[i], want[i])
		}
	}
}

// A fill that finds nothing the type is for answers that it does not apply
// rather than writing a tile that says so: data with every AppliesWith array
// empty is refused, such as a schema change tile for a pull request that
// only edits queries.
func TestCheckFillRefusesATileOfNothing(t *testing.T) {
	v := tiles.NewValidator()
	raw, err := os.ReadFile("testdata/schema_change-queries-only.json")
	if err != nil {
		t.Fatal(err)
	}
	var queries library.Sample
	if err := json.Unmarshal(raw, &queries); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		typ  string
		data json.RawMessage
		diff []bundle.Hunk
	}{
		{"schema_change", queries.Data, diffOf(queries)},
		{"schema_change", json.RawMessage(`{"engine":"unknown","summary":"No change.","migrations":[],"tables":[],"data_changes":[],"readers":[]}`), nil},
		{"api_change", json.RawMessage(`{"summary":"No change.","endpoints":[],"consumers":[],"docs":null}`), nil},
	}
	for _, tc := range cases {
		entry, _ := library.Get(tc.typ)
		if v.CheckData(entry.Encoded, tc.data) != nil {
			t.Fatalf("%s: the data itself is invalid", tc.typ)
		}
		problems := library.CheckFill(v, entry, tc.data, tc.diff)
		if !strings.Contains(strings.Join(problems, "; "), "does not apply") {
			t.Errorf("%s %s: problems %v, want that the tile does not apply", tc.typ, tc.data, problems)
		}
	}
}
