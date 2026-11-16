package view_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

func exampleDocument(t *testing.T) *tiles.Document {
	t.Helper()
	normalized, err := review.NormalizeStored(schema.Example)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

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

// Related lists the risks anchored on the hunks a fill cites.
func TestWithRelated(t *testing.T) {
	doc := exampleDocument(t)
	risk, hunk := riskHunk(t, doc)
	data := func(hunk string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"engine":"postgres","migrations":[],"tables":[{"name":"widgets","change":"altered","renamed_from":null,"columns":[{"name":"legacy","change":"removed","before":"TEXT","after":null,"destructive":false,"note":null}],"indexes":[],"cites":[{"type":"hunk","id":%q}]}],"data_changes":[],"readers":[]}`, hunk))
	}
	if got := library.WithRelated(data(hunk), doc); !strings.Contains(string(got), fmt.Sprintf(`{"type":"risk","id":%q}`, risk.ID)) {
		t.Errorf("related: %s", got)
	}
	if got := library.WithRelated(data("h_000000000000"), doc); strings.Contains(string(got), "related") {
		t.Errorf("data citing no item's hunk got related items: %s", got)
	}
}

// An authored tile that restates the review is a duplicate; one with points
// of its own is not.
func TestDuplicatesTile(t *testing.T) {
	doc := exampleDocument(t)
	risk, hunk := riskHunk(t, doc)
	restating := tiles.AuthoredTile{ID: "prose_again", Title: "Note", Data: json.RawMessage(fmt.Sprintf(`{"text":%q,"anchors":[{"type":"hunk","id":%q}]}`, risk.Text, hunk))}
	if !view.DuplicatesTile(doc, nil, restating) {
		t.Errorf("a restated risk is not a duplicate: %q", risk.Text)
	}
	own := tiles.AuthoredTile{ID: "table_matrix", Title: "Client matrix", Data: json.RawMessage(fmt.Sprintf(
		`{"columns":["Client","Version","Works"],"rows":[["desktop app","seven point two","yes after the upgrade"],["command line","three point one","no until it updates"]],"source":[{"type":"hunk","id":%q}]}`, hunk))}
	if view.DuplicatesTile(doc, nil, own) {
		t.Error("a table of its own is a duplicate")
	}
	units := view.DocumentUnits(doc)
	units = append(units, view.TileUnits(tiles.Tile{ID: "prose_again", Data: restating.Data})...)
	if pairs := view.Duplicates(units, map[string]bool{"prose_again": true}); len(pairs) == 0 {
		t.Error("the duplication metric misses a restated risk")
	}
}
