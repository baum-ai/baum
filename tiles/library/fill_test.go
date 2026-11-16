package library_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
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
		{"schema_change", json.RawMessage(`{"engine":"unknown","migrations":[],"tables":[],"data_changes":[],"readers":[]}`), nil},
		{"api_change", json.RawMessage(`{"endpoints":[],"consumers":[],"docs":null}`), nil},
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

// citingEntry is the kit's starter with items that cite anything the
// review holds (#/$defs/citation), as a saved type may.
func citingEntry(t *testing.T) library.Entry {
	t.Helper()
	entry, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	def := entry.Definition
	def.Name = "cites_anything"
	var schema map[string]any
	if err := json.Unmarshal(def.DataSchema, &schema); err != nil {
		t.Fatal(err)
	}
	item := schema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	item["properties"].(map[string]any)["cites"].(map[string]any)["items"] = map[string]any{"$ref": "#/$defs/citation"}
	if def.DataSchema, err = json.Marshal(schema); err != nil {
		t.Fatal(err)
	}
	if entry.Encoded, err = library.Seal(&def); err != nil {
		t.Fatal(err)
	}
	entry.Definition = def
	return entry
}

// The bundle pass checks the citations a saved type may make besides hunks:
// a file must be a path of the diff, a prompt or session event a session of
// the bundle that is not restricted, and a check or review thread one of
// the bundle's. Without the bundle (CheckFill), only files are checked.
func TestCheckFillInChecksTheBundle(t *testing.T) {
	v := tiles.NewValidator()
	entry := citingEntry(t)
	b := &bundle.Bundle{
		Diff:     []bundle.Hunk{{ID: "h_3e8a1c0d5b72", Path: "internal/worker/retry.go", OldPath: "internal/retry.go", StartLine: 10, LineCount: 5}},
		Sessions: []bundle.Session{{RootSessionID: "ses_open", Vendor: "claude"}, {RootSessionID: "ses_closed", Vendor: "claude", Restricted: true}},
		Checks:   []bundle.Check{{ID: "chk_unit", Name: "unit"}},
		Threads:  []bundle.Thread{{ID: "thr_1", Path: "internal/worker/retry.go"}},
	}
	data := func(cite string) json.RawMessage {
		return json.RawMessage(`{"summary":"One item.","items":[{"title":"An item","severity":"low","text":null,"cites":[` + cite + `]}]}`)
	}
	fine := []string{
		`{"type":"hunk","id":"h_3e8a1c0d5b72","lines":[10,12]}`,
		`{"type":"file","path":"internal/worker/retry.go"}`,
		`{"type":"file","path":"internal/retry.go"}`,
		`{"type":"prompt","session":"ses_open","index":1}`,
		`{"type":"session_event","session":"ses_open","event":"evt_1"}`,
		`{"type":"check","id":"chk_unit"}`,
		`{"type":"review_thread","id":"thr_1"}`,
	}
	for _, cite := range fine {
		if problems := library.CheckFillIn(v, entry, data(cite), b); len(problems) > 0 {
			t.Errorf("%s: %v", cite, problems)
		}
	}
	refused := map[string]string{
		`{"type":"file","path":"README.md"}`:                          "names a file the diff does not change",
		`{"type":"prompt","session":"ses_closed","index":1}`:          "names a restricted session",
		`{"type":"session_event","session":"ses_closed","event":"e"}`: "names a restricted session",
		`{"type":"prompt","session":"ses_elsewhere","index":1}`:       "names a session the pull request does not have",
		`{"type":"check","id":"chk_other"}`:                           "names a check the pull request does not have",
		`{"type":"review_thread","id":"thr_2"}`:                       "names a review thread the pull request does not have",
		`{"type":"risk","id":"rsk_retry_storm"}`:                      "which a fill cannot",
		`{"type":"evidence","id":"ev_retry_test"}`:                    "which a fill cannot",
		`{"type":"tile","id":"overview"}`:                             "which a fill cannot",
	}
	for cite, want := range refused {
		problems := library.CheckFillIn(v, entry, data(cite), b)
		if !strings.Contains(strings.Join(problems, "; "), "/items/0/cites/0 "+want) && !strings.Contains(strings.Join(problems, "; "), want) {
			t.Errorf("%s: problems %v, want %q", cite, problems, want)
		}
		if added := library.CheckAddedFillIn(v, entry, data(cite), b); len(added) == 0 {
			t.Errorf("%s: an added fill passes", cite)
		}
	}
	// Without the bundle, a restricted session cannot be told apart; a file
	// still can.
	if problems := library.CheckFill(v, entry, data(`{"type":"prompt","session":"ses_closed","index":1}`), b.Diff); len(problems) > 0 {
		t.Errorf("CheckFill without the bundle: %v", problems)
	}
	if problems := library.CheckFill(v, entry, data(`{"type":"file","path":"README.md"}`), b.Diff); len(problems) == 0 {
		t.Error("CheckFill took a file outside the diff")
	}
}

// The host writes related items only into a type whose schema declares
// them: the built-in types do, the kit's starter (and a saved type made
// from it) leaves the name unused.
func TestDeclaresRelated(t *testing.T) {
	for _, e := range library.All() {
		if !e.DeclaresRelated() {
			t.Errorf("%s does not declare related", e.Definition.Name)
		}
	}
	if citingEntry(t).DeclaresRelated() {
		t.Error("the starter declares related")
	}
}
