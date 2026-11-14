package compose

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// riskHunk returns a risk of the example and a hunk it is anchored on.
func riskHunk(t *testing.T, doc *tiles.Document) (tiles.Risk, string) {
	t.Helper()
	for _, risk := range doc.Risks {
		var anchors []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal(risk.Anchors, &anchors)
		for _, a := range anchors {
			if a.Type == "hunk" {
				return risk, a.ID
			}
		}
	}
	t.Fatal("the example has no risk anchored on a hunk")
	return tiles.Risk{}, ""
}

// schemaData is schema change data with one destructive column, cited on
// hunk.
func schemaData(hunk string, destructive bool) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"engine":"postgres","migrations":[],"tables":[{"name":"widgets","change":"altered","renamed_from":null,"columns":[{"name":"legacy","change":"removed","before":"TEXT","after":null,"destructive":%t,"note":null}],"indexes":[],"cites":[{"type":"hunk","id":%q}]}],"data_changes":[],"readers":[]}`, destructive, hunk))
}

// The inventory lists the ready fills with what they list and whether they
// lead, and the authored tiles with what they add; the template it carries
// places both.
func TestInventoryCarriesFillsAndAuthored(t *testing.T) {
	_, doc := example(t)
	_, hunk := riskHunk(t, doc)
	entry, _ := library.Get("schema_change")
	fill := FillOf(entry, []string{hunk}, "drops a column", "ready", library.WithRelated(schemaData(hunk, true), doc))
	authored := Authored{Tile: tiles.AuthoredTile{ID: "table_versions", Title: "Client versions", Data: json.RawMessage(fmt.Sprintf(
		`{"columns":["Client","Works"],"rows":[["desktop","yes"],["cli","no"]],"source":[{"type":"hunk","id":%q}]}`, hunk))}, Adds: "Which clients keep working."}
	inv, err := BuildInventory(doc, []Fill{fill}, nil, Preferences{}, authored)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Library) != 1 || !inv.Library[0].Leads || inv.Library[0].Items["tables"] != 1 || inv.Library[0].Items["related"] != 0 {
		t.Errorf("library facts: %+v", inv.Library)
	}
	if len(inv.Authored) != 1 || inv.Authored[0].Adds != "Which clients keep working." || inv.Authored[0].Rows != 2 || inv.Authored[0].Type != tiles.TypeTable {
		t.Errorf("authored facts: %+v", inv.Authored)
	}
	var placed []string
	for _, e := range inv.Template.Entries {
		placed = append(placed, e.Tile)
	}
	if !slices.Contains(placed, "schema_change") || !slices.Contains(placed, "table_versions") {
		t.Errorf("the template does not place the extras: %v", placed)
	}
	encoded, err := inv.Encode()
	if err != nil || !strings.Contains(string(encoded), `"destructive_or_breaking":true`) || !strings.Contains(string(encoded), `"authored":[`) {
		t.Errorf("encoded inventory: %v", err)
	}
}

// Related lists the risks anchored on the hunks a fill cites.
func TestWithRelated(t *testing.T) {
	_, doc := example(t)
	risk, hunk := riskHunk(t, doc)
	data := library.WithRelated(schemaData(hunk, false), doc)
	if !strings.Contains(string(data), fmt.Sprintf(`{"type":"risk","id":%q}`, risk.ID)) {
		t.Errorf("related: %s", data)
	}
	if got := library.WithRelated(schemaData("h_000000000000", false), doc); strings.Contains(string(got), "related") {
		t.Errorf("data citing no item's hunk got related items: %s", got)
	}
}

// An authored tile that restates the review is a duplicate; one with points
// of its own is not.
func TestDuplicatesTile(t *testing.T) {
	_, doc := example(t)
	risk, hunk := riskHunk(t, doc)
	restating := tiles.AuthoredTile{ID: "prose_again", Title: "Note", Data: json.RawMessage(fmt.Sprintf(`{"text":%q,"anchors":[{"type":"hunk","id":%q}]}`, risk.Text, hunk))}
	if !DuplicatesTile(doc, nil, restating) {
		t.Errorf("a restated risk is not a duplicate: %q", risk.Text)
	}
	own := tiles.AuthoredTile{ID: "table_matrix", Title: "Client matrix", Data: json.RawMessage(fmt.Sprintf(
		`{"columns":["Client","Version","Works"],"rows":[["desktop app","seven point two","yes after the upgrade"],["command line","three point one","no until it updates"]],"source":[{"type":"hunk","id":%q}]}`, hunk))}
	if DuplicatesTile(doc, nil, own) {
		t.Error("a table of its own is a duplicate")
	}
	units := DocumentUnits(doc)
	units = append(units, TileUnits(tiles.Tile{ID: "prose_again", Data: restating.Data})...)
	if pairs := Duplicates(units, map[string]bool{"prose_again": true}); len(pairs) == 0 {
		t.Error("the duplication metric misses a restated risk")
	}
}

// A composer that leaves the extras out gets them placed: a library tile
// that leads right after the overview and risks, another before the changes
// group, an authored tile after it.
func TestPlaceExtras(t *testing.T) {
	_, doc := example(t)
	_, hunk := riskHunk(t, doc)
	template, _ := tiles.Template(doc, nil)
	entry, _ := library.Get("schema_change")
	api, _ := library.Get("api_change")
	c := template
	c.Library = []tiles.LibraryTile{{Definition: api.Encoded, Data: json.RawMessage(`{}`), Pending: true}, {Definition: entry.Encoded, Data: schemaData(hunk, true)}}
	c.Authored = []tiles.AuthoredTile{{ID: "table_matrix", Title: "Matrix"}}
	tiles.PlaceExtras(&c, doc)
	order := map[string]int{}
	first, last := -1, -1
	for i, e := range c.Layout.Entries {
		order[e.Tile] = i
		if e.Group == tiles.GroupChanges {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if order["schema_change"] != order["risks"]+1 || order["api_change"] != first-1 || order["table_matrix"] != last+1 {
		t.Errorf("placed: %v (group %d-%d)", c.Layout.Entries, first, last)
	}
	again := c
	again.Layout.Entries = slices.Clone(c.Layout.Entries)
	tiles.PlaceExtras(&again, doc)
	if !slices.Equal(again.Layout.Entries, c.Layout.Entries) {
		t.Error("placing twice moved tiles")
	}
	n, report := tiles.NormalizeComposition(doc, &c)
	if report.Counts()[tiles.FixAppended] != 0 {
		t.Errorf("normalisation appended tiles after PlaceExtras: %v", report.Messages())
	}
	if len(n.Library) != 2 {
		t.Errorf("library tiles kept: %d", len(n.Library))
	}
}
