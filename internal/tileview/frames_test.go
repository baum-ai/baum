package tileview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/tilestest"
	"github.com/baum-ai/baum/tiles/view"
)

// withCustomTiles serves the synthetic custom tiles in every document Build
// converts during the test.
func withCustomTiles(t *testing.T) {
	t.Helper()
	t.Cleanup(SetFixture(tileviewtest.AddCustomTiles))
}

// withFrames gives the test a registry of its own.
func withFrames(t *testing.T, r *tileframes.Registry) {
	t.Helper()
	previous := frames
	frames = r
	t.Cleanup(func() { frames = previous })
}

func custom(v *view.View, id string) *view.Tile {
	if v == nil {
		return nil
	}
	for i := range v.Tiles {
		if v.Tiles[i].ID == id {
			return &v.Tiles[i]
		}
	}
	return nil
}

// Build registers the ready custom tiles' documents, which are the kit's
// composition of their HTML, and not the unavailable tile's.
func TestBuildRegistersReadyCustomTiles(t *testing.T) {
	withCustomTiles(t)
	withFrames(t, tileframes.New(tileframes.MaxBytes))
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{})
	if r.View == nil {
		t.Fatal(r.Problem)
	}
	for _, id := range []string{tileviewtest.WidgetHealth, tileviewtest.EscapeProbe} {
		tile := custom(r.View, id)
		if tile == nil || tile.State != tiles.StateReady {
			t.Fatalf("%s: %+v", id, tile)
		}
		def := tileviewtest.CustomDefinition(id)
		document, ok := frames.Get(def.Digest)
		want, err := kit.Compose(def.Kit, def.Source.HTML)
		if err != nil || !ok || !bytes.Equal(document, want) {
			t.Fatalf("%s: registered %v, %v", id, ok, err)
		}
	}
	unavailable := custom(r.View, tileviewtest.NewerKit)
	if unavailable == nil || unavailable.State != tiles.StateUnavailable || len(unavailable.Problems) == 0 {
		t.Fatalf("newer kit: %+v", unavailable)
	}
	if _, ok := frames.Get(tileviewtest.CustomDefinition(tileviewtest.NewerKit).Digest); ok {
		t.Fatal("an unavailable tile's frame was registered")
	}
	if len(r.Frames) != 2 {
		t.Fatalf("%d frames to register again", len(r.Frames))
	}
	// Another conversion of a document with the same types composes nothing.
	before := tileframes.Composed.Load()
	Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{Draft: true})
	if got := tileframes.Composed.Load() - before; got != 0 {
		t.Fatalf("composed %d documents again", got)
	}
}

// The served view holds each custom tile's definition without its
// HTML; the digest is the one the validator checked.
func TestBuildServesDefinitionsWithoutHTML(t *testing.T) {
	withCustomTiles(t)
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{})
	answer, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range tileviewtest.CustomTileIDs {
		def := tileviewtest.CustomDefinition(id)
		if bytes.Contains(answer.Body.Data, []byte("baum.tile(")) || strings.Contains(string(answer.Body.Data), "outside.invalid/base") {
			t.Fatal("the answer holds a tile's HTML")
		}
		var served struct {
			Digest string         `json:"digest"`
			Title  string         `json:"title"`
			Kit    int            `json:"kit"`
			Sizes  []string       `json:"sizes"`
			Source map[string]any `json:"source"`
		}
		if err := json.Unmarshal(custom(r.View, id).Definition, &served); err != nil {
			t.Fatal(err)
		}
		if served.Digest != def.Digest || served.Title != def.Title || served.Kit != def.Kit || len(served.Sizes) != len(def.Sizes) {
			t.Errorf("%s: served %+v", id, served)
		}
		if len(served.Source) != 2 || served.Source["format"] != "html" || served.Source["bytes"] != float64(len(def.Source.HTML)) {
			t.Errorf("%s: source %v", id, served.Source)
		}
	}
}

// A ready tile whose frame document cannot be made is unavailable: its HTML
// is within the format's 262144 characters but over the kit's 256 KiB.
func TestBuildMarksAnUncomposableTileUnavailable(t *testing.T) {
	def := tileviewtest.CustomDefinition(tileviewtest.WidgetHealth)
	def.Name, def.Digest = "wide_widgets", ""
	def.Source.HTML = "<script>baum.tile({glance: () => 'x', detail: () => 'x'});</script><!--" + strings.Repeat("界", 100000) + "-->"
	restore := SetFixture(func(doc *tiles.Document) {
		tile := tilestest.CustomTile("wide_widgets", "Wide widgets", def, json.RawMessage(`{"summary":"x","stores":[]}`))
		doc.Tiles = append(doc.Tiles, tile)
	})
	defer restore()
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{})
	tile := custom(r.View, "wide_widgets")
	if tile.State != tiles.StateUnavailable || len(tile.Problems) != 1 || !strings.Contains(tile.Problems[0], "256 KiB") || len(r.Frames) != 0 {
		t.Fatalf("%s: %v, %d frames", tile.State, tile.Problems, len(r.Frames))
	}
}

// An answer served from a cache registers its frames again, so a frame the
// registry dropped comes back with the answer that names it.
func TestCacheRegistersFramesAgain(t *testing.T) {
	withFrames(t, tileframes.New(1))
	kept := tileviewtest.CustomDefinition(tileviewtest.WidgetHealth)
	other := tileviewtest.CustomDefinition(tileviewtest.EscapeProbe)
	var c Cache
	c.Put("v1", Answer{Body: etagjson.Body{Data: []byte("{}")}, Frames: Frames{kept}})
	Frames{kept}.Register()
	Frames{other}.Register() // the bound of one byte drops kept
	if _, ok := frames.Get(kept.Digest); ok {
		t.Fatal("the bound kept two documents")
	}
	if _, ok := c.Get("v1"); !ok {
		t.Fatal("the answer is gone")
	}
	if _, ok := frames.Get(kept.Digest); !ok {
		t.Fatal("serving the answer did not register its frame again")
	}
	// The kept HTML counts towards the cache's bytes.
	if c.bytes != 2+len(kept.Source.HTML) {
		t.Fatalf("%d bytes", c.bytes)
	}
}

func TestServedDefinition(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"name":"a","source":{"format":"html","html":"<p>é</p>"},"digest":"d"}`, `{"digest":"d","name":"a","source":{"bytes":9,"format":"html"}}`},
		{`{"name":"a","source":{"format":"html","html":"x","extra":"y"}}`, `{"name":"a","source":{"bytes":1,"format":"html"}}`},
		{`{"name":"a","source":"<p>x</p>"}`, `{"name":"a","source":{"bytes":0}}`},
		{`{"name":"a","source":{"html":7}}`, `{"name":"a","source":{"bytes":0}}`},
		{`{"name":"a"}`, `{"name":"a"}`},
		{`"<p>x</p>"`, `{}`},
		{`null`, `{}`},
		{`[1]`, `{}`},
	} {
		if got := string(ServedDefinition(json.RawMessage(tc.in))); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.in, got, tc.want)
		}
	}
}

// SetFixture's restore puts back what was there.
func TestSetFixtureRestores(t *testing.T) {
	restore := SetFixture(tileviewtest.AddCustomTiles)
	if r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{}); custom(r.View, tileviewtest.WidgetHealth) == nil {
		t.Fatal("the fixture did not run")
	}
	restore()
	if r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{}); custom(r.View, tileviewtest.WidgetHealth) != nil || len(r.Frames) != 0 {
		t.Fatal("the fixture outlived its restore")
	}
}

// A composed answer's custom tiles, a library tile the composition adds and
// the fixture's alike, are registered and served with their digests and
// without their HTML.
func TestBuildServesComposedCustomTilesWithoutHTML(t *testing.T) {
	withCustomTiles(t)
	withFrames(t, tileframes.New(tileframes.MaxBytes))
	library := tilestest.LibraryTile("retry_budget", "h_000000000001")
	var libraryDef tiles.TypeDefinition
	if err := json.Unmarshal(library.Definition, &libraryDef); err != nil {
		t.Fatal(err)
	}
	composition, err := json.Marshal(view.Composition{
		Schema:   view.CompositionName,
		Overview: view.Overview{Screenshots: []string{}, Intent: []view.Intent{}},
		Sections: []view.Placement{},
		Library:  []tiles.LibraryTile{library},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), composition, tiles.Options{})
	if r.View == nil || custom(r.View, libraryDef.Name) == nil {
		t.Fatalf("not composed: %q", r.Problem)
	}
	answer, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(answer.Body.Data, []byte("baum.tile(")) || bytes.Contains(answer.Body.Data, []byte(`"html":`)) {
		t.Fatal("the composed answer holds a tile's HTML")
	}
	for id, def := range map[string]tiles.TypeDefinition{
		libraryDef.Name:           libraryDef,
		tileviewtest.WidgetHealth: tileviewtest.CustomDefinition(tileviewtest.WidgetHealth),
	} {
		tile := custom(r.View, id)
		if tile == nil || tile.State != tiles.StateReady {
			t.Fatalf("%s: %+v", id, tile)
		}
		if !bytes.Contains(tile.Definition, []byte(def.Digest)) {
			t.Errorf("%s: served without its digest", id)
		}
		if _, ok := frames.Get(def.Digest); !ok {
			t.Errorf("%s: not registered", id)
		}
	}
	if len(r.Frames) != 3 {
		t.Errorf("%d frames to register again", len(r.Frames))
	}
}
