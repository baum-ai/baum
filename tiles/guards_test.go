package tiles_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// withGaps is a fixture whose evidence has at least three gaps in current
// changes.
func withGaps(name string) fixture {
	base := fixtures[slices.IndexFunc(fixtures, func(f fixture) bool { return f.name == name })]
	return fixture{name: name + "-gaps", opts: base.opts, mutate: func(doc map[string]any) {
		if base.mutate != nil {
			base.mutate(doc)
		}
		// Every evidence item takes the shape of the first missing one,
		// keeping its own ID and claim, until three are missing.
		var missing map[string]any
		var items []map[string]any
		for _, s := range array(doc, "sections") {
			for _, e := range array(s.(map[string]any), "evidence") {
				item := e.(map[string]any)
				if missing == nil && item["status"] == "missing" {
					missing = item
				}
				items = append(items, item)
			}
		}
		n := 0
		for _, item := range items {
			if item["status"] == "missing" {
				n++
			}
		}
		for _, item := range items {
			if n >= 3 || item["status"] == "missing" {
				continue
			}
			for key, value := range missing {
				if key != "id" && key != "claim" {
					item[key] = value
				}
			}
			for key := range item {
				if _, ok := missing[key]; !ok && key != "id" && key != "claim" {
					delete(item, key)
				}
			}
			n++
		}
	}}
}

func entryOf(doc *tiles.Document, id string) (int, tiles.Entry) {
	i := slices.IndexFunc(doc.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == id })
	if i < 0 {
		return -1, tiles.Entry{}
	}
	return i, doc.Layout.Entries[i]
}

// exampleLayout lays the example review's changes out after the overview
// and risks: those in front outside the group at their sizes, the rest in
// the changes group, then the other tiles.
func exampleLayout(front []tiles.Entry, rest ...string) *tiles.Composition {
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
		{Tile: tiles.TypeOverview, Size: tiles.SizeHalf}, {Tile: tiles.TypeRisks, Size: tiles.SizeHalf},
	}}}
	c.Layout.Entries = append(c.Layout.Entries, front...)
	for _, id := range rest {
		c.Layout.Entries = append(c.Layout.Entries, tiles.Entry{Tile: id, Size: tiles.SizeThird, Group: tiles.GroupChanges})
	}
	return c
}

// A layout that shows fewer changes on the first screen than the template,
// with nothing that must be seen instead, takes the template's layout.
func TestGuardKeepsTheTemplatesChangesOnTheFirstScreen(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
		{Tile: tiles.TypeOverview, Size: tiles.SizeFull}, {Tile: tiles.TypeContext, Size: tiles.SizeFull}, {Tile: tiles.TypeIntent, Size: tiles.SizeFull},
	}}}
	n, report, _ := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixFirstScreen] != 1 {
		t.Fatalf("fixes %v", report.Messages())
	}
	template, _ := tiles.Template(doc, nil)
	if !slices.Equal(n.Layout.Entries, template.Layout.Entries) {
		t.Errorf("entries %v, want the template's %v", n.Layout.Entries, template.Layout.Entries)
	}

	// The same layout with the context tile pinned is the person's: kept.
	c.Layout.Entries[1].Pin = true
	n, report, _ = checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixFirstScreen] != 0 || !slices.ContainsFunc(n.Layout.Entries, func(e tiles.Entry) bool { return e.Pin }) {
		t.Errorf("a pinned layout was replaced: %v", report.Messages())
	}
}

// A change featured alone at half, empty units beside it, takes the full row.
func TestGuardWidensALoneFeaturedChange(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "example"))
	// The person pinned the context tile, so the layout is theirs; the
	// evidence (with gaps) is on the landing page, and the change featured
	// alone on the row after it still takes the row.
	c := exampleLayout([]tiles.Entry{
		{Tile: tiles.TypeContext, Size: tiles.SizeFull, Pin: true}, {Tile: tiles.TypeEvidence, Size: tiles.SizeFull},
		{Tile: "sec_ackfix", Size: tiles.SizeHalf},
	}, "sec_api", "sec_retry", "sec_deadletter", "sec_docs")
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixLoneFeatured] != 1 || report.Counts()[tiles.FixFirstScreen] != 0 {
		t.Fatalf("fixes %v", report.Messages())
	}
	if _, entry := entryOf(composed, "sec_ackfix"); entry.Size != tiles.SizeFull {
		t.Errorf("the lone change is %s; layout %v", entry.Size, n.Layout.Entries)
	}
}

// Evidence with three gaps or more is never behind "Show more": the template
// puts it beside the lead change on the second row, and a composition that
// pushes it past the landing page has it moved up.
func TestGuardKeepsEvidenceWithGapsOnTheLandingPage(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	template, _ := tiles.Template(doc, nil)
	composedTemplate, err := tiles.Compose(doc, &template)
	if err != nil {
		t.Fatal(err)
	}
	rows := tiles.Rows(composedTemplate.Layout)
	second := []string{}
	for _, i := range rows[1].Entries {
		second = append(second, composedTemplate.Layout.Entries[i].Tile)
	}
	if !slices.Contains(second, tiles.TypeEvidence) || len(second) != 2 {
		t.Errorf("the template's second row is %v", second)
	}

	c := exampleLayout([]tiles.Entry{
		{Tile: "sec_api", Size: tiles.SizeThird}, {Tile: "sec_retry", Size: tiles.SizeThird}, {Tile: "sec_deadletter", Size: tiles.SizeThird},
		{Tile: tiles.TypeContext, Size: tiles.SizeFull}, {Tile: tiles.TypeIntent, Size: tiles.SizeFull},
	}, "sec_ackfix", "sec_docs")
	_, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixEvidenceGaps] != 1 {
		t.Fatalf("fixes %v", report.Messages())
	}
	if i, _ := entryOf(composed, tiles.TypeEvidence); i < 0 || !tiles.Visible(composed.Layout)[i] {
		t.Errorf("the evidence tile is not on the landing page: %v", composed.Layout.Entries)
	}
}

// A pull request with one current change has no changes group in its
// template: the change stands at half beside the next tile. (The base
// review's second change is made obsolete in the converted document.)
func TestTemplateOfOneChangeHasNoGroup(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "base"))
	var current []string
	for _, entry := range doc.Layout.Entries {
		if entry.Group == tiles.GroupChanges {
			current = append(current, entry.Tile)
		}
	}
	trimmed := *doc
	trimmed.Layout.Entries = slices.DeleteFunc(slices.Clone(doc.Layout.Entries), func(e tiles.Entry) bool { return e.Tile == current[1] })
	trimmed.Layout.Entries = append(trimmed.Layout.Entries, tiles.Entry{Tile: current[1], Size: tiles.SizeThird, Group: tiles.GroupObsolete})
	template, _ := tiles.Template(&trimmed, nil)
	for _, entry := range template.Layout.Entries {
		if entry.Tile == current[0] && (entry.Group != "" || entry.Size == tiles.SizeThird) {
			t.Errorf("the only change is %+v", entry)
		}
	}
}

// A docs or CI pull request with a risk above low keeps the risks tile on the
// landing page, beside the overview.
func TestDocsTemplateKeepsActiveRisksOnTheLandingPage(t *testing.T) {
	for _, kind := range []string{"docs", "ci"} {
		normalized, doc := convert(t, withKind(fixtureNamed(t, "example"), kind))
		template, _ := tiles.Template(doc, nil)
		_, _, composed := checkComposed(t, normalized, doc, &template)
		if i, entry := entryOf(composed, tiles.TypeRisks); i != 1 || entry.Size != tiles.SizeHalf || !tiles.Visible(composed.Layout)[i] {
			t.Errorf("%s: the risks tile is %+v at %d", kind, entry, i)
		}
	}
}

// What must be seen on the template's first screen stays on the
// composition's: evidence with gaps beside the lead change on the second row
// is not pushed down to the third, even with as many changes shown.
func TestGuardKeepsTheTemplatesMustSeeTilesOnTheFirstScreen(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	template, _ := tiles.Template(doc, nil)
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Entries: slices.Clone(template.Layout.Entries)}}
	// Swap the evidence tile with the first tile of the third row.
	composed, err := tiles.Compose(doc, &template)
	if err != nil {
		t.Fatal(err)
	}
	rows := tiles.Rows(composed.Layout)
	third := composed.Layout.Entries[rows[2].Entries[0]].Tile
	at := slices.IndexFunc(c.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == tiles.TypeEvidence })
	to := slices.IndexFunc(c.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == third })
	c.Layout.Entries[at].Tile, c.Layout.Entries[to].Tile = c.Layout.Entries[to].Tile, c.Layout.Entries[at].Tile
	n, report, _ := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixFirstScreen] != 1 || !slices.Equal(n.Layout.Entries, template.Layout.Entries) {
		t.Errorf("fixes %v; entries %v", report.Messages(), n.Layout.Entries)
	}
}

// When pinned tiles and the overview fill the landing page's own rows, the
// only room left is in the rows that authored tiles add. Moving the evidence
// in front of those tiles would push them, and the rows they add, off the
// landing page, so the evidence with gaps takes the place of a change shown
// there instead, and the change takes the evidence's.
func TestGuardShowsEvidenceInTheRowsAuthoredTilesAdd(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	c := &tiles.Composition{
		Authored: []tiles.AuthoredTile{
			tilestest.AuthoredTile("prose_lead", "h_000000000001"),
			tilestest.AuthoredTile("prose_aside", "h_000000000002"),
		},
		Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
			{Tile: "prose_lead", Size: tiles.SizeFull},
			{Tile: "sec_retry_policy", Size: tiles.SizeThird},
			{Tile: "prose_aside", Size: tiles.SizeHalf},
			{Tile: "sec_config_flag", Size: tiles.SizeFull, Pin: true},
			{Tile: tiles.TypeOverview, Size: tiles.SizeFull},
			{Tile: tiles.TypeContext, Size: tiles.SizeFull},
			{Tile: tiles.TypeIntent, Size: tiles.SizeThird, Pin: true},
			{Tile: tiles.TypeEvidence, Size: tiles.SizeThird},
			{Tile: tiles.TypeRisks, Size: tiles.SizeHalf, Pin: true},
		}},
	}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixEvidenceGaps] != 1 {
		t.Fatalf("fixes %v", report.Messages())
	}
	visible := tiles.Visible(composed.Layout)
	for _, id := range []string{tiles.TypeEvidence, "prose_lead", "prose_aside", tiles.TypeOverview} {
		if i, _ := entryOf(composed, id); i < 0 || !visible[i] {
			t.Errorf("tile %s is not on the landing page: %v", id, n.Layout.Entries)
		}
	}
	if i, _ := entryOf(composed, "sec_retry_policy"); i < 0 || visible[i] {
		t.Errorf("the change the evidence displaces is still on the landing page: %v", n.Layout.Entries)
	}
}

// A layout that leaves the evidence with gaps out has it placed after
// everything it lists; the guard moves it from there onto the landing page.
func TestGuardMovesUnlistedEvidenceOntoTheLandingPage(t *testing.T) {
	normalized, doc := convert(t, withGaps("example"))
	c := &tiles.Composition{Layout: tiles.CompositionLayout{Entries: []tiles.Entry{
		{Tile: tiles.TypeOverview, Size: tiles.SizeHalf}, {Tile: tiles.TypeRisks, Size: tiles.SizeHalf},
		{Tile: "sec_retry_policy", Size: tiles.SizeHalf}, {Tile: "sec_config_flag", Size: tiles.SizeHalf},
		{Tile: tiles.TypeIntent, Size: tiles.SizeFull}, {Tile: tiles.TypeContext, Size: tiles.SizeFull},
		{Tile: tiles.TypeMedia, Size: tiles.SizeFull}, {Tile: tiles.TypeDecisions, Size: tiles.SizeFull},
	}}}
	n, report, composed := checkComposed(t, normalized, doc, c)
	if report.Counts()[tiles.FixEvidenceGaps] != 1 {
		t.Fatalf("fixes %v", report.Messages())
	}
	if i, _ := entryOf(composed, tiles.TypeEvidence); i < 0 || !tiles.Visible(composed.Layout)[i] {
		t.Errorf("the evidence tile is not on the landing page: %v", n.Layout.Entries)
	}
}
