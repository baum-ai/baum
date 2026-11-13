package tiles_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// routesDefinition is a synthetic custom tile type: a summary, routes that
// cite the code, and related items of the review.
func routesDefinition() tiles.TypeDefinition {
	return tiles.TypeDefinition{
		Name:    "route_changes",
		Title:   "Route changes",
		UseWhen: "When the pull request adds, removes or changes an HTTP route.",
		Fill:    "List each changed route. Cite the hunk that registers it.",
		DataSchema: json.RawMessage(`{
			"type": "object", "additionalProperties": false, "required": ["summary", "routes"],
			"properties": {
				"summary": {"$ref": "#/$defs/markdown"},
				"routes": {"type": "array", "maxItems": 20, "items": {
					"type": "object", "additionalProperties": false, "required": ["method", "path", "cites"],
					"properties": {
						"method": {"enum": ["GET", "POST", "PUT", "DELETE"]},
						"path": {"type": "string", "maxLength": 200},
						"tier": {"anyOf": [{"$ref": "#/$defs/tier"}, {"type": "null"}]},
						"cites": {"type": "array", "maxItems": 300, "items": {"$ref": "#/$defs/citation"}}
					}
				}},
				"related": {"type": "array", "maxItems": 20, "items": {"$ref": "#/$defs/citation"}}
			}
		}`),
		Kit:    1,
		Sizes:  []tiles.Size{tiles.SizeHalf, tiles.SizeFull},
		Source: tiles.TypeSource{Format: "html", HTML: "<script>baum.tile({glance: (v) => baum.html`<p>${v.data.summary}</p>`, detail: (v) => baum.md(v.data.summary)});</script>"},
	}
}

const routesData = `{
	"summary": "Adds **one** route.",
	"routes": [{
		"method": "POST", "path": "/api/retries", "tier": "high",
		"cites": [
			{"type": "hunk", "id": "h_000000000003", "lines": [4, 9]},
			{"type": "hunk", "id": "h_000000000001"},
			{"type": "hunk", "id": "h_000000000003"},
			{"type": "file", "path": "internal/retry/policy.go"},
			{"type": "commit", "sha": "a1b2c3d"},
			{"type": "check", "id": "chk_unit"},
			{"type": "review_thread", "id": "thr_1"},
			{"type": "prompt", "session": "ses_open", "index": 1},
			{"type": "external_url", "url": "https://example.com/spec"}
		]
	}],
	"related": [
		{"type": "risk", "id": "rsk_retry_storm"},
		{"type": "evidence", "id": "ev_retry_test"},
		{"type": "change", "id": "sec_retry_policy"},
		{"type": "tile", "id": "overview"}
	]
}`

// customDocument is the base document with an index that has a check, a
// review thread and two sessions, one restricted, and a ready custom tile.
func customDocument(t *testing.T) *tiles.Document {
	t.Helper()
	doc := validDocument(t)
	var index map[string]any
	if err := json.Unmarshal(doc.Index, &index); err != nil {
		t.Fatal(err)
	}
	index["checks"] = []any{map[string]any{"id": "chk_unit", "kind": "check_run", "name": "unit", "conclusion": "success", "required": true, "head": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"}}
	index["threads"] = []any{map[string]any{"id": "thr_1", "path": "internal/retry/policy.go", "line": 4, "resolved": false}}
	index["sessions"] = []any{
		map[string]any{"id": "ses_open", "vendor": "claude-code", "restricted": false},
		map[string]any{"id": "ses_private", "vendor": "claude-code", "restricted": true},
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	doc.Index = encoded
	addTile(doc, tilestest.CustomTile("route_changes", "Route changes", routesDefinition(), json.RawMessage(routesData)))
	return doc
}

func TestCustomTileReady(t *testing.T) {
	doc, err := validate(t, customDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	tile := tileOf(doc, "route_changes")
	if tile.State != tiles.StateReady || len(tile.Problems) > 0 {
		t.Fatalf("custom tile is %s: %v", tile.State, tile.Problems)
	}
	// Covers are the cited hunks, sorted and without repeats.
	if want := []string{"h_000000000001", "h_000000000003"}; !slices.Equal(tile.Covers, want) {
		t.Errorf("covers = %v, want %v", tile.Covers, want)
	}
	for _, other := range doc.Tiles {
		if other.State == tiles.StateUnavailable {
			t.Errorf("tile %s is unavailable: %v", other.ID, other.Problems)
		}
	}
}

// secret is a value in tile data that no problem may repeat.
const secret = "SECRET-7f3a-do-not-echo"

// Each cause marks the custom tile, and only it, unavailable with a problem
// that names the cause and never quotes the data.
func TestCustomTileUnavailable(t *testing.T) {
	definition := func(mutate func(def *tiles.TypeDefinition)) json.RawMessage {
		def := routesDefinition()
		mutate(&def)
		return tilestest.SealDefinition(def)
	}
	rawDefinition := func(mutate func(object map[string]any)) json.RawMessage {
		var object map[string]any
		if err := json.Unmarshal(tilestest.SealDefinition(routesDefinition()), &object); err != nil {
			t.Fatal(err)
		}
		mutate(object)
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	withData := func(data string) func(tile *tiles.Tile) {
		return func(tile *tiles.Tile) { tile.Data = json.RawMessage(data) }
	}
	cite := func(citation string) func(tile *tiles.Tile) {
		return withData(`{"summary": "` + secret + `", "routes": [], "related": [` + citation + `]}`)
	}
	var many []string
	for range 257 {
		many = append(many, `{"type":"hunk","id":"h_000000000001"}`)
	}
	for _, tc := range []struct {
		name   string
		want   string
		mutate func(tile *tiles.Tile)
	}{
		// 1. The definition's shape and name.
		{"a definition without its kit", `the definition is not valid: at the top level: lacks the required property "kit"`, func(tile *tiles.Tile) {
			tile.Definition = rawDefinition(func(object map[string]any) { delete(object, "kit") })
		}},
		{"a fill over 8000 characters", "the definition is not valid: at /fill: is 8001 characters long; its maxLength is 8000", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Fill = strings.Repeat("f", 8001) })
		}},
		{"a use-when line on two lines", "the definition is not valid: at /use_when: does not match its pattern", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.UseWhen = "When.\nOr when." })
		}},
		{"repeated sizes", "the definition is not valid: at /sizes: has equal items 0 and 1", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Sizes = []tiles.Size{tiles.SizeHalf, tiles.SizeHalf} })
		}},
		{"no sizes", "the definition is not valid: at /sizes: has 0 items; its minItems is 1", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Sizes = []tiles.Size{} })
		}},
		{"an unknown size", "the definition is not valid: at /sizes/0: is not one of the 3 values of its enum", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Sizes = []tiles.Size{"quarter"} })
		}},
		{"an unknown member", `the definition is not valid: at the top level: has the property "samples", which its schema does not define`, func(tile *tiles.Tile) {
			tile.Definition = rawDefinition(func(object map[string]any) { object["samples"] = []any{} })
		}},
		{"a definition that is not valid UTF-8", "the definition is not valid: not valid UTF-8", func(tile *tiles.Tile) {
			tile.Definition = json.RawMessage(strings.Replace(string(tilestest.SealDefinition(routesDefinition())), "Route changes", "Route \xff", 1))
		}},
		{"a built-in name", `the type's name is "risks", a built-in tile type's`, func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Name = tiles.TypeRisks })
		}},
		{"the name custom", `the type's name is "custom", which is reserved`, func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Name = tiles.TypeCustom })
		}},
		// 2. The digest.
		{"a definition changed after its digest", "the definition doesn't match its digest", func(tile *tiles.Tile) {
			tile.Definition = rawDefinition(func(object map[string]any) { object["title"] = "Route changes, edited" })
		}},
		{"a definition without its digest", `lacks the required property "digest"`, func(tile *tiles.Tile) {
			tile.Definition = rawDefinition(func(object map[string]any) { delete(object, "digest") })
		}},
		// 3. The kit.
		{"a newer kit", "this tile was made with a newer version of baum (tile kit 2; this version has up to 1)", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Kit = tiles.KitLatest + 1 })
		}},
		{"kit zero", "the definition is not valid: at /kit: is less than its minimum, 1", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Kit = 0 })
		}},
		// 4. The component.
		{"HTML with a NUL", "the definition's HTML contains a NUL character", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Source.HTML += "\x00" })
		}},
		{"HTML over 262144 characters", "at /source/html: is 262145 characters long; its maxLength is 262144", func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) { def.Source.HTML = strings.Repeat("h", 262145) })
		}},
		{"a definition over 2 MiB", "the definition is larger than 2 MiB", func(tile *tiles.Tile) {
			tile.Definition = rawDefinition(func(object map[string]any) { object["padding"] = strings.Repeat("p", 2<<20) })
		}},
		// 5. The data schema.
		{"a data schema outside the dialect", `data schema #/properties/note: a string needs "maxLength"`, func(tile *tiles.Tile) {
			tile.Definition = definition(func(def *tiles.TypeDefinition) {
				def.DataSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"note":{"type":"string"}}}`)
			})
		}},
		// 6. The data.
		{"data over 64 KiB", "the data is larger than 64 KiB", withData(`{"summary":"` + strings.Repeat("s", 64<<10) + `","routes":[]}`)},
		{"data without a required property", `the data does not match its schema: at the top level: lacks the required property "routes"`, withData(`{"summary":"` + secret + `"}`)},
		{"a string over its maxLength", "the data does not match its schema: at /summary: is 4024 characters long; its maxLength is 4000", withData(`{"summary":"` + secret + strings.Repeat("s", 4001) + `","routes":[]}`)},
		{"a value not in its enum", "at /routes/0/method: is not one of the 4 values of its enum", withData(`{"summary":"","routes":[{"method":"` + secret + `","path":"/","cites":[]}]}`)},
		{"a value of another type", "at /routes/0/path: is a number; the schema allows string", withData(`{"summary":"","routes":[{"method":"GET","path":1.5,"cites":[]}]}`)},
		{"an unknown property", `at /routes/0: has the property "secret", which its schema does not define`, withData(`{"summary":"","routes":[{"method":"GET","path":"/","cites":[],"secret":"` + secret + `"}]}`)},
		{"an unknown property named by the data", "at the top level: has a property, which its schema does not define", withData(`{"summary":"","routes":[],"` + secret + ` key":1}`)},
		{"a value in no option", "at /routes/0/tier: matches none of the 2 options of its anyOf; the closest is not one of the 5 values of its enum", withData(`{"summary":"","routes":[{"method":"GET","path":"/","tier":"` + secret + `","cites":[]}]}`)},
		{"a citation of another shape", `at /related/0/id: does not match its pattern "^h_[0-9a-f]{12}(-[0-9]+)?$" (in the closest option of the oneOf at /related/0)`, cite(`{"type":"hunk","id":"` + secret + `"}`)},
		// 7. The citations.
		{"more than 256 citations", "the data has more than 256 citations", withData(`{"summary":"","routes":[{"method":"GET","path":"/","cites":[` + strings.Join(many, ",") + `]}]}`)},
		{"an unknown hunk", "the citation at /related/0 names a hunk this review does not have", cite(`{"type":"hunk","id":"h_0000000000ff"}`)},
		{"lines that end before they start", "the citation at /related/0 has lines that end before they start", cite(`{"type":"hunk","id":"h_000000000001","lines":[9,4]}`)},
		{"an unknown check", "the citation at /related/0 names a check this review does not have", cite(`{"type":"check","id":"chk_gone"}`)},
		{"an unknown review thread", "the citation at /related/0 names a review thread this review does not have", cite(`{"type":"review_thread","id":"thr_gone"}`)},
		{"an unknown session", "the citation at /related/0 names a session this review does not have", cite(`{"type":"prompt","session":"ses_gone","index":1}`)},
		{"a restricted session", "the citation at /related/0 names a restricted session", cite(`{"type":"session_event","session":"ses_private","event":"evt_1"}`)},
		{"an unknown risk", "the citation at /related/0 names a risk this review does not have", cite(`{"type":"risk","id":"rsk_gone"}`)},
		{"an unknown evidence item", "the citation at /related/0 names an evidence item this review does not have", cite(`{"type":"evidence","id":"ev_gone"}`)},
		{"an unknown change", "the citation at /related/0 names a change this review does not have", cite(`{"type":"change","id":"sec_gone"}`)},
		{"an unknown tile", "the citation at /related/0 names a tile this review does not have", cite(`{"type":"tile","id":"gone"}`)},
		{"many unknown citations", "and 2 more citations do not resolve", cite(strings.Repeat(`{"type":"risk","id":"rsk_gone"},`, 6) + `{"type":"risk","id":"rsk_gone"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := customDocument(t)
			tc.mutate(tileOf(doc, "route_changes"))
			validated, err := validate(t, doc)
			if err != nil {
				t.Fatalf("a tile-level failure rejected the document: %v", err)
			}
			for _, tile := range validated.Tiles {
				if tile.ID != "route_changes" {
					if tile.State == tiles.StateUnavailable {
						t.Errorf("tile %s is unavailable too: %v", tile.ID, tile.Problems)
					}
					continue
				}
				problems := strings.Join(tile.Problems, "\n")
				if tile.State != tiles.StateUnavailable {
					t.Fatalf("custom tile is %s, want unavailable", tile.State)
				}
				if !strings.Contains(problems, tc.want) {
					t.Errorf("problems:\n%s\nwant one saying %q", problems, tc.want)
				}
				if strings.Contains(problems, secret) || strings.Contains(problems, "SECRET") {
					t.Errorf("problems quote the data:\n%s", problems)
				}
				if tile.Covers != nil {
					t.Errorf("an unavailable custom tile covers %v", tile.Covers)
				}
			}
		})
	}
}

// A draft's custom tile is pending, with empty data, until its data is
// written; its definition is still checked.
func TestCustomTilePending(t *testing.T) {
	doc := customDocument(t)
	tile := tileOf(doc, "route_changes")
	tile.State, tile.Data = tiles.StatePending, json.RawMessage(`{}`)
	validated, err := validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(validated, "route_changes"); tile.State != tiles.StatePending || tile.Covers != nil {
		t.Errorf("pending custom tile is %s, covers %v: %v", tile.State, tile.Covers, tile.Problems)
	}
	tile.Definition = json.RawMessage(`{}`)
	validated, err = validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(validated, "route_changes"); tile.State != tiles.StateUnavailable {
		t.Errorf("pending custom tile with a bad definition is %s", tile.State)
	}
}

// The document schema checks only that a custom tile has a definition
// object; its shape is a tile-level check.
func TestCustomTileDocumentRules(t *testing.T) {
	doc := customDocument(t)
	tileOf(doc, "route_changes").Definition = json.RawMessage(`"not an object"`)
	if _, err := validate(t, doc); err == nil {
		t.Error("a custom tile whose definition is not an object was accepted")
	}
	doc = customDocument(t)
	tileOf(doc, "route_changes").Definition = json.RawMessage(`{"name":"x"}`)
	validated, err := validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(validated, "route_changes"); tile.State != tiles.StateUnavailable {
		t.Errorf("custom tile with a partial definition is %s", tile.State)
	}
}

// CheckTiles, the serving path, checks custom tiles as Validate does, and
// sets their covers.
func TestCustomTileCheckTiles(t *testing.T) {
	doc := customDocument(t)
	tile := tileOf(doc, "route_changes")
	tile.Covers = []string{"h_000000000007"}
	if err := validator.CheckTiles(doc); err != nil {
		t.Fatal(err)
	}
	if tile.State != tiles.StateReady || !slices.Equal(tile.Covers, []string{"h_000000000001", "h_000000000003"}) {
		t.Errorf("custom tile is %s, covers %v: %v", tile.State, tile.Covers, tile.Problems)
	}
	problems, err := validator.TileProblems(doc, tile)
	if err != nil || problems != nil {
		t.Errorf("TileProblems = %v, %v", problems, err)
	}
}

func TestCheckDefinitionAndData(t *testing.T) {
	sealed := tilestest.SealDefinition(routesDefinition())
	def, problems := validator.CheckDefinition(sealed)
	if problems != nil || def.Name != "route_changes" || def.Kit != 1 || !strings.HasPrefix(def.Digest, "sha256:") {
		t.Fatalf("CheckDefinition = %+v, %v", def.Name, problems)
	}
	if digest, _ := tiles.DefinitionDigest(sealed); digest != def.Digest {
		t.Errorf("digest = %s, want %s", def.Digest, digest)
	}
	if problems := validator.CheckData(sealed, json.RawMessage(routesData)); problems != nil {
		t.Errorf("CheckData = %v", problems)
	}
	if problems := validator.CheckData(sealed, json.RawMessage(`{"summary":""}`)); len(problems) != 1 {
		t.Errorf("CheckData of data without routes = %v", problems)
	}
	if problems := validator.CheckData(sealed, json.RawMessage(`{"summary":`)); !slices.Equal(problems, []string{"the data is not valid JSON"}) {
		t.Errorf("CheckData of data that is not JSON = %v", problems)
	}
	if _, problems := validator.CheckDefinition(json.RawMessage(`{}`)); len(problems) != 1 {
		t.Errorf("CheckDefinition of {} = %v", problems)
	}
	if problems := validator.CheckData(json.RawMessage(`{}`), json.RawMessage(routesData)); len(problems) != 1 || !strings.Contains(problems[0], "definition") {
		t.Errorf("CheckData with a bad definition = %v", problems)
	}
}

// Validators are shared by concurrent requests, and their caches with them.
func TestCustomTilesConcurrently(t *testing.T) {
	shared := tiles.NewValidator()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				doc := customDocument(t)
				if (i+j)%2 == 1 {
					def := routesDefinition()
					def.Title = fmt.Sprintf("Routes %d", j)
					tileOf(doc, "route_changes").Definition = tilestest.SealDefinition(def)
				}
				if err := shared.CheckTiles(doc); err != nil {
					t.Error(err)
					return
				}
				if tile := tileOf(doc, "route_changes"); tile.State != tiles.StateReady {
					t.Errorf("custom tile is %s: %v", tile.State, tile.Problems)
				}
			}
		}()
	}
	wg.Wait()
}

// Arrange gives a custom tile's entry of a size its definition does not
// declare the first size it declares.
func TestArrangeCustomSizes(t *testing.T) {
	doc := customDocument(t)
	addTile(doc, tilestest.CustomTile("route_changes_full", "Routes", routesDefinition(), json.RawMessage(routesData)))
	broken := tilestest.CustomTile("broken", "Broken", routesDefinition(), json.RawMessage(routesData))
	broken.Definition = json.RawMessage(`{"sizes":["huge"]}`)
	addTile(doc, broken)
	size := func(id string) tiles.Size {
		for _, entry := range doc.Layout.Entries {
			if entry.Tile == id {
				return entry.Size
			}
		}
		t.Fatalf("no entry for %s", id)
		return ""
	}
	setSize := func(id string, s tiles.Size) {
		for i := range doc.Layout.Entries {
			if doc.Layout.Entries[i].Tile == id {
				doc.Layout.Entries[i].Size = s
			}
		}
	}
	setSize("route_changes", tiles.SizeThird)
	setSize("route_changes_full", tiles.SizeFull)
	setSize("broken", tiles.SizeThird)
	builtins := map[string]tiles.Size{}
	for _, entry := range doc.Layout.Entries {
		builtins[entry.Tile] = entry.Size
	}
	tiles.Arrange(doc)
	if got := size("route_changes"); got != tiles.SizeHalf {
		t.Errorf("an undeclared third became %s, want half", got)
	}
	if got := size("route_changes_full"); got != tiles.SizeFull {
		t.Errorf("a declared full became %s", got)
	}
	if got := size("broken"); got != tiles.SizeThird {
		t.Errorf("an entry of a broken definition became %s", got)
	}
	for id, want := range builtins {
		if tile := tileOf(doc, id); tile.Type != tiles.TypeCustom && size(id) != want {
			t.Errorf("built-in %s became %s", id, size(id))
		}
	}
	if doc.Layout.Landing != tiles.Landing(doc.Layout) {
		t.Errorf("landing %d is not the coerced layout's %d", doc.Layout.Landing, tiles.Landing(doc.Layout))
	}
}

// baum.review/1 has no place for custom tiles: ToReview leaves them out,
// whatever their ID.
func TestToReviewLeavesOutCustomTiles(t *testing.T) {
	doc := validDocument(t)
	without, err := tiles.ToReview(doc)
	if err != nil {
		t.Fatal(err)
	}
	addTile(doc, tilestest.CustomTile("route_changes", "Routes", routesDefinition(), json.RawMessage(routesData)))
	with, err := tiles.ToReview(doc)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := tilestest.RoundTripEqual(without, with); err != nil || !equal {
		t.Errorf("a custom tile changed the review (%v)", err)
	}

	// A custom tile under a built-in single tile's ID the document does not
	// use is not read as that tile.
	doc = validDocument(t)
	for i, tile := range doc.Tiles {
		if tile.ID == tiles.TypeContext {
			doc.Tiles = slices.Delete(doc.Tiles, i, i+1)
			break
		}
	}
	doc.Layout.Entries = removeEntry(doc.Layout.Entries, tiles.TypeContext)
	without, err = tiles.ToReview(doc)
	if err != nil {
		t.Fatal(err)
	}
	addTile(doc, tilestest.CustomTile(tiles.TypeContext, "Context", routesDefinition(), json.RawMessage(routesData)))
	with, err = tiles.ToReview(doc)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := tilestest.RoundTripEqual(without, with); err != nil || !equal {
		t.Errorf("a custom tile with the context tile's ID changed the review (%v)", err)
	}
}

// The golden document holds custom tiles, ready and unavailable, beside the
// built-in tiles, as Validate leaves them. Validating it again changes
// nothing.
func TestCustomTilesGolden(t *testing.T) {
	doc := customDocument(t)
	tamper := func(id string, mutate func(def *tiles.TypeDefinition), data string) {
		def := routesDefinition()
		if mutate != nil {
			mutate(&def)
		}
		addTile(doc, tilestest.CustomTile(id, "Routes", def, json.RawMessage(data)))
	}
	tamper("routes_newer_kit", func(def *tiles.TypeDefinition) { def.Kit = tiles.KitLatest + 1 }, routesData)
	tamper("routes_unresolved", nil, `{"summary":"","routes":[],"related":[{"type":"risk","id":"rsk_gone"}]}`)
	tamper("routes_bad_data", nil, `{"summary":""}`)
	addTile(doc, tilestest.CustomTile("routes_edited", "Routes", routesDefinition(), json.RawMessage(routesData)))
	edited := tileOf(doc, "routes_edited")
	edited.Definition = json.RawMessage(strings.Replace(string(edited.Definition), "Route changes", "Edited", 1))
	tiles.Arrange(doc)

	validated, err := validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"route_changes":     tiles.StateReady,
		"routes_newer_kit":  tiles.StateUnavailable,
		"routes_unresolved": tiles.StateUnavailable,
		"routes_bad_data":   tiles.StateUnavailable,
		"routes_edited":     tiles.StateUnavailable,
	}
	for _, tile := range validated.Tiles {
		state, ok := want[tile.ID]
		if !ok {
			state = tiles.StateReady
		}
		if tile.State != state {
			t.Errorf("tile %s is %s, want %s: %v", tile.ID, tile.State, state, tile.Problems)
		}
	}
	goldentest.Golden(t, "testdata/custom.tiles.json", validated)

	again := golden(t, "custom")
	revalidated, err := validate(t, again)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(validated)
	second, _ := json.Marshal(revalidated)
	if string(first) != string(second) {
		t.Error("validating the golden document again changed it")
	}
}
