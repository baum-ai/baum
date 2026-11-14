package tileview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// The built-in types' frame documents are registered at start-up.
func TestRegisterLibrary(t *testing.T) {
	if err := RegisterLibrary(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range library.All() {
		if _, ok := FrameRegistry().Get(entry.Definition.Digest); !ok {
			t.Errorf("%s is not registered", entry.Definition.Name)
		}
	}
}

// A stored composition's library tile embeds its definition, so a version
// keeps working after the built-in type changes: it is served with its own
// definition and digest, and its frame is registered under that digest.
func TestStoredLibraryTileKeepsItsDefinition(t *testing.T) {
	document := tileviewtest.Encode(t, tileviewtest.Base(t))
	doc := Build(document, nil, tiles.Options{}).checked
	var hunk string
	for _, tile := range doc.Tiles {
		if len(tile.Covers) > 0 {
			hunk = tile.Covers[0]
			break
		}
	}
	if hunk == "" {
		t.Fatal("no hunk")
	}
	entry, _ := library.Get("schema_change")
	old := entry.Definition
	old.Title = "Schema changes (an earlier release)"
	old.Source.HTML = strings.Replace(old.Source.HTML, "<style>", "<style>/* earlier */", 1)
	encoded, err := library.Seal(&old)
	if err != nil {
		t.Fatal(err)
	}
	if old.Digest == entry.Definition.Digest {
		t.Fatal("the earlier definition has the current digest")
	}
	data := `{"engine":"sqlite","summary":"Adds widgets.","migrations":[],"tables":[{"name":"widgets","change":"created","renamed_from":null,"columns":[],"indexes":[],"cites":[{"type":"hunk","id":"` + hunk + `"}]}],"data_changes":[],"readers":[]}`
	composition, _ := json.Marshal(tiles.Composition{Schema: tiles.CompositionName, Library: []tiles.LibraryTile{{Definition: encoded, Data: json.RawMessage(data)}}})
	served := Build(document, composition, tiles.Options{})
	var tile *tiles.Tile
	for i := range served.Tiles.Tiles {
		if served.Tiles.Tiles[i].ID == "schema_change" {
			tile = &served.Tiles.Tiles[i]
		}
	}
	if tile == nil || tile.State != tiles.StateReady {
		t.Fatalf("the stored library tile: %+v", tile)
	}
	if !strings.Contains(string(tile.Definition), old.Digest) || strings.Contains(string(tile.Definition), "<style>") {
		t.Errorf("served definition: %s", tile.Definition)
	}
	if _, ok := FrameRegistry().Get(old.Digest); !ok {
		t.Error("the earlier definition's frame is not registered")
	}
}

// The browser fixture's library composition serves its four tiles ready.
func TestLibraryCompositionFixture(t *testing.T) {
	document := tileviewtest.Encode(t, tileviewtest.Base(t))
	served := Build(document, tileviewtest.LibraryComposition(t, document), tiles.Options{})
	ready := map[string]bool{}
	for _, tile := range served.checked.Tiles {
		if tile.State == tiles.StateReady {
			ready[tile.ID] = true
		} else if tile.Type == tiles.TypeCustom || tile.Type == tiles.TypeTable || tile.Type == tiles.TypeCode {
			t.Errorf("%s: %s %v", tile.ID, tile.State, tile.Problems)
		}
	}
	for _, id := range []string{"schema_change", "api_change", "table_retry_states", "code_sync_backoff"} {
		if !ready[id] {
			t.Errorf("%s is not served ready", id)
		}
	}
	if served.Tiles.Layout.Entries[0].Tile != tiles.TypeOverview {
		t.Errorf("layout: %v", served.Tiles.Layout.Entries)
	}
}
