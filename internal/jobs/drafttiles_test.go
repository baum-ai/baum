package jobs

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
)

// A draft's tile view is kept per draft count and composition: the same
// draft with another composition is built again and laid out as composed.
func TestDraftTilesKeepsTheViewPerDraftAndComposition(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	composition, pinned := tileviewtest.PinnedComposition(t, draft)
	var d draftTiles
	before := tileview.Builds.Load()
	plain, err := d.get("job", 1, draft, nil, tileview.Layout{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.get("job", 1, draft, nil, tileview.Layout{})
	if err != nil || !bytes.Equal(plain, again) || tileview.Builds.Load() != before+1 {
		t.Fatalf("an unchanged draft was built %d times: %v", tileview.Builds.Load()-before, err)
	}
	composed, err := d.get("job", 1, draft, composition, tileview.Layout{})
	if err != nil || bytes.Equal(plain, composed) || tileview.Builds.Load() != before+2 {
		t.Fatalf("composed view: %v, %d builds", err, tileview.Builds.Load()-before)
	}
	var body struct {
		Draft int `json:"draft"`
		Tiles struct {
			Layout struct {
				Entries []struct {
					Tile string `json:"tile"`
				} `json:"entries"`
			} `json:"layout"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(composed, &body); err != nil || body.Draft != 1 || body.Tiles.Layout.Entries[0].Tile != pinned {
		t.Fatalf("draft %d, first tile %v: %v", body.Draft, body.Tiles.Layout.Entries, err)
	}
}

// A draft is shown with the pull request's own layout, kept per layout: an
// edit builds the view again, and a regeneration keeps the person's layout.
func TestDraftTilesApplyThePersonsLayout(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	o, err := (&tiles.Overlay{Schema: tiles.OverlayName}).With(tiles.Batch{ID: "edt_0000000000b1", By: "chat", Summary: []string{},
		Ops: []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}})
	if err != nil {
		t.Fatal(err)
	}
	var d draftTiles
	plain, err := d.get("job", 1, draft, nil, tileview.Layout{})
	if err != nil {
		t.Fatal(err)
	}
	before := tileview.Builds.Load()
	edited, err := d.get("job", 1, draft, nil, tileview.Layout{Overlay: &o})
	if err != nil || bytes.Equal(plain, edited) || tileview.Builds.Load() != before+1 {
		t.Fatalf("the edited layout was not built: %v", err)
	}
	var body struct {
		Tiles struct {
			Layout struct {
				Entries []struct {
					Tile  string `json:"tile"`
					Group string `json:"group"`
				} `json:"entries"`
			} `json:"layout"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(edited, &body); err != nil {
		t.Fatal(err)
	}
	last := body.Tiles.Layout.Entries[len(body.Tiles.Layout.Entries)-1]
	if last.Tile != "context" || last.Group != "hidden" {
		t.Errorf("the hidden tile is not last and hidden: %+v", last)
	}
}
