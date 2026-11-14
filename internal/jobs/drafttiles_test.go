package jobs

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
)

// A draft's tile view is kept per draft count and composition: the same
// draft with another composition is built again and laid out as composed.
func TestDraftTilesKeepsTheViewPerDraftAndComposition(t *testing.T) {
	draft := tileviewtest.Encode(t, tileviewtest.Base(t))
	composition, pinned := tileviewtest.PinnedComposition(t, draft)
	var d draftTiles
	before := tileview.Builds.Load()
	plain, err := d.get("job", 1, draft, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.get("job", 1, draft, nil)
	if err != nil || !bytes.Equal(plain, again) || tileview.Builds.Load() != before+1 {
		t.Fatalf("an unchanged draft was built %d times: %v", tileview.Builds.Load()-before, err)
	}
	composed, err := d.get("job", 1, draft, composition)
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
