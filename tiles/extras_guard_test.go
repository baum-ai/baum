package tiles_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// Hunks of the example review's changes: sec_deadletter (a medium risk) and
// sec_docs (low risks only).
const (
	deadletterHunk = "h_113c5f55c94f"
	docsHunk       = "h_938fb177e9e6"
)

// wideTable is an authored table of four columns, whose content takes the
// full row.
func wideTable(id, hunk string) tiles.AuthoredTile {
	return tiles.AuthoredTile{ID: id, Title: "Synthetic wide table", Data: json.RawMessage(fmt.Sprintf(
		`{"columns":["a","b","c","d"],"rows":[["1","2","3","4"]],"source":[{"type":"hunk","id":%q}]}`, hunk))}
}

// rowsOf names each row's tiles, for messages and checks.
func rowsOf(doc *tiles.Document) [][]string {
	var out [][]string
	for _, row := range tiles.Rows(doc.Layout) {
		var ids []string
		for _, i := range row.Entries {
			entry := doc.Layout.Entries[i]
			ids = append(ids, entry.Tile+"/"+string(entry.Size))
		}
		out = append(out, ids)
	}
	return out
}

// onFirstScreen reports whether a composed document's first two rows hold a
// tile.
func onFirstScreen(doc *tiles.Document, id string) bool {
	for k, row := range rowsOf(doc) {
		if k >= tiles.FirstScreenRows {
			break
		}
		if slices.ContainsFunc(row, func(s string) bool { return strings.HasPrefix(s, id+"/") }) {
			return true
		}
	}
	return false
}

func indexOf(entries []tiles.Entry, id string) int {
	return slices.IndexFunc(entries, func(e tiles.Entry) bool { return e.Tile == id })
}

// The templates place a library tile that does not lead after the risks and
// evidence tiles, and an authored tile after those and the changes, so
// neither takes a place on the first screen.
func TestTemplatesPlaceSupportingTilesAfterRisksAndEvidence(t *testing.T) {
	for _, kind := range []string{"feature", "refactor", "dependency", "docs"} {
		normalized, doc := convert(t, withKind(fixtureNamed(t, "example"), kind))
		content := &tiles.Composition{Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", docsHunk)},
			Authored: []tiles.AuthoredTile{tilestest.AuthoredTile("table_delays", docsHunk)}}
		template, _ := tiles.Template(doc, content)
		_, report, composed := checkComposed(t, normalized, doc, &template)
		entries := template.Layout.Entries
		lastChange := -1
		for i, e := range entries {
			if strings.HasPrefix(e.Tile, "sec_") {
				lastChange = i
			}
		}
		library, authored := indexOf(entries, "retry_budget"), indexOf(entries, "table_delays")
		core := max(indexOf(entries, tiles.TypeRisks), indexOf(entries, tiles.TypeEvidence))
		if library < core || authored < core || authored < lastChange {
			t.Errorf("%s: library at %d, authored at %d, risks and evidence up to %d, changes up to %d: %v", kind, library, authored, core, lastChange, rowsOf(composed))
		}
		if onFirstScreen(composed, "retry_budget") || onFirstScreen(composed, "table_delays") {
			t.Errorf("%s: a supporting tile is on the first screen: %v", kind, rowsOf(composed))
		}
		if report.Counts()[tiles.FixExtraPlace] != 0 {
			t.Errorf("%s: the template needed a fix: %v", kind, report.Messages())
		}
	}
}

// A library tile that does not lead and takes the second row from the lead
// change and the evidence moves down to just after the evidence, at the
// size its content needs.
func TestGuardMovesASupportingTileOffTheSecondRow(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	c := &tiles.Composition{
		Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", docsHunk)},
		Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
			{Tile: tiles.TypeOverview, Size: tiles.SizeHalf}, {Tile: tiles.TypeRisks, Size: tiles.SizeHalf},
			{Tile: "retry_budget", Size: tiles.SizeFull},
			{Tile: "sec_api", Size: tiles.SizeHalf}, {Tile: tiles.TypeEvidence, Size: tiles.SizeHalf},
			{Tile: "sec_retry", Size: tiles.SizeThird, Group: tiles.GroupChanges}, {Tile: "sec_deadletter", Size: tiles.SizeThird, Group: tiles.GroupChanges},
			{Tile: "sec_ackfix", Size: tiles.SizeThird, Group: tiles.GroupChanges}, {Tile: "sec_docs", Size: tiles.SizeThird, Group: tiles.GroupChanges},
		}},
	}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixExtraPlace] != 1 || report.Counts()[tiles.FixFirstScreen] != 0 {
		t.Fatalf("fixes %v", report.Messages())
	}
	if indexOf(n.Layout.Entries, "retry_budget") != indexOf(n.Layout.Entries, tiles.TypeEvidence)+1 || onFirstScreen(composed, "retry_budget") {
		t.Errorf("rows %v", rowsOf(composed))
	}
	for _, id := range []string{"sec_api", tiles.TypeEvidence} {
		if !onFirstScreen(composed, id) {
			t.Errorf("%s is not on the first screen: %v", id, rowsOf(composed))
		}
	}
}

// A supporting tile that pushes the change it supports off the first screen
// moves beside that change, the two at half, when that change stands
// outside the group: in place of a minor change, not of a core tile.
func TestGuardPutsASupportingTileBesideItsChange(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	c := &tiles.Composition{
		Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", deadletterHunk)},
		Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
			{Tile: tiles.TypeRisks, Size: tiles.SizeHalf}, {Tile: tiles.TypeEvidence, Size: tiles.SizeHalf},
			{Tile: "retry_budget", Size: tiles.SizeFull},
			{Tile: "sec_deadletter", Size: tiles.SizeHalf}, {Tile: "sec_docs", Size: tiles.SizeHalf},
			{Tile: tiles.TypeOverview, Size: tiles.SizeHalf},
			{Tile: "sec_api", Size: tiles.SizeThird, Group: tiles.GroupChanges}, {Tile: "sec_retry", Size: tiles.SizeThird, Group: tiles.GroupChanges},
			{Tile: "sec_ackfix", Size: tiles.SizeThird, Group: tiles.GroupChanges},
		}},
	}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixExtraPlace] != 1 || report.Counts()[tiles.FixFirstScreen] != 0 {
		t.Fatalf("fixes %v; rows %v", report.Messages(), rowsOf(composed))
	}
	at := indexOf(n.Layout.Entries, "retry_budget")
	if at < 1 || n.Layout.Entries[at-1].Tile != "sec_deadletter" || n.Layout.Entries[at].Size != tiles.SizeHalf || n.Layout.Entries[at-1].Size != tiles.SizeHalf ||
		!onFirstScreen(composed, "sec_deadletter") || !onFirstScreen(composed, "retry_budget") {
		t.Errorf("rows %v", rowsOf(composed))
	}
}

// A supporting tile that pushes no core tile off the first screen stays
// where the composer put it, and so does one a person pinned.
func TestGuardLeavesSupportingTilesThatPushNothing(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	content := &tiles.Composition{Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", docsHunk)}}
	template, _ := tiles.Template(doc, content)
	c := template
	c.Generator = nil
	// The library tile last: it pushes nothing.
	at := indexOf(c.Layout.Entries, "retry_budget")
	c.Layout.Entries = append(slices.Delete(slices.Clone(c.Layout.Entries), at, at+1), c.Layout.Entries[at])
	if _, report, _ := checkComposed(t, normalized, doc, &c); report.Counts()[tiles.FixExtraPlace] != 0 {
		t.Errorf("a tile that pushes nothing moved: %v", report.Messages())
	}
	// Pinned first, it is the person's.
	pinned := c
	pinned.Layout.Entries = slices.Clone(c.Layout.Entries)
	last := len(pinned.Layout.Entries) - 1
	pinned.Layout.Entries[last].Pin = true
	n, report, composed := checkComposed(t, normalized, doc, &pinned)
	if report.Counts()[tiles.FixExtraPlace] != 0 || !onFirstScreen(composed, "retry_budget") {
		t.Errorf("a pinned tile moved: %v; %v", report.Messages(), n.Layout.Entries)
	}
}

// When the evidence must take the place of a tile on the landing page (the
// landing page's own rows hold pinned tiles and the overview, and the room
// left is in the rows authored tiles add), a tile that is not a current
// change gives way before a change does, even when the change comes later.
func TestEvidenceSwapPrefersATileThatIsNotAChange(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	c := &tiles.Composition{
		Authored: []tiles.AuthoredTile{wideTable("table_lead", "h_000000000001"), tilestest.AuthoredTile("prose_aside", "h_000000000002")},
		Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
			{Tile: "table_lead", Size: tiles.SizeFull},
			{Tile: tiles.TypeContext, Size: tiles.SizeThird},
			{Tile: "sec_retry_policy", Size: tiles.SizeThird},
			{Tile: "prose_aside", Size: tiles.SizeThird},
			{Tile: "sec_config_flag", Size: tiles.SizeFull, Pin: true},
			{Tile: tiles.TypeOverview, Size: tiles.SizeFull},
			{Tile: tiles.TypeIntent, Size: tiles.SizeThird, Pin: true},
			{Tile: tiles.TypeEvidence, Size: tiles.SizeThird},
			{Tile: tiles.TypeRisks, Size: tiles.SizeHalf, Pin: true},
		}},
	}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixEvidenceGaps] != 1 {
		t.Fatalf("fixes %v; rows %v", report.Messages(), rowsOf(composed))
	}
	visible := tiles.Visible(composed.Layout)
	shown := func(id string) bool { i, _ := entryOf(composed, id); return i >= 0 && visible[i] }
	if !shown(tiles.TypeEvidence) || !shown("sec_retry_policy") || shown(tiles.TypeContext) {
		t.Errorf("the evidence displaced the wrong tile: %v; rows %v", n.Layout.Entries, rowsOf(composed))
	}
}

// When every tile the landing page shows must be seen (pinned, the overview,
// the severe risks), the evidence has no tile to swap with: nothing moves,
// and the page still holds its invariants.
func TestEvidenceWithNoSwapCandidateStays(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
		{Tile: "sec_config_flag", Size: tiles.SizeFull, Pin: true},
		{Tile: "sec_retry_policy", Size: tiles.SizeFull, Pin: true},
		{Tile: tiles.TypeRisks, Size: tiles.SizeHalf, Pin: true},
		{Tile: tiles.TypeOverview, Size: tiles.SizeHalf},
		{Tile: tiles.TypeContext, Size: tiles.SizeFull},
		{Tile: tiles.TypeEvidence, Size: tiles.SizeThird},
	}}}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixEvidenceGaps] != 0 {
		t.Errorf("fixes %v", report.Messages())
	}
	if i, _ := entryOf(composed, tiles.TypeEvidence); i < 0 || tiles.Visible(composed.Layout)[i] {
		t.Errorf("rows %v; %v", rowsOf(composed), n.Layout.Entries)
	}
}

// A library tile listing one item and a narrow authored table that the
// layout gives a full row each share one row at half; a supporting tile
// alone on its row keeps the full row, which it would grow back to.
func TestGuardSizesSupportingTilesByContent(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	content := &tiles.Composition{Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", docsHunk)},
		Authored: []tiles.AuthoredTile{tilestest.AuthoredTile("table_delays", docsHunk)}}
	template, _ := tiles.Template(doc, content)
	c := template
	c.Generator = nil
	c.Layout.Entries = slices.DeleteFunc(slices.Clone(template.Layout.Entries), func(e tiles.Entry) bool {
		return e.Tile == "retry_budget" || e.Tile == "table_delays"
	})
	c.Layout.Entries = append(c.Layout.Entries, tiles.Entry{Tile: "retry_budget", Size: tiles.SizeFull}, tiles.Entry{Tile: "table_delays", Size: tiles.SizeFull})
	n, report, composed := checkComposed(t, normalized, doc, &c)
	if report.Counts()[tiles.FixExtraSize] != 2 {
		t.Fatalf("fixes %v; rows %v", report.Messages(), rowsOf(composed))
	}
	last := rowsOf(composed)[len(rowsOf(composed))-1]
	if !slices.Equal(last, []string{"retry_budget/half", "table_delays/half"}) {
		t.Errorf("rows %v; %v", rowsOf(composed), n.Layout.Entries)
	}
	// The library tile alone at the end keeps its full row.
	c.Layout.Entries = c.Layout.Entries[:len(c.Layout.Entries)-1]
	c.Authored = nil
	c.Layout.Entries = slices.DeleteFunc(c.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == "table_delays" })
	if _, report, composed := checkComposed(t, normalized, doc, &c); report.Counts()[tiles.FixExtraSize] != 0 {
		t.Errorf("a lone supporting tile shrank: %v; rows %v", report.Messages(), rowsOf(composed))
	}
}
