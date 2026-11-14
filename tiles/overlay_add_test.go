package tiles_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
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
