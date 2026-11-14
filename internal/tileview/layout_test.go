package tileview

import (
	"bytes"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/tiles"
)

func overlay(t *testing.T, ops ...tiles.Op) *tiles.Overlay {
	t.Helper()
	o, err := (&tiles.Overlay{Schema: tiles.OverlayName}).With(tiles.Batch{ID: "edt_0000000000a1", At: "2026-11-14T10:00:00Z", By: "chat", Ops: ops, Summary: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return &o
}

// A layout with no edits serves Build's answer byte for byte; one with edits
// serves them on the stored composition or the converted layout, and its key
// names it.
func TestBuildWithLayout(t *testing.T) {
	document := tileviewtest.Encode(t, tileviewtest.Base(t))
	composition, _ := tileviewtest.PinnedComposition(t, document)
	first := func(r Response) string { return r.Tiles.Layout.Entries[0].Tile }
	reset, err := overlay(t, tiles.Op{Op: tiles.OpHide, Tile: "context"}).With(tiles.Batch{ID: "edt_0000000000a2", By: "page", Reset: true, Summary: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]byte{nil, composition} {
		want, err := Encode(Build(document, c, tiles.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, layout := range []Layout{{}, {Overlay: &tiles.Overlay{Schema: tiles.OverlayName}}, {Overlay: &reset}} {
			got, err := Encode(BuildWith(document, c, layout, tiles.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Body.Data, want.Body.Data) || LayoutKey(layout) != "" {
				t.Error("a layout with no edits changed the answer")
			}
		}
		edited := Layout{Overlay: overlay(t, tiles.Op{Op: tiles.OpMove, Tile: "evidence", To: &tiles.Place{Edge: tiles.EdgeFirst}}, tiles.Op{Op: tiles.OpHide, Tile: "context"})}
		r := BuildWith(document, c, edited, tiles.Options{})
		if r.Problem != "" || r.Tiles.Origin.Kind != tiles.OriginGenerated {
			t.Fatalf("problem %q, origin %s", r.Problem, r.Tiles.Origin.Kind)
		}
		hidden := false
		for _, e := range r.Tiles.Layout.Entries {
			hidden = hidden || e.Tile == "context" && e.Group == tiles.GroupHidden
		}
		if !hidden || first(r) == first(Build(document, c, tiles.Options{})) && first(r) != "evidence" && !r.Tiles.Layout.Entries[0].Pin {
			t.Errorf("the edits do not show: %+v", r.Tiles.Layout.Entries)
		}
		if LayoutKey(edited) == "" || LayoutKey(edited) == LayoutKey(Layout{Overlay: overlay(t, tiles.Op{Op: tiles.OpHide, Tile: "context"})}) {
			t.Error("the layout key does not tell the layouts apart")
		}
	}
	// Edits whose tiles the version lacks are dropped and counted.
	before := workstats.Take()
	r := BuildWith(document, nil, Layout{Overlay: overlay(t, tiles.Op{Op: tiles.OpHide, Tile: "sec_gone"})}, tiles.Options{})
	if r.Tiles == nil || r.Tiles.Origin.Kind != tiles.OriginConverted {
		t.Error("a layout whose edits all dropped is not the converted one")
	}
	dropped := int64(0)
	for _, d := range workstats.Take().Since(before) {
		if d.Name == "tileview.overlay_ops_dropped" {
			dropped = d.Count
		}
	}
	if dropped != 1 {
		t.Errorf("%d dropped ops counted", dropped)
	}
}
