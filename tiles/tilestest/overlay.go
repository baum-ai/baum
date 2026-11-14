package tilestest

import (
	"fmt"
	"math/rand/v2"

	"github.com/baum-ai/baum/tiles"
)

// RandomOps draws n layout operations for a document a person is shown, as
// a person or an agent might ask for them: every kind of operation, aimed
// mostly at its tiles and anchored to them, with some aimed at tiles it does
// not have (as an edit made on another version would be), sizes a tile may
// not have, the overview and risks tiles, and the tile itself as its own
// anchor. Every operation is well formed (tiles.CheckOpShape), as a stored
// overlay's are. The draw is deterministic for a given rng state.
func RandomOps(rng *rand.Rand, doc *tiles.Document, n int) []tiles.Op {
	ids := []string{tiles.TypeOverview, tiles.TypeRisks, "sec_gone_away", "table_gone_away"}
	var library []string
	for _, tile := range doc.Tiles {
		ids = append(ids, tile.ID)
		if tile.Type == tiles.TypeCustom {
			library = append(library, tile.ID)
		}
	}
	pick := func() string { return ids[rng.IntN(len(ids))] }
	sizes := []tiles.Size{tiles.SizeFull, tiles.SizeHalf, tiles.SizeThird}
	var ops []tiles.Op
	for len(ops) < n {
		tile := pick()
		var op tiles.Op
		switch rng.IntN(10) {
		case 0, 1, 2:
			to := &tiles.Place{}
			switch rng.IntN(4) {
			case 0:
				to.Edge = tiles.EdgeFirst
			case 1:
				to.Edge = tiles.EdgeLast
			case 2:
				to.Before = pick()
			default:
				to.After = pick()
			}
			op = tiles.Op{Op: tiles.OpMove, Tile: tile, To: to}
		case 3, 4:
			op = tiles.Op{Op: tiles.OpResize, Tile: tile, Size: sizes[rng.IntN(len(sizes))]}
		case 5, 6:
			op = tiles.Op{Op: tiles.OpHide, Tile: tile}
		case 7:
			op = tiles.Op{Op: tiles.OpShow, Tile: tile}
		case 8:
			op = tiles.Op{Op: []string{tiles.OpPin, tiles.OpUnpin}[rng.IntN(2)], Tile: tile}
		default:
			if rng.IntN(2) == 0 {
				op = tiles.Op{Op: tiles.OpChangesShown, Count: new(1 + rng.IntN(8))}
				break
			}
			name := "unknown_library"
			if len(library) > 0 && rng.IntN(2) == 0 {
				name = library[rng.IntN(len(library))]
			}
			op = tiles.Op{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: name}}
		}
		if tiles.CheckOpShape(op) != nil {
			continue
		}
		ops = append(ops, op)
	}
	return ops
}

// RandomOverlay draws an overlay of batches batches of 1 to 6 operations
// each (RandomOps), now and then a reset, with the host's summaries for doc.
func RandomOverlay(rng *rand.Rand, doc *tiles.Document, batches int) *tiles.Overlay {
	o := &tiles.Overlay{Schema: tiles.OverlayName}
	for range batches {
		b := tiles.Batch{ID: fmt.Sprintf("edt_%012x", rng.Uint64()&0xffffffffffff), At: "2026-11-14T10:00:00Z", By: []string{"chat", "page"}[rng.IntN(2)], Summary: []string{}}
		if rng.IntN(12) == 0 {
			b.Reset = true
		} else {
			b.Ops = RandomOps(rng, doc, 1+rng.IntN(6))
			for _, op := range b.Ops {
				b.Summary = append(b.Summary, tiles.OpSummary(doc, op))
			}
		}
		next, err := o.With(b)
		if err != nil {
			continue
		}
		o = &next
	}
	return o
}
