package tileview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

func servedDocument(t *testing.T, s Served) reviewv2.Document {
	t.Helper()
	if s.Problem != "" {
		t.Fatalf("problem %q", s.Problem)
	}
	var d reviewv2.Document
	if err := json.Unmarshal(s.Document, &d); err != nil {
		t.Fatal(err)
	}
	if d.Schema != reviewv2.Name {
		t.Fatalf("schema %q", d.Schema)
	}
	return d
}

func servedTile(d reviewv2.Document, id string) *reviewv2.Tile {
	for i := range d.Tiles {
		if d.Tiles[i].ID == id {
			return &d.Tiles[i]
		}
	}
	return nil
}

// A baum.review/1 version is served as baum.review/2: its composition's
// tiles checked and placed as the review page's view places them, each
// custom definition without its HTML, and no view beside it.
func TestServeConvertsV1(t *testing.T) {
	document := viewDocument(t)
	s := Serve(document, viewComposition(t, document), nil, tiles.Options{})
	d := servedDocument(t, s)
	if bytes.Contains(s.Document, []byte(`"view"`)) {
		t.Fatal("the answer still has a view")
	}
	answer, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(answer.Body.Data, &body); err != nil || body["view"] != nil || body["document"] == nil {
		t.Fatalf("answer keys %v", body)
	}
	if err := schema.ValidateReview2(s.Document); err != nil {
		t.Fatalf("the served document does not validate: %v", err)
	}
	want := map[string]string{"dg_delivery": reviewv2.OwnerOverview, "dg_flag": "sec_config_flag", "schema_change": "sec_retry_policy", "api_change": "sec_retry_policy", "table_retry_states": "sec_retry_policy", "code_sync_backoff": "sec_retry_policy"}
	for id, owner := range want {
		tile := servedTile(d, id)
		if tile == nil || tile.Owner != owner {
			t.Errorf("tile %s = %+v, want owner %s", id, tile, owner)
		}
	}
	schemaChange := servedTile(d, "schema_change")
	if schemaChange == nil || schemaChange.Type != "schema_change" || schemaChange.State != reviewv2.TileReady || bytes.Contains(schemaChange.Definition, []byte(`"html":`)) {
		t.Fatalf("schema_change = %+v", schemaChange)
	}
	if d.Summary.RiskWhy == "" || d.Overview.Diagram == nil || *d.Overview.Diagram != "dg_delivery" || len(d.Overview.Screenshots) != 1 {
		t.Fatalf("overview %+v, risk_why %q", d.Overview, d.Summary.RiskWhy)
	}
	if len(s.Frames) == 0 {
		t.Fatal("no frames registered")
	}
}

// A baum.review/2 version is served as stored, with the added tiles merged
// into its tiles: marked added, with from and filling, owned where they were
// added or by the hunks they cite.
func TestServeV2WithAddedTiles(t *testing.T) {
	stored, err := os.ReadFile(filepath.Join("..", "..", "reviewv2", "testdata", "v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	added := addedAPI(t, "h_a00000000002", "")
	added.From, added.Filling = 1, true
	d := servedDocument(t, Serve(stored, nil, []AddedTile{added}, tiles.Options{}))
	if len(d.Tiles) != 4 {
		t.Fatalf("tiles %+v", d.Tiles)
	}
	tile := servedTile(d, "api_change")
	if tile == nil || !tile.Added || tile.From != 1 || !tile.Filling || tile.Owner != "sec_delivery_errors" || tile.Type != "api_change" || tile.State != reviewv2.TileReady {
		t.Fatalf("added tile = %+v", tile)
	}
	if bytes.Contains(tile.Definition, []byte(`"html":`)) {
		t.Fatal("the added tile's definition holds its HTML")
	}
	// On context when added there; once only when the document has its type.
	d = servedDocument(t, Serve(stored, nil, []AddedTile{addedAPI(t, "h_a00000000002", "context")}, tiles.Options{}))
	if tile := servedTile(d, "api_change"); tile == nil || tile.Owner != reviewv2.OwnerContext {
		t.Fatalf("added tile = %+v", tile)
	}
	var doc reviewv2.Document
	if err := json.Unmarshal(stored, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Tiles[2].Type = "api_change"
	again, _ := json.Marshal(doc)
	d = servedDocument(t, Serve(again, nil, []AddedTile{added}, tiles.Options{}))
	if len(d.Tiles) != 3 {
		t.Fatalf("a type the document has was served twice: %d tiles", len(d.Tiles))
	}
	// Without added tiles the document is served as stored, with its hunks'
	// links derived.
	d = servedDocument(t, Serve(stored, nil, nil, tiles.Options{}))
	if len(d.Tiles) != 3 || !bytes.Contains(mustJSON(t, d.Index), []byte("github_url")) {
		t.Fatalf("served %d tiles", len(d.Tiles))
	}
}

// An added tile cites hunks of a stale section no more than of a current
// one: it is owned by a current section, else context.
func TestServeV2AddedTileSkipsStaleSections(t *testing.T) {
	stored, err := os.ReadFile(filepath.Join("..", "..", "reviewv2", "testdata", "v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc reviewv2.Document
	if err := json.Unmarshal(stored, &doc); err != nil {
		t.Fatal(err)
	}
	for i := range doc.Sections {
		if doc.Sections[i].ID == "sec_delivery_errors" {
			doc.Sections[i].Derived = json.RawMessage(`{"state":"stale"}`)
		}
	}
	stale, _ := json.Marshal(doc)
	for _, section := range []string{"", "sec_delivery_errors"} {
		d := servedDocument(t, Serve(stale, nil, []AddedTile{addedAPI(t, "h_a00000000002", section)}, tiles.Options{}))
		if tile := servedTile(d, "api_change"); tile == nil || tile.Owner == "sec_delivery_errors" {
			t.Errorf("added on %q: tile %+v on a stale section", section, tile)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
