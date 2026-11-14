package tiles_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/tilestest"
)

func addOp(name string) tiles.Op {
	e, _ := library.Get(name)
	return tiles.Op{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: name, Digest: e.Definition.Digest}}
}

func pendingTile(t *testing.T, name string) tiles.LibraryTile {
	t.Helper()
	e, ok := library.Get(name)
	if !ok {
		t.Fatalf("no library type %s", name)
	}
	return tiles.LibraryTile{Definition: e.Encoded, Data: json.RawMessage(`{}`), Pending: true}
}

// An added library type the version has no tile of is appended from what the
// host holds for it, pending while it is filled, and placed where the add
// says; the composed document keeps every generated-document rule.
func TestApplyOverlayAddsAPendingLibraryTile(t *testing.T) {
	normalized, doc := convert(t, fixture{name: "example", example: true})
	op := addOp("api_change")
	op.To = &tiles.Place{Edge: tiles.EdgeFirst}
	o := overlayOf(t, []tiles.Op{op})
	out, report := tiles.ApplyOverlay(doc, nil, o, []tiles.LibraryTile{pendingTile(t, "api_change")})
	if report.Applied != 1 || len(out.Library) != 1 || !out.Library[0].Pending {
		t.Fatalf("report %+v, library %+v", report, out.Library)
	}
	composed, err := tiles.Compose(doc, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range tilestest.CheckGenerated(normalized, &out, composed) {
		t.Error(failure)
	}
	var tile *tiles.Tile
	for i := range composed.Tiles {
		if composed.Tiles[i].ID == "api_change" {
			tile = &composed.Tiles[i]
		}
	}
	if tile == nil || tile.State != tiles.StatePending || tile.Type != tiles.TypeCustom {
		t.Fatalf("the added tile: %+v", tile)
	}
	if first := composed.Layout.Entries[0].Tile; first != "api_change" {
		t.Errorf("the added tile is not first: %s", first)
	}
	if !library.IsBuiltin(tile.Definition) {
		t.Error("an added built-in type lost its built-in definition")
	}
}

// An add whose type the host holds nothing for (an unknown type) is dropped
// silently, and so is one past the most library tiles a review may have.
func TestApplyOverlayDropsAnUnknownOrOneTooManyAdd(t *testing.T) {
	_, doc := convert(t, fixture{name: "example", example: true})
	_, report := tiles.ApplyOverlay(doc, nil, overlayOf(t, []tiles.Op{addOp("api_change")}), nil)
	if report.Applied != 0 || report.Dropped[tiles.DropUnavailable] != 1 {
		t.Errorf("an add with nothing held: %+v", report)
	}
	full := tiles.Composition{Schema: tiles.CompositionName}
	for i := range tiles.MaxLibraryTiles {
		lib := pendingTile(t, "schema_change")
		lib.Definition = renamed(t, lib.Definition, fmt.Sprintf("kind_%d", i))
		full.Library = append(full.Library, lib)
	}
	_, report = tiles.ApplyOverlay(doc, &full, overlayOf(t, []tiles.Op{addOp("api_change")}), []tiles.LibraryTile{pendingTile(t, "api_change")})
	if report.Applied != 0 || report.Dropped[tiles.DropUnavailable] != 1 {
		t.Errorf("an add past the limit: %+v", report)
	}
}

// renamed is a definition under another name (its digest no longer matches,
// which a read-mode composition accepts).
func renamed(t *testing.T, definition json.RawMessage, name string) json.RawMessage {
	t.Helper()
	var def map[string]any
	if err := json.Unmarshal(definition, &def); err != nil {
		t.Fatal(err)
	}
	def["name"] = name
	out, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A type generation already filled for the version is only placed: the
// host's data for it is not appended a second time.
func TestApplyOverlayPlacesAGeneratedLibraryTile(t *testing.T) {
	_, doc := convert(t, fixture{name: "example", example: true})
	generated := tiles.Composition{Schema: tiles.CompositionName, Library: []tiles.LibraryTile{pendingTile(t, "api_change")}}
	op := addOp("api_change")
	op.To = &tiles.Place{Edge: tiles.EdgeFirst}
	out, report := tiles.ApplyOverlay(doc, &generated, overlayOf(t, []tiles.Op{op}), []tiles.LibraryTile{pendingTile(t, "api_change")})
	if report.Applied != 1 || len(out.Library) != 1 {
		t.Fatalf("report %+v, library %d", report, len(out.Library))
	}
	if out.Layout.Entries[0].Tile != "api_change" {
		t.Errorf("the generated tile was not placed first: %+v", out.Layout.Entries[0])
	}
}

// CheckOp lets a person add a library type the review does not have, and
// refuses one already shown or one past the limit, in the host's words.
func TestCheckOpAdd(t *testing.T) {
	_, doc := convert(t, fixture{name: "example", example: true})
	shown, err := tiles.Compose(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason := tiles.CheckOp(shown, addOp("api_change")); reason != "" {
		t.Errorf("adding a new type was refused: %q", reason)
	}
	out, _ := tiles.ApplyOverlay(doc, nil, overlayOf(t, []tiles.Op{addOp("api_change")}), []tiles.LibraryTile{pendingTile(t, "api_change")})
	with, err := tiles.Compose(doc, &out)
	if err != nil {
		t.Fatal(err)
	}
	if reason := tiles.CheckOp(with, addOp("api_change")); !strings.Contains(reason, "already on the review") {
		t.Errorf("adding a shown type: %q", reason)
	}
	if reason := tiles.CheckOp(with, tiles.Op{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: "overview"}}); !strings.Contains(reason, "not a library tile type") {
		t.Errorf("adding a built-in tile: %q", reason)
	}
	crowded := *with
	crowded.Tiles = append([]tiles.Tile(nil), with.Tiles...)
	for i := range tiles.MaxLibraryTiles {
		crowded.Tiles = append(crowded.Tiles, tiles.Tile{ID: fmt.Sprintf("kind_%d", i), Type: tiles.TypeCustom})
	}
	if reason := tiles.CheckOp(&crowded, addOp("schema_change")); !strings.Contains(reason, "already has 8 library tiles") {
		t.Errorf("adding past the limit: %q", reason)
	}
}

// An add placed right after the overview, as the host places one with no
// place, keeps the landing page's rules: the overview and a severe risks
// tile stay on it, the recommended change is not folded, and every rule of
// a generated document holds.
func TestApplyOverlayAddAfterTheOverviewKeepsTheLandingRules(t *testing.T) {
	for _, f := range []fixture{{name: "example", example: true}, {name: "base"}} {
		normalized, doc := convert(t, f)
		op := addOp("api_change")
		op.To = &tiles.Place{After: tiles.TypeOverview}
		out, report := tiles.ApplyOverlay(doc, nil, overlayOf(t, []tiles.Op{op}), []tiles.LibraryTile{pendingTile(t, "api_change")})
		if report.Applied != 1 {
			t.Fatalf("%s: %+v", f.name, report)
		}
		composed, err := tiles.Compose(doc, &out)
		if err != nil {
			t.Fatal(err)
		}
		for _, failure := range tilestest.CheckGenerated(normalized, &out, composed) {
			t.Errorf("%s: %s", f.name, failure)
		}
		at := -1
		for i, e := range composed.Layout.Entries {
			if e.Tile == "api_change" {
				at = i
			}
		}
		if at < 0 || at > 2 {
			t.Errorf("%s: the added tile is at %d", f.name, at)
		}
		visible := tiles.Visible(composed.Layout)
		for i, e := range composed.Layout.Entries {
			if e.Tile == tiles.TypeOverview && !visible[i] {
				t.Errorf("%s: the overview left the landing page", f.name)
			}
		}
	}
}

// customType is a type made in chat for a review: the kit's starter under
// its own name, sealed, with data citing the review's first hunk.
func customType(t *testing.T, doc *tiles.Document, name string) (tiles.Op, tiles.LibraryTile) {
	t.Helper()
	e, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	def := e.Definition
	def.Name, def.Title = name, "New environment variables"
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	diff := library.DiffOf(doc)
	if len(diff) == 0 {
		t.Fatal("the review has no hunks")
	}
	data := fmt.Sprintf(`{"summary":"Adds one variable.","items":[{"title":"RETRY_LIMIT, default 10","severity":"low","text":null,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID)
	op := tiles.Op{Op: tiles.OpAdd, Add: &tiles.Added{Kind: tiles.AddCustom, Name: name, Digest: def.Digest, Title: def.Title}}
	return op, tiles.LibraryTile{Definition: encoded, Data: json.RawMessage(data)}
}

// A tile made in chat is a custom add: placed from what the host holds for
// it like an added library tile, a ready custom tile that is not baum's
// own, with every rule of a generated document kept; without what the host
// holds it is dropped, and a custom add names its definition's digest.
func TestApplyOverlayAddsATileMadeInChat(t *testing.T) {
	for _, f := range []fixture{{name: "example", example: true}, {name: "base"}} {
		normalized, doc := convert(t, f)
		op, held := customType(t, doc, "env_vars")
		op.To = &tiles.Place{After: tiles.TypeOverview}
		op.Size = tiles.SizeFull
		out, report := tiles.ApplyOverlay(doc, nil, overlayOf(t, []tiles.Op{op}), []tiles.LibraryTile{held})
		if report.Applied != 1 || len(out.Library) != 1 {
			t.Fatalf("%s: report %+v, library %d", f.name, report, len(out.Library))
		}
		composed, err := tiles.Compose(doc, &out)
		if err != nil {
			t.Fatal(err)
		}
		for _, failure := range tilestest.CheckGenerated(normalized, &out, composed) {
			t.Errorf("%s: %s", f.name, failure)
		}
		var tile *tiles.Tile
		for i := range composed.Tiles {
			if composed.Tiles[i].ID == "env_vars" {
				tile = &composed.Tiles[i]
			}
		}
		if tile == nil || tile.Type != tiles.TypeCustom || tile.State != tiles.StateReady || library.IsBuiltin(tile.Definition) {
			t.Fatalf("%s: the tile made in chat: %+v", f.name, tile)
		}
		at, size := -1, tiles.Size("")
		for i, e := range composed.Layout.Entries {
			if e.Tile == "env_vars" {
				at, size = i, e.Size
			}
		}
		if at < 0 || at > 2 || size != tiles.SizeFull {
			t.Errorf("%s: the tile is at %d, %s", f.name, at, size)
		}
		if reason := tiles.CheckOp(composed, op); !strings.Contains(reason, "already on the review") {
			t.Errorf("%s: adding it twice: %q", f.name, reason)
		}
		// Undone, the generated layout comes back.
		undone, report := tiles.ApplyOverlay(doc, nil, overlayOf(t), []tiles.LibraryTile{held})
		if report.Applied != 0 || len(undone.Library) != 0 {
			t.Errorf("%s: without the add: %+v", f.name, report)
		}
	}
	_, doc := convert(t, fixture{name: "example", example: true})
	op, _ := customType(t, doc, "env_vars")
	if _, report := tiles.ApplyOverlay(doc, nil, overlayOf(t, []tiles.Op{op}), nil); report.Dropped[tiles.DropUnavailable] != 1 {
		t.Errorf("an add with nothing held: %+v", report)
	}
	op.Add.Digest = ""
	if err := tiles.CheckOpShape(op); err == nil {
		t.Error("a custom add without a digest passed")
	}
	op.Add.Kind = "weather"
	op.Add.Digest = "sha256:00"
	if err := tiles.CheckOpShape(op); err == nil {
		t.Error("an add of an unknown kind passed")
	}
}

// CheckCitedData holds a tile made in chat to its review: the data matches
// its schema, every citation resolves, and it cites at least one hunk,
// file, prompt or session event.
func TestCheckCitedData(t *testing.T) {
	_, doc := convert(t, fixture{name: "example", example: true})
	_, held := customType(t, doc, "env_vars")
	v := tiles.NewValidator()
	if problems, grounded := v.CheckCitedData(doc, held.Definition, held.Data); len(problems) > 0 || !grounded {
		t.Errorf("grounded data: %v %v", problems, grounded)
	}
	if problems, grounded := v.CheckCitedData(doc, held.Definition, json.RawMessage(`{"summary":"Nothing.","items":[]}`)); len(problems) > 0 || grounded {
		t.Errorf("data citing nothing: %v %v", problems, grounded)
	}
	unknown := json.RawMessage(`{"summary":"x","items":[{"title":"t","severity":"low","text":null,"cites":[{"type":"hunk","id":"h_000000000000"}]}]}`)
	if problems, grounded := v.CheckCitedData(doc, held.Definition, unknown); len(problems) != 1 || grounded || !strings.Contains(problems[0], "does not have") {
		t.Errorf("an unknown hunk: %v %v", problems, grounded)
	}
	if problems, _ := v.CheckCitedData(doc, held.Definition, json.RawMessage(`{"summary":1}`)); len(problems) == 0 {
		t.Error("data that does not match its schema passed")
	}
}
