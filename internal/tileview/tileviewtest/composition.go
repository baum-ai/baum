package tileviewtest

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
)

// PinnedComposition returns a composition of the review (as JSON) that pins
// the tile placed last in the converted layout, at full width, and the ID of
// that tile: composing moves it to the front of the page. Entries the
// composition omits are appended, so it names only the pinned tile.
func PinnedComposition(t testing.TB, document []byte) (composition []byte, pinned string) {
	t.Helper()
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(doc.Layout.Entries) - 1; i >= 0 && pinned == ""; i-- {
		e := doc.Layout.Entries[i]
		if e.Group == "" && e.Tile != tiles.TypeOverview && e.Tile != tiles.TypeRisks {
			pinned = e.Tile
		}
	}
	if pinned == "" {
		t.Fatal("the converted document has no tile to pin")
	}
	c := tiles.Composition{Schema: tiles.CompositionName, Layout: tiles.CompositionLayout{Entries: []tiles.Entry{{Tile: pinned, Size: tiles.SizeFull, Pin: true}}}}
	composition, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return composition, pinned
}
