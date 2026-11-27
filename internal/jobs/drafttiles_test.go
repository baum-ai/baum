package jobs

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A draft's view is kept per draft count and composition: the same draft
// with another composition is built again and shows it.
func TestDraftTilesKeepsTheViewPerDraftAndComposition(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	composition := tileviewtest.ViewComposition(t)
	var d draftTiles
	q := &Queue{drafts: map[string]heldDraft{"job": {}}}
	before := tileview.Builds.Load()
	plain, err := d.get(q, "job", 1, draft, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.get(q, "job", 1, draft, nil, nil)
	if err != nil || !bytes.Equal(plain, again) || tileview.Builds.Load() != before+1 {
		t.Fatalf("an unchanged draft was built %d times: %v", tileview.Builds.Load()-before, err)
	}
	composed, err := d.get(q, "job", 1, draft, composition, nil)
	if err != nil || bytes.Equal(plain, composed) || tileview.Builds.Load() != before+2 {
		t.Fatalf("composed view: %v, %d builds", err, tileview.Builds.Load()-before)
	}
	var body struct {
		Draft    int `json:"draft"`
		Document struct {
			Summary struct {
				RiskWhy string `json:"risk_why"`
			} `json:"summary"`
		} `json:"document"`
	}
	if err := json.Unmarshal(composed, &body); err != nil || body.Draft != 1 || body.Document.Summary.RiskWhy != tileviewtest.RiskSummary {
		t.Fatalf("draft %d, document %+v: %v", body.Draft, body.Document.Summary, err)
	}
}

// A draft shows the tiles the person added to the pull request, kept per
// added tiles: an add builds the view again.
func TestDraftTilesShowTheAddedTiles(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	var d draftTiles
	q := &Queue{drafts: map[string]heldDraft{"job": {}}}
	plain, err := d.get(q, "job", 1, draft, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := library.Get("api_change")
	added := []tileview.AddedTile{{Tile: tiles.LibraryTile{Definition: entry.Encoded, Data: json.RawMessage(`{}`), Pending: true}, Section: "context", Filling: true}}
	before := tileview.Builds.Load()
	withAdded, err := d.get(q, "job", 1, draft, nil, added)
	if err != nil || bytes.Equal(plain, withAdded) || tileview.Builds.Load() != before+1 {
		t.Fatalf("the added tile was not built: %v", err)
	}
	var body struct {
		Document struct {
			Tiles []struct {
				ID    string `json:"id"`
				Owner string `json:"owner"`
			} `json:"tiles"`
		} `json:"document"`
	}
	if err := json.Unmarshal(withAdded, &body); err != nil {
		t.Fatal(err)
	}
	onContext := 0
	for _, tile := range body.Document.Tiles {
		if tile.ID == "api_change" && tile.Owner == "context" {
			onContext++
		}
	}
	if onContext != 1 {
		t.Errorf("the added tile is not on context: %+v", body.Document.Tiles)
	}
}
