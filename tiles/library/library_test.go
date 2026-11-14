package library_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// digests pins each built-in type's definition. A saved review embeds the
// definition it was made with, and a generation reuses a fill only for the
// same digest, so an edit must be deliberate: update the digest in the same
// commit and say in its message what changed.
var digests = map[string]string{
	"schema_change": "sha256:e67520c2cf53f819dc3a18d311d5a545159d83d73b2e0f8394f8b5b30e9e8ab1",
	"api_change":    "sha256:15a6db0250bf83fde84fcf86e71c2dfdd2d11dbcd7bcbd3c84b1fe0c52e8b592",
}

// A built-in definition is recognised by its content's digest, current or
// past; one that claims a built-in digest but differs is not.
func TestIsBuiltin(t *testing.T) {
	for _, entry := range library.All() {
		if !library.IsBuiltin(entry.Encoded) {
			t.Errorf("%s is not built in", entry.Definition.Name)
		}
		var def map[string]any
		_ = json.Unmarshal(entry.Encoded, &def)
		def["title"] = "Something else"
		changed, _ := json.Marshal(def)
		if library.IsBuiltin(changed) {
			t.Errorf("%s with another title, under its digest, is built in", entry.Definition.Name)
		}
		if digests[entry.Definition.Name] != entry.Definition.Digest || slices.Contains(library.PastDigests[entry.Definition.Name], entry.Definition.Digest) {
			t.Errorf("%s: the current digest is not pinned, or listed as past", entry.Definition.Name)
		}
	}
}

func TestBuiltinTypesPassEveryCheck(t *testing.T) {
	entries := library.All()
	if len(entries) != len(library.Names) {
		t.Fatalf("All returns %d types, want %d", len(entries), len(library.Names))
	}
	for i, entry := range entries {
		def := entry.Definition
		if def.Name != library.Names[i] {
			t.Errorf("type %d is %q, want %q", i, def.Name, library.Names[i])
		}
		for _, problem := range library.Check(entry) {
			t.Errorf("%s: %s", def.Name, problem)
		}
		if got, ok := library.Get(def.Name); !ok || got.Definition.Digest != def.Digest {
			t.Errorf("Get(%q) does not return the type", def.Name)
		}
		if def.Digest != digests[def.Name] {
			t.Errorf("%s's definition changed: its digest is %s; if the change is deliberate, pin it in digests and append the old digest, %s, to library.PastDigests so saved tiles of it stay baum's own", def.Name, def.Digest, digests[def.Name])
		}
		if _, builtin := tiles.Builtin(def.Name); builtin || def.Name == tiles.TypeCustom {
			t.Errorf("%s clashes with a built-in type", def.Name)
		}
		if tiles.LibraryTileID(def.Name) != def.Name {
			t.Errorf("%s's tile ID is %s, not its name", def.Name, tiles.LibraryTileID(def.Name))
		}
		// The texts a generation reads, within the limits the format and the
		// pipeline set.
		if n := utf8.RuneCountInString(def.Fill); n < 1000 || n > 8000 {
			t.Errorf("%s's fill is %d characters", def.Name, n)
		}
		if n := utf8.RuneCountInString(def.UseWhen); n > 200 || !strings.HasPrefix(def.UseWhen, "When ") || !strings.Contains(def.UseWhen, "Not ") {
			t.Errorf("%s's use_when must be one line of at most 200 characters naming its trigger (When …) and an exclusion (Not …)", def.Name)
		}
		if entry.ReviewChecks == "" {
			t.Errorf("%s has no review checks", def.Name)
		}
		if len(entry.Samples) < 4 {
			t.Errorf("%s has %d samples; keep the common case, the edges and the empty case", def.Name, len(entry.Samples))
		}
	}
}

// The definitions a review embeds compose into frame documents, which the
// registry keeps under their digests.
func TestFramesComposeAndRegister(t *testing.T) {
	registry := tileframes.New(tileframes.MaxBytes)
	for _, entry := range library.All() {
		digest, err := registry.Add(entry.Definition)
		if err != nil {
			t.Fatalf("%s: %v", entry.Definition.Name, err)
		}
		document, ok := registry.Get(digest)
		if !ok || !strings.Contains(string(document), entry.Definition.Source.HTML) {
			t.Errorf("%s: the registry does not serve its document", entry.Definition.Name)
		}
		if len(document) > kit.MaxDocument {
			t.Errorf("%s: the frame document is %d bytes", entry.Definition.Name, len(document))
		}
	}
}

// A library tile with sample data, cited to a synthetic review's hunks, is
// ready in a composed document and covers the hunks it cites.
func TestLibraryTilesAreReadyInADocument(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	normalized, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "testdata", "base.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hunks := tilestest.Hunks(doc)
	var libraryTiles []tiles.LibraryTile
	for _, entry := range library.All() {
		for _, sample := range entry.Samples {
			if sample.Name == "nothing" {
				continue
			}
			libraryTiles = append(libraryTiles, tiles.LibraryTile{Definition: entry.Encoded, Data: retarget(t, sample.Data, hunks)})
			break
		}
	}
	n, report := tiles.Template(doc, &tiles.Composition{Library: libraryTiles})
	if len(report.Fixes) > 0 {
		t.Fatalf("fixes: %v", report.Messages())
	}
	composed, err := tiles.Compose(doc, &n)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range library.Names {
		i := slices.IndexFunc(composed.Tiles, func(tile tiles.Tile) bool { return tile.ID == name })
		if i < 0 {
			t.Fatalf("the document has no %s tile", name)
		}
		tile := composed.Tiles[i]
		if tile.State != tiles.StateReady || len(tile.Problems) > 0 || len(tile.Covers) == 0 {
			t.Errorf("%s: state %s, problems %v, covers %v", name, tile.State, tile.Problems, tile.Covers)
		}
	}
}

// retarget points sample data's hunk citations at a document's hunks, in
// turn, without lines, and drops what the host writes.
func retarget(t *testing.T, data json.RawMessage, hunks []string) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	delete(value.(map[string]any), library.Related)
	next := 0
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if v["type"] == "hunk" {
				v["id"] = hunks[next%len(hunks)]
				delete(v, "lines")
				next++
				return
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sample returns a built-in type's sample, decoded so a test can change it.
func sample(t *testing.T, name, sampleName string) (library.Entry, map[string]any) {
	t.Helper()
	entry, ok := library.Get(name)
	if !ok {
		t.Fatalf("no type %s", name)
	}
	for _, s := range entry.Samples {
		if s.Name == sampleName {
			var data map[string]any
			if err := json.Unmarshal(s.Data, &data); err != nil {
				t.Fatal(err)
			}
			return entry, data
		}
	}
	t.Fatalf("%s has no sample %s", name, sampleName)
	return entry, nil
}

// The data schemas refuse the shapes a fill gets wrong most: a fact without
// a citation, a value outside its enum, a property the schema lacks, a
// missing property, an over-long text and a citation that is not one.
func TestDataSchemasRejectWrongShapes(t *testing.T) {
	v := tiles.NewValidator()
	cases := []struct {
		name, typ, sampleName, want string
		change                      func(map[string]any)
	}{
		{"table without citations", "schema_change", "sqlite-store", "cites", func(d map[string]any) {
			table(d, 0)["cites"] = []any{}
		}},
		{"unknown change kind", "schema_change", "sqlite-store", "change", func(d map[string]any) {
			table(d, 1)["change"] = "modified"
		}},
		{"a concern written by the fill", "schema_change", "sqlite-store", "concerns", func(d map[string]any) {
			d["concerns"] = []any{map[string]any{"severity": "high", "text": "Drops labels."}}
		}},
		{"column without destructive", "schema_change", "sqlite-store", "destructive", func(d map[string]any) {
			delete(table(d, 1)["columns"].([]any)[0].(map[string]any), "destructive")
		}},
		{"a file citation where a hunk goes", "schema_change", "sqlite-store", "cites", func(d map[string]any) {
			table(d, 0)["cites"] = []any{map[string]any{"type": "file", "path": "internal/widgets/store.go"}}
		}},
		{"a hunk ID that is not one", "schema_change", "sqlite-store", "id", func(d map[string]any) {
			table(d, 0)["cites"] = []any{map[string]any{"type": "hunk", "id": "store.go:44"}}
		}},
		{"a summary, which the tile computes", "schema_change", "sqlite-store", "summary", func(d map[string]any) {
			d["summary"] = "Adds a table."
		}},
		{"endpoint without breaking", "api_change", "http-routes", "breaking", func(d map[string]any) {
			delete(d["endpoints"].([]any)[0].(map[string]any), "breaking")
		}},
		{"a method on a tool", "api_change", "mixed-surfaces", "method", func(d map[string]any) {
			d["endpoints"].([]any)[3].(map[string]any)["method"] = "CALL"
		}},
		{"lines that are not a pair", "api_change", "http-routes", "lines", func(d map[string]any) {
			d["endpoints"].([]any)[1].(map[string]any)["cites"] = []any{map[string]any{"type": "hunk", "id": "h_9e5a3172d4fc", "lines": []any{42}}}
		}},
	}
	for _, tc := range cases {
		entry, data := sample(t, tc.typ, tc.sampleName)
		tc.change(data)
		encoded, _ := json.Marshal(data)
		problems := v.CheckData(entry.Encoded, encoded)
		if len(problems) == 0 {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(problems, "; "), tc.want) {
			t.Errorf("%s: the problems %v do not name %q", tc.name, problems, tc.want)
		}
	}
}

func table(data map[string]any, i int) map[string]any {
	return data["tables"].([]any)[i].(map[string]any)
}

// The kit's starter, which a new type starts from, passes every check a
// library type does.
func TestStarterPassesEveryCheck(t *testing.T) {
	entry, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range library.Check(entry) {
		t.Error(problem)
	}
}
