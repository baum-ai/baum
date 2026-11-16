package tilescorpus

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// randomOverlays is how many seeded layout overlays each version gets, each
// replayed on its converted layout and on its template.
const randomOverlays = 3

// overlayTally counts what replaying people's layout edits found.
type overlayTally struct {
	// replays counts the overlays applied, ops the ops they held, and
	// applied and dropped what the replays did with them.
	replays, ops, applied int
	dropped               map[tiles.DropReason]int
	// carried counts the replays of an overlay drawn on a pull request's
	// previous version on its next one, and carriedDropped the ops they
	// dropped by reason.
	carried        int
	carriedOps     int
	carriedDropped map[tiles.DropReason]int
	// vanished counts the ops of carried overlays whose tile the previous
	// version had and this one does not.
	vanished int
	// moves counts the last ops of carried overlays that were moves which
	// applied, and held those whose relation still holds once composed.
	moves, held int
	failures    int
	durations   []time.Duration
	// previous holds the overlays drawn on the version before, of the same
	// pull request, and previousTiles that version's tile IDs.
	previous      []*tiles.Overlay
	previousTiles map[string]bool
}

func newOverlayTally() overlayTally {
	return overlayTally{dropped: map[tiles.DropReason]int{}, carriedDropped: map[tiles.DropReason]int{}}
}

// overlays replays seeded layout overlays on a version, and the overlays
// drawn on the pull request's previous version (fresh tells it is a new
// pull request), and checks every generated document.
func overlays(stats *tally, v version, normalized []byte, doc *tiles.Document, fresh bool) {
	o := &stats.overlays
	if fresh {
		o.previous, o.previousTiles = nil, nil
	}
	current := map[string]bool{}
	for _, tile := range doc.Tiles {
		current[tile.ID] = true
	}
	template, _ := tiles.Template(doc, nil)
	replay := func(overlay *tiles.Overlay, base *tiles.Composition, label string) (*tiles.Document, tiles.OverlayReport, bool) {
		start := time.Now()
		c, report := tiles.ApplyOverlay(doc, base, overlay, nil)
		composed, err := tiles.Compose(doc, &c)
		o.durations = append(o.durations, time.Since(start))
		o.replays++
		if err != nil {
			stats.fail(label + ": compose")
			o.failures++
			return nil, report, false
		}
		failures := tilestest.CheckGenerated(normalized, &c, composed)
		for _, failure := range failures {
			stats.fail(label + ": " + invariantOf(failure))
		}
		if len(failures) > 0 {
			o.failures++
		}
		return composed, report, len(failures) == 0
	}
	count := func(overlay *tiles.Overlay) int {
		n := 0
		for _, b := range overlay.Active() {
			n += len(b.Ops)
		}
		return n
	}

	for _, previous := range o.previous {
		for _, b := range previous.Active() {
			for _, op := range b.Ops {
				if op.Tile != "" && o.previousTiles[op.Tile] && !current[op.Tile] {
					o.vanished++
				}
			}
		}
		for k, base := range []*tiles.Composition{nil, &template} {
			composed, report, ok := replay(previous, base, "carried overlay")
			o.carried++
			o.carriedOps += count(previous)
			for reason, n := range report.Dropped {
				o.carriedDropped[reason] += n
			}
			if ok && k == 0 {
				if held, counted := lastMoveHolds(previous, doc, composed); counted {
					o.moves++
					if held {
						o.held++
					}
				}
			}
		}
	}

	seed := fnv.New64a()
	_, _ = fmt.Fprintf(seed, "overlay %d %s %d", v.store, v.pullRequest, v.number)
	var drawn []*tiles.Overlay
	for i := range randomOverlays {
		rng := rand.New(rand.NewPCG(seed.Sum64(), uint64(i)))
		overlay := tilestest.RandomOverlay(rng, doc, 1+rng.IntN(10))
		drawn = append(drawn, overlay)
		for _, base := range []*tiles.Composition{nil, &template} {
			_, report, _ := replay(overlay, base, "overlay")
			o.ops += count(overlay)
			o.applied += report.Applied
			for reason, n := range report.Dropped {
				o.dropped[reason] += n
			}
		}
	}
	o.previous, o.previousTiles = drawn, current
}

// lastMoveHolds reports whether the last op of an overlay, when it is a move
// that the version can apply, still stands as asked in the composed
// document: first or last among the tiles that are neither pinned nor kept on
// the landing page, before or after its anchor. counted is false when the
// last op is not such a move.
func lastMoveHolds(o *tiles.Overlay, converted, composed *tiles.Document) (held, counted bool) {
	active := o.Active()
	if len(active) == 0 || len(active[len(active)-1].Ops) == 0 {
		return false, false
	}
	ops := active[len(active)-1].Ops
	op := ops[len(ops)-1]
	if op.Op != tiles.OpMove || tiles.CheckOp(converted, op) != "" {
		return false, false
	}
	var order []string
	for _, entry := range composed.Layout.Entries {
		if entry.Pin || entry.Group == tiles.GroupObsolete || entry.Group == tiles.GroupHidden {
			continue
		}
		order = append(order, entry.Tile)
	}
	at := slices.Index(order, op.Tile)
	if at < 0 {
		return false, true
	}
	switch {
	case op.To.Edge == tiles.EdgeFirst:
		// The overview and a severe risks tile may stand before it.
		for _, id := range order[:at] {
			if id != tiles.TypeOverview && id != tiles.TypeRisks {
				return false, true
			}
		}
		return true, true
	case op.To.Edge == tiles.EdgeLast:
		return at == len(order)-1, true
	case op.To.Before != "":
		anchor := slices.Index(order, op.To.Before)
		return anchor < 0 || at < anchor, true
	default:
		anchor := slices.Index(order, op.To.After)
		return anchor < 0 || at > anchor, true
	}
}

func reportOverlays(t *testing.T, o overlayTally) {
	t.Logf("layout overlays: %d replays (%d seeded per version, each on the converted layout and the template), %d ops, %d applied; dropped by reason: %s",
		o.replays-o.carried, randomOverlays, o.ops, o.applied, reasons(o.dropped))
	t.Logf("overlays carried to the next version of a pull request: %d replays of %d ops; dropped by reason: %s", o.carried, o.carriedOps, reasons(o.carriedDropped))
	t.Logf("ops of carried overlays (each counted once) whose tile the previous version had and the next does not: %d", o.vanished)
	t.Logf("carried overlays whose last move applied: %d, its relation holding once composed: %d", o.moves, o.held)
	t.Logf("overlay replays failing an invariant: %d", o.failures)
	t.Logf("ApplyOverlay and Compose per replay: p50 %v, p99 %v, max %v", percentile(o.durations, 50), percentile(o.durations, 99), percentile(o.durations, 100))
}

func reasons(dropped map[tiles.DropReason]int) string {
	values := map[string]int{}
	for reason, n := range dropped {
		values[string(reason)] = n
	}
	return counts(values)
}
