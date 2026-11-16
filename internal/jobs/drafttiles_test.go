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
	before := tileview.Builds.Load()
	plain, err := d.get("job", 1, draft, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.get("job", 1, draft, nil, nil)
	if err != nil || !bytes.Equal(plain, again) || tileview.Builds.Load() != before+1 {
		t.Fatalf("an unchanged draft was built %d times: %v", tileview.Builds.Load()-before, err)
	}
	composed, err := d.get("job", 1, draft, composition, nil)
	if err != nil || bytes.Equal(plain, composed) || tileview.Builds.Load() != before+2 {
		t.Fatalf("composed view: %v, %d builds", err, tileview.Builds.Load()-before)
	}
	var body struct {
		Draft int `json:"draft"`
		View  struct {
			Overview struct {
				RiskSummary string `json:"risk_summary"`
			} `json:"overview"`
		} `json:"view"`
	}
	if err := json.Unmarshal(composed, &body); err != nil || body.Draft != 1 || body.View.Overview.RiskSummary != tileviewtest.RiskSummary {
		t.Fatalf("draft %d, view %+v: %v", body.Draft, body.View, err)
	}
}

// A draft shows the tiles the person added to the pull request, kept per
// added tiles: an add builds the view again.
func TestDraftTilesShowTheAddedTiles(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	var d draftTiles
	plain, err := d.get("job", 1, draft, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := library.Get("api_change")
	added := []tileview.AddedTile{{Tile: tiles.LibraryTile{Definition: entry.Encoded, Data: json.RawMessage(`{}`), Pending: true}, Section: "context", Filling: true}}
	before := tileview.Builds.Load()
	withAdded, err := d.get("job", 1, draft, nil, added)
	if err != nil || bytes.Equal(plain, withAdded) || tileview.Builds.Load() != before+1 {
		t.Fatalf("the added tile was not built: %v", err)
	}
	var body struct {
		View struct {
			Sections []struct {
				ID    string   `json:"id"`
				Tiles []string `json:"tiles"`
			} `json:"sections"`
		} `json:"view"`
	}
	if err := json.Unmarshal(withAdded, &body); err != nil {
		t.Fatal(err)
	}
	if first := body.View.Sections[0]; first.ID != "context" || len(first.Tiles) != 1 || first.Tiles[0] != "api_change" {
		t.Errorf("the added tile is not on context: %+v", body.View.Sections)
	}
}
