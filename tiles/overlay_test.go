package tiles_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// overlayOf is an overlay of one batch per op list.
func overlayOf(t *testing.T, batches ...[]tiles.Op) *tiles.Overlay {
	t.Helper()
	o := &tiles.Overlay{Schema: tiles.OverlayName}
	for i, ops := range batches {
		b := tiles.Batch{ID: fmt.Sprintf("edt_%012x", i+1), At: "2026-11-14T10:00:00Z", By: "chat", Ops: ops, Reset: ops == nil, Summary: []string{}}
		next, err := o.With(b)
		if err != nil {
			t.Fatal(err)
		}
		o = &next
	}
	return o
}

func move(tile string, to tiles.Place) tiles.Op {
	return tiles.Op{Op: tiles.OpMove, Tile: tile, To: &to}
}

// overlayView is what an overlay golden records: the replay's report and the
// composed layout, an entry per line.
type overlayView struct {
	Report  tiles.OverlayReport `json:"report"`
	Edited  bool                `json:"edited"`
	Entries []string            `json:"entries"`
	Groups  []tiles.Group       `json:"groups"`
	Landing int                 `json:"landing"`
}

func entryLines(layout tiles.Layout) []string {
	var out []string
	for _, e := range layout.Entries {
		line := e.Tile + " " + string(e.Size)
		if e.Group != "" {
			line += " [" + e.Group + "]"
		}
		if e.Pin {
			line += " pinned"
		}
		out = append(out, line)
	}
	return out
}

// applyChecked replays an overlay on a fixture's version, composes it and
// runs every generated-document check.
func applyChecked(t *testing.T, normalized []byte, doc *tiles.Document, c *tiles.Composition, o *tiles.Overlay) (tiles.Composition, tiles.OverlayReport, *tiles.Document) {
	t.Helper()
	before := mustJSON(t, doc)
	out, report := tiles.ApplyOverlay(doc, c, o, nil)
	composed, err := tiles.Compose(doc, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range tilestest.CheckGenerated(normalized, &out, composed) {
		t.Error(failure)
	}
	if !bytes.Equal(before, mustJSON(t, doc)) {
		t.Fatal("ApplyOverlay changed its input")
	}
	again, _ := tiles.ApplyOverlay(doc, c, o, nil)
	if !reflect.DeepEqual(out, again) {
		t.Error("replaying twice gave different compositions")
	}
	return out, report, composed
}

// Each operation, and each reason one is dropped, on the converted layout of
// a review with a changes group and an obsolete change.
func TestApplyOverlayGoldens(t *testing.T) {
	example := fixture{name: "example", example: true}
	normalized, doc := convert(t, example)
	five := 1
	for _, tc := range []struct {
		name string
		ops  []tiles.Op
	}{
		{"move-first", []tiles.Op{move("risks", tiles.Place{Edge: tiles.EdgeFirst})}},
		{"move-last", []tiles.Op{move("overview", tiles.Place{Edge: tiles.EdgeLast})}},
		{"move-before", []tiles.Op{move("evidence", tiles.Place{Before: "overview"})}},
		{"move-change-out-of-group", []tiles.Op{move("sec_docs", tiles.Place{Edge: tiles.EdgeFirst})}},
		{"move-change-within-group", []tiles.Op{move("sec_docs", tiles.Place{Before: "sec_api"})}},
		{"move-into-group-snaps-to-edge", []tiles.Op{move("context", tiles.Place{After: "sec_api"}), move("intent", tiles.Place{Before: "sec_docs"})}},
		{"resize", []tiles.Op{{Op: tiles.OpResize, Tile: "sec_api", Size: tiles.SizeFull}, {Op: tiles.OpResize, Tile: "evidence", Size: tiles.SizeHalf}}},
		{"hide-show", []tiles.Op{{Op: tiles.OpHide, Tile: "context"}, {Op: tiles.OpHide, Tile: "media"}, {Op: tiles.OpShow, Tile: "media"}}},
		{"pin", []tiles.Op{{Op: tiles.OpPin, Tile: "sec_retry"}, {Op: tiles.OpPin, Tile: "decisions"}, {Op: tiles.OpUnpin, Tile: "decisions"}}},
		{"changes-shown", []tiles.Op{{Op: tiles.OpChangesShown, Count: &five}}},
		{"move-shows-a-hidden-tile", []tiles.Op{{Op: tiles.OpHide, Tile: "intent"}, move("intent", tiles.Place{After: "risks"})}},
		{"dropped", []tiles.Op{
			move("sec_gone", tiles.Place{Edge: tiles.EdgeFirst}),
			move("evidence", tiles.Place{After: "sec_gone"}),
			move("sec_fixed_delay", tiles.Place{Edge: tiles.EdgeFirst}),
			move("evidence", tiles.Place{After: "sec_fixed_delay"}),
			{Op: tiles.OpResize, Tile: "overview", Size: tiles.SizeThird},
			{Op: tiles.OpHide, Tile: "overview"},
			{Op: tiles.OpHide, Tile: "sec_fixed_delay"},
			{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: "api_changes"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, report, composed := applyChecked(t, normalized, doc, nil, overlayOf(t, tc.ops))
			if report.Applied+report.DroppedTotal() != len(tc.ops) {
				t.Errorf("report %+v does not count %d ops", report, len(tc.ops))
			}
			goldentest.Golden(t, "testdata/overlays/"+tc.name+".json", overlayView{report, out.Layout.Edited, entryLines(composed.Layout), composed.Layout.Groups, composed.Layout.Landing})
		})
	}
}

// With no operation to apply, the replay gives the composition it was given,
// and so the served document is today's.
func TestApplyOverlayEmpty(t *testing.T) {
	normalized, doc := convert(t, fixture{name: "example", example: true})
	template, _ := tiles.Template(doc, nil)
	for _, o := range []*tiles.Overlay{nil, {Schema: tiles.OverlayName}, overlayOf(t, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}, nil)} {
		for _, c := range []*tiles.Composition{nil, &template} {
			out, report := tiles.ApplyOverlay(doc, c, o, nil)
			if report.Applied != 0 || out.Layout.Edited {
				t.Errorf("an overlay with nothing to apply applied %d ops", report.Applied)
			}
			want := tiles.Composition{}
			if c != nil {
				want = *c
			}
			if !bytes.Equal(mustJSON(t, out), mustJSON(t, want)) {
				t.Error("an overlay with nothing to apply changed the composition")
			}
			composed, err := tiles.Compose(doc, &out)
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range tilestest.CheckGenerated(normalized, &out, composed) {
				t.Error(failure)
			}
		}
	}
}

// An edited layout is the person's: the guard rails do not put the
// template's back, and a row they resized is not grown to fill.
func TestEditedLayoutIsThePersons(t *testing.T) {
	normalized, doc := convert(t, fixture{name: "example", example: true})
	template, _ := tiles.Template(doc, nil)
	// Every change to the end: the template's first screen shows changes,
	// so the guard would replace a composer's layout like this.
	var ops []tiles.Op
	for _, change := range []string{"sec_api", "sec_retry", "sec_deadletter", "sec_ackfix", "sec_docs"} {
		ops = append(ops, move(change, tiles.Place{Edge: tiles.EdgeLast}))
	}
	out, _, composed := applyChecked(t, normalized, doc, &template, overlayOf(t, ops))
	last := composed.Layout.Entries[len(composed.Layout.Entries)-2]
	if last.Tile != "sec_docs" {
		t.Errorf("the person's order was replaced: %v", entryLines(composed.Layout))
	}
	unedited := out
	unedited.Layout.Edited = false
	guarded, err := tiles.Compose(doc, &unedited)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(entryLines(guarded.Layout), entryLines(composed.Layout)) {
		t.Error("the guard rails would have kept this layout anyway; the test proves nothing")
	}

	// A third beside a half leaves a unit empty; the person chose it.
	resize := []tiles.Op{{Op: tiles.OpResize, Tile: "risks", Size: tiles.SizeThird}}
	if !slices.Contains(tiles.SizesIn(doc, *tileOf(doc, "risks")), tiles.SizeThird) {
		resize = []tiles.Op{{Op: tiles.OpResize, Tile: "evidence", Size: tiles.SizeHalf}}
	}
	_, report, composed := applyChecked(t, normalized, doc, &template, overlayOf(t, resize))
	if report.Applied != 1 {
		t.Fatalf("the resize did not apply: %+v", report)
	}
	for _, e := range composed.Layout.Entries {
		if e.Tile == resize[0].Tile && e.Size != resize[0].Size {
			t.Errorf("tile %s is %s, not the %s the person chose", e.Tile, e.Size, resize[0].Size)
		}
	}
}

// Random overlays on every fixture, on its converted layout, its template
// and random compositions, keep every guarantee of a generated document,
// deterministically; and an overlay drawn on one version and replayed on the
// next, which lacks some of its tiles, does too, dropping what names them.
func TestApplyOverlayRandom(t *testing.T) {
	all := append(slices.Clone(fixtures), exhibits, withManyBlocks())
	runs := 16
	if testing.Short() || tiles.RaceEnabled {
		runs = 3
	}
	for _, f := range all {
		t.Run(f.name, func(t *testing.T) {
			normalized, doc := convert(t, f)
			template, _ := tiles.Template(doc, nil)
			for seed := range runs {
				rng := rand.New(rand.NewPCG(uint64(seed), 2023))
				bases := []*tiles.Composition{nil, &template, tilestest.RandomComposition(rng, doc, seed%2 == 1)}
				for k, c := range bases {
					o := tilestest.RandomOverlay(rng, doc, 1+rng.IntN(8))
					ops := 0
					for _, b := range o.Active() {
						ops += len(b.Ops)
					}
					_, report, _ := applyChecked(t, normalized, doc, c, o)
					if report.Applied+report.DroppedTotal() != ops {
						t.Errorf("seed %d base %d: report %+v does not count %d ops", seed, k, report, ops)
					}
					if t.Failed() {
						t.Fatalf("seed %d base %d failed; overlay: %s", seed, k, mustJSON(t, o))
					}
				}
			}
		})
	}
	// Recorded on one version, replayed on the next.
	_, before := convert(t, exhibits)
	next := fixture{name: "next", mutate: func(doc map[string]any) {
		exhibits.mutate(doc)
		doc["unassigned_hunks"] = append(array(doc, "unassigned_hunks"), array(sectionOf(doc, "sec_config_flag"), "hunks")...)
		removeSection(doc, "sec_config_flag")
	}}
	normalized, after := convert(t, next)
	dropped := 0
	for seed := range runs {
		rng := rand.New(rand.NewPCG(uint64(seed), 7))
		o := tilestest.RandomOverlay(rng, before, 6)
		_, report, _ := applyChecked(t, normalized, after, nil, o)
		dropped += report.Dropped[tiles.DropGone]
	}
	if dropped == 0 {
		t.Error("no op on the vanished change was dropped; the test proves nothing")
	}
	o := overlayOf(t, []tiles.Op{{Op: tiles.OpHide, Tile: "sec_config_flag"}, move("risks", tiles.Place{Edge: tiles.EdgeFirst})})
	_, report, composed := applyChecked(t, normalized, after, nil, o)
	if report.Applied != 1 || report.Dropped[tiles.DropGone] != 1 || composed.Layout.Entries[0].Tile != "risks" {
		t.Errorf("replayed on the next version: %+v, %v", report, entryLines(composed.Layout))
	}
}

// Undo removes the newest batch; a reset drops what came before from the
// replay, and undoing it brings that back.
func TestOverlayUndoAndReset(t *testing.T) {
	normalized, doc := convert(t, fixture{name: "example", example: true})
	hide := []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}
	o := overlayOf(t, hide, nil)
	if o.Edited() {
		t.Error("a reset overlay is edited")
	}
	_, report, _ := applyChecked(t, normalized, doc, nil, o)
	if report.Applied != 0 {
		t.Errorf("a reset overlay applied %d ops", report.Applied)
	}
	undone, last, ok := o.Undo()
	if !ok || !last.Reset || !undone.Edited() {
		t.Fatalf("undoing the reset: %v %+v %v", ok, last, undone.Edited())
	}
	_, report, _ = applyChecked(t, normalized, doc, nil, &undone)
	if report.Applied != 1 {
		t.Errorf("after undoing the reset, %d ops applied", report.Applied)
	}
	empty, _, _ := undone.Undo()
	if empty.CanUndo() || empty.Edited() {
		t.Error("an overlay with no batch can undo or is edited")
	}
}

// Past MaxOverlayBatches the oldest batches fold into a base batch, which
// keeps the last of each kind of edit per tile and cannot be undone.
func TestOverlayFolds(t *testing.T) {
	o := &tiles.Overlay{Schema: tiles.OverlayName}
	for i := range tiles.MaxOverlayBatches + 5 {
		size := []tiles.Size{tiles.SizeFull, tiles.SizeHalf}[i%2]
		b := tiles.Batch{ID: fmt.Sprintf("edt_%012x", i+1), At: "2026-11-14T10:00:00Z", By: "page", Summary: []string{"x"}, Ops: []tiles.Op{
			{Op: tiles.OpResize, Tile: "context", Size: size},
			{Op: []string{tiles.OpHide, tiles.OpShow}[i%2], Tile: fmt.Sprintf("tile_%d", i%3)},
		}}
		next, err := o.With(b)
		if err != nil {
			t.Fatal(err)
		}
		o = &next
	}
	if len(o.Batches) != tiles.MaxOverlayBatches || o.Batches[0].ID != tiles.BaseBatch {
		t.Fatalf("%d batches, first %s", len(o.Batches), o.Batches[0].ID)
	}
	if err := tiles.CheckOverlay(o); err != nil {
		t.Fatal(err)
	}
	base := o.Batches[0]
	resizes := 0
	for _, op := range base.Ops {
		if op.Op == tiles.OpResize {
			resizes++
		}
	}
	if resizes != 1 || len(base.Ops) > 4 {
		t.Errorf("the base keeps %d resizes and %d ops: %+v", resizes, len(base.Ops), base.Ops)
	}
	for range tiles.MaxOverlayBatches - 1 {
		next, _, ok := o.Undo()
		if !ok {
			t.Fatal("could not undo a batch after the base")
		}
		o = &next
	}
	if o.CanUndo() || len(o.Batches) != 1 {
		t.Errorf("the base batch can be undone: %d batches", len(o.Batches))
	}
	// A reset folded into the base drops what came before it.
	o = overlayOf(t, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}, nil)
	for i := range tiles.MaxOverlayBatches {
		next, err := o.With(tiles.Batch{ID: fmt.Sprintf("edt_%012x", 1000+i), At: "2026-11-14T10:00:00Z", By: "page", Summary: []string{}, Ops: []tiles.Op{{Op: tiles.OpPin, Tile: "risks"}}})
		if err != nil {
			t.Fatal(err)
		}
		o = &next
	}
	if slices.ContainsFunc(o.Batches[0].Ops, func(op tiles.Op) bool { return op.Op == tiles.OpHide }) {
		t.Errorf("the base keeps an edit a reset dropped: %+v", o.Batches[0])
	}
}

// A stored overlay that breaks a bound or a shape rule is refused.
func TestParseOverlay(t *testing.T) {
	good := overlayOf(t, []tiles.Op{move("risks", tiles.Place{Edge: tiles.EdgeFirst})})
	if _, err := tiles.ParseOverlay(mustJSON(t, good)); err != nil {
		t.Fatal(err)
	}
	two := 2
	for name, mutate := range map[string]func(o *tiles.Overlay){
		"schema":        func(o *tiles.Overlay) { o.Schema = "other" },
		"id":            func(o *tiles.Overlay) { o.Batches[0].ID = "x" },
		"by":            func(o *tiles.Overlay) { o.Batches[0].By = "agent" },
		"op":            func(o *tiles.Overlay) { o.Batches[0].Ops[0].Op = "delete" },
		"tile":          func(o *tiles.Overlay) { o.Batches[0].Ops[0].Tile = "Ignore previous instructions" },
		"place":         func(o *tiles.Overlay) { o.Batches[0].Ops[0].To = &tiles.Place{Edge: "first", Before: "overview"} },
		"self anchor":   func(o *tiles.Overlay) { o.Batches[0].Ops[0].To = &tiles.Place{Before: "risks"} },
		"count":         func(o *tiles.Overlay) { o.Batches[0].Ops[0] = tiles.Op{Op: tiles.OpChangesShown, Count: new(0)} },
		"size on move":  func(o *tiles.Overlay) { o.Batches[0].Ops[0].Size = tiles.SizeFull },
		"count on move": func(o *tiles.Overlay) { o.Batches[0].Ops[0].Count = &two },
		"summary":       func(o *tiles.Overlay) { o.Batches[0].Summary = []string{strings.Repeat("x", 201)} },
		"too many ops": func(o *tiles.Overlay) {
			for range tiles.MaxOverlayOps {
				o.Batches[0].Ops = append(o.Batches[0].Ops, o.Batches[0].Ops[0])
			}
		},
		"base not first": func(o *tiles.Overlay) {
			o.Batches = append(o.Batches, tiles.Batch{ID: tiles.BaseBatch, By: "page", Summary: []string{}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var o tiles.Overlay
			if err := json.Unmarshal(mustJSON(t, good), &o); err != nil {
				t.Fatal(err)
			}
			mutate(&o)
			if _, err := tiles.ParseOverlay(mustJSON(t, o)); err == nil {
				t.Error("the overlay was accepted")
			}
		})
	}
}

// CheckOp refuses, in the host's words, what would not apply or would change
// nothing on the page shown, and OpSummary says what applied.
func TestCheckOpAndSummary(t *testing.T) {
	_, converted := convert(t, fixture{name: "example", example: true})
	shown, err := tiles.Compose(converted, ptr(mustApply(converted, overlayOf(t, []tiles.Op{{Op: tiles.OpHide, Tile: "media"}, {Op: tiles.OpPin, Tile: "decisions"}}))))
	if err != nil {
		t.Fatal(err)
	}
	severe := tiles.CheckOp(shown, tiles.Op{Op: tiles.OpHide, Tile: "risks"}) != ""
	for _, tc := range []struct {
		op      tiles.Op
		refusal string
		summary string
	}{
		{move("risks", tiles.Place{Edge: tiles.EdgeFirst}), "", "Moved Risks to the top"},
		{move("evidence", tiles.Place{Before: "overview"}), "", "Moved Evidence before " + tiles.TileTitle(shown, "overview")},
		{move("nothing_here", tiles.Place{Edge: tiles.EdgeFirst}), "No tile nothing_here on this review; tile_layout lists them", ""},
		{move("overview", tiles.Place{Edge: tiles.EdgeLast}), "The overview always stays on the landing page", ""},
		{move("sec_fixed_delay", tiles.Place{Edge: tiles.EdgeFirst}), "is a change an earlier version had", ""},
		{move("evidence", tiles.Place{After: "media"}), "is hidden; show it first", ""},
		{tiles.Op{Op: tiles.OpResize, Tile: "overview", Size: tiles.SizeThird}, "can be full, half, not third", ""},
		{tiles.Op{Op: tiles.OpResize, Tile: "evidence", Size: tiles.SizeFull}, "", "Made Evidence full width"},
		{tiles.Op{Op: tiles.OpResize, Tile: "overview", Size: tiles.SizeHalf}, "is already half width", ""},
		{tiles.Op{Op: tiles.OpHide, Tile: "overview"}, "The overview always stays on the landing page", ""},
		{tiles.Op{Op: tiles.OpHide, Tile: "context"}, "", "Removed Context"},
		{tiles.Op{Op: tiles.OpHide, Tile: "media"}, "Media is already hidden", ""},
		{tiles.Op{Op: tiles.OpShow, Tile: "media"}, "", "Brought back Media"},
		{tiles.Op{Op: tiles.OpShow, Tile: "context"}, "Context is not hidden", ""},
		{tiles.Op{Op: tiles.OpPin, Tile: "decisions"}, "Decisions is already pinned", ""},
		{tiles.Op{Op: tiles.OpUnpin, Tile: "decisions"}, "", "Unpinned Decisions"},
		{tiles.Op{Op: tiles.OpChangesShown, Count: ptr(2)}, "", "Showing 2 changes before folding"},
		{tiles.Op{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: "api_changes"}}, "not available yet", ""},
		{tiles.Op{Op: "delete", Tile: "risks"}, "Unknown op", ""},
	} {
		got := tiles.CheckOp(shown, tc.op)
		if tc.refusal == "" && got != "" || tc.refusal != "" && !strings.Contains(got, tc.refusal) {
			t.Errorf("CheckOp(%+v) = %q, want %q", tc.op, got, tc.refusal)
		}
		if tc.summary != "" {
			if summary := tiles.OpSummary(shown, tc.op); summary != tc.summary {
				t.Errorf("OpSummary(%+v) = %q, want %q", tc.op, summary, tc.summary)
			}
		}
	}
	if severe != (tiles.CheckOp(shown, move("risks", tiles.Place{Edge: tiles.EdgeLast})) != "") {
		t.Error("hiding and moving the risks tile off the landing page are not refused alike")
	}
}

func mustApply(doc *tiles.Document, o *tiles.Overlay) tiles.Composition {
	out, _ := tiles.ApplyOverlay(doc, nil, o, nil)
	return out
}

func ptr[T any](v T) *T { return &v }

// A person's edits are never stored: normalising, as a host does before it
// stores a composition, drops them.
func TestNormalizeDropsEdits(t *testing.T) {
	_, doc := convert(t, fixture{name: "example", example: true})
	edited := mustApply(doc, overlayOf(t, []tiles.Op{{Op: tiles.OpResize, Tile: "evidence", Size: tiles.SizeHalf}, {Op: tiles.OpHide, Tile: "context"}}))
	if !edited.Layout.Edited || len(edited.Layout.Resized) != 1 {
		t.Fatalf("the replay did not mark its edits: %+v", edited.Layout)
	}
	n, report := tiles.NormalizeComposition(doc, &edited)
	if n.Layout.Edited || len(n.Layout.Resized) > 0 || len(n.Layout.Hidden) > 0 {
		t.Errorf("normalising kept a person's edits: %+v", n.Layout)
	}
	if report.Counts()[tiles.FixHostOnly] != 1 {
		t.Errorf("fixes %v", report.Counts())
	}
}

// A change the recommendation names cannot be hidden or moved off the page:
// CheckOp refuses it in the host's words, and a replay drops such an op.
func TestOverlayKeepsRecommendedChange(t *testing.T) {
	_, plain := convert(t, withManyBlocks())
	var members []string
	for _, entry := range plain.Layout.Entries {
		if entry.Group == tiles.GroupChanges {
			members = append(members, entry.Tile)
		}
	}
	named := members[4]
	many := withManyBlocks()
	f := fixture{name: "recommended-kept", mutate: func(doc map[string]any) {
		many.mutate(doc)
		object(doc, "summary")["recommendation"] = map[string]any{"decision": "look_first", "text": "Read the last part first.", "refs": []any{named}}
	}}
	normalized, doc := convert(t, f)
	const reason = "baum's recommendation points at this change, so it stays on the page"
	for _, op := range []tiles.Op{{Op: tiles.OpHide, Tile: named}, move(named, tiles.Place{Edge: tiles.EdgeLast})} {
		if got := tiles.CheckOp(doc, op); got != reason {
			t.Errorf("CheckOp(%+v) = %q, want %q", op, got, reason)
		}
	}
	if got := tiles.CheckOp(doc, tiles.Op{Op: tiles.OpHide, Tile: members[0]}); got != "" {
		t.Errorf("hiding an unnamed change: %q", got)
	}
	ops := []tiles.Op{{Op: tiles.OpHide, Tile: named}, move(named, tiles.Place{Edge: tiles.EdgeLast}), {Op: tiles.OpHide, Tile: members[0]}}
	_, report, composed := applyChecked(t, normalized, doc, nil, overlayOf(t, ops))
	if report.Dropped[tiles.DropKept] != 2 || report.Applied != 1 {
		t.Errorf("report %+v, want 2 kept drops and 1 applied", report)
	}
	for _, entry := range composed.Layout.Entries {
		if entry.Tile == named && entry.Group == tiles.GroupHidden {
			t.Error("the recommended change is hidden")
		}
	}
	// A stored composition that hides it is served with it shown.
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Hidden: []string{named}}}
	shown, err := tiles.Compose(doc, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range shown.Layout.Entries {
		if entry.Tile == named && entry.Group == tiles.GroupHidden {
			t.Error("a composition hid the recommended change")
		}
	}
}
