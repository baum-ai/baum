package tileview

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

func decode(t *testing.T, r Response) map[string]any {
	t.Helper()
	body, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body.Body.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The answer is the normalised review and its view; the tile document it
// is checked against is never served.
func TestBuildServesTheReviewAndItsView(t *testing.T) {
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{})
	if r.View == nil || r.Problem != "" {
		t.Fatalf("view %v, problem %q", r.View != nil, r.Problem)
	}
	out := decode(t, r)
	if out["document"].(map[string]any)["schema"] != "baum.review/1" || out["view"].(map[string]any)["schema"] != view.Name {
		t.Fatalf("schemas %v", out)
	}
	for _, gone := range []string{"tiles", "added", "problem"} {
		if _, ok := out[gone]; ok {
			t.Errorf("the answer has %q", gone)
		}
	}
}

func TestBuildFailureLeavesTheViewOutWithoutContent(t *testing.T) {
	for _, document := range []string{
		`{"review":{"repository":"acme/widgets"},"secret":"PRIVATE_TEXT"}`,
		`{"schema":"baum.review/1","unknown_member":"PRIVATE_TEXT"}`,
		`not json PRIVATE_TEXT`,
	} {
		r := Build([]byte(document), nil, tiles.Options{})
		if r.View != nil || r.Problem == "" || strings.Contains(r.Problem, "PRIVATE_TEXT") {
			t.Errorf("%s: view %v, problem %q", document, r.View != nil, r.Problem)
		}
		body, err := Encode(r)
		if err != nil || !json.Valid(body.Body.Data) || strings.Contains(string(body.Body.Data), `"view"`) {
			t.Errorf("%s: %s %v", document, body.Body.Data, err)
		}
	}
}

func TestDraftOutlineBuilds(t *testing.T) {
	r := Build(tileviewtest.Encode(t, tileviewtest.DraftOutline(t)), nil, tiles.Options{Draft: true})
	if r.View == nil || r.Problem != "" {
		t.Fatalf("problem %q", r.Problem)
	}
}

// Warm compiles the checks once and is safe to repeat and to run beside
// requests.
func TestWarm(t *testing.T) {
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- Warm() }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{}); r.View == nil {
		t.Fatal(r.Problem)
	}
}

// The helper's draft converts as the tiles package's draft-outline golden does.
func TestDraftOutlineMatchesTheGolden(t *testing.T) {
	// The golden is converted from the normalised review, without the hunk
	// URLs Build derives.
	normalized, err := review.NormalizeStored(tileviewtest.Encode(t, tileviewtest.DraftOutline(t)))
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../../tiles/testdata/draft-outline.tiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("the draft helper no longer converts to the draft-outline golden")
	}
}

// With no composition, an empty one, or one of another schema, the answer
// is the empty view's, byte for byte.
func TestBuildWithoutAViewCompositionIsTheEmptyView(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	want, err := Encode(Build(raw, nil, tiles.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for name, composition := range map[string][]byte{
		"empty":      {},
		"null":       []byte("null"),
		"v1":         []byte(`{"schema":"baum.composition/1","layout":{"entries":[]}}`),
		"unreadable": []byte(`{"schema":`),
		"other":      []byte(`{"schema":"baum.composition/9","layout":{"entries":[]}}`),
		"not json":   []byte(`PRIVATE_TEXT`),
	} {
		got, err := Encode(Build(raw, composition, tiles.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Body.Data) != string(want.Body.Data) || got.Body.ETag != want.Body.ETag {
			t.Errorf("%s: the answer changed", name)
		}
	}
}

func ignoredCount() int64 { return workstats.Take().Counters["tileview.views_ignored"] }

// A review view composition that cannot be read is ignored and counted,
// without a problem for the reader and without quoting it.
func TestBuildIgnoresAnUnusableCompositionAndCountsIt(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	want, _ := Encode(Build(raw, nil, tiles.Options{}))
	for name, composition := range map[string][]byte{
		"unreadable": []byte(`{"schema":"baum.composition/2","sections":"PRIVATE_TEXT"}`),
		"oversized":  []byte(`{"schema":"baum.composition/2","x":"` + strings.Repeat("a", tiles.MaxStoredComposition) + `"}`),
	} {
		before := ignoredCount()
		r := Build(raw, composition, tiles.Options{})
		got, err := Encode(r)
		if err != nil || r.Problem != "" || string(got.Body.Data) != string(want.Body.Data) || ignoredCount() != before+1 {
			t.Errorf("%s: problem %q, changed %v, counted %d: %v", name, r.Problem, string(got.Body.Data) != string(want.Body.Data), ignoredCount()-before, err)
		}
	}
}

// A review view composition changes the view.
func TestBuildAppliesTheComposition(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	r := Build(raw, tileviewtest.ViewComposition(t), tiles.Options{})
	if r.View == nil || r.View.Overview.RiskSummary != tileviewtest.RiskSummary {
		t.Fatalf("view %+v", r.View)
	}
}

func TestKeyNamesTheComposition(t *testing.T) {
	a, b := []byte(`{"a":1}`), []byte(`{"a":2}`)
	if Key(nil) != "" || Key([]byte{}) != "" || Key(a) == "" || Key(a) == Key(b) || Key(a) != Key([]byte(`{"a":1}`)) {
		t.Error("keys do not name the composition's bytes")
	}
}

// addedAPI is the api_change library type added on section, its data
// citing hunk.
func addedAPI(t *testing.T, hunk, section string) AddedTile {
	t.Helper()
	entry, ok := library.Get("api_change")
	if !ok {
		t.Fatal("no api_change type")
	}
	data := json.RawMessage(`{"endpoints":[],"docs":null,"consumers":[{"label":"x","updated":true,"cites":[{"type":"hunk","id":"` + hunk + `"}]}]}`)
	return AddedTile{Tile: tiles.LibraryTile{Definition: entry.Encoded, Data: data}, Section: section}
}

// The tiles a person added are served in the view, marked added, on the
// section they were added on, with the version their data is from; an
// added tile whose type the composition already fills is shown once, as
// the composition has it. AddedKey names them.
func TestBuildServesAddedTiles(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	normalized, _ := review.NormalizeStored(raw)
	r, err := view.ParseReview(normalized)
	if err != nil {
		t.Fatal(err)
	}
	first := r.Sections[0].Hunks[0]
	added := addedAPI(t, first, "context")
	added.From, added.Filling = 2, true
	got := BuildWith(raw, nil, []AddedTile{added}, tiles.Options{})
	if got.View == nil || len(got.View.Tiles) != 1 {
		t.Fatalf("view %+v", got.View)
	}
	tile := got.View.Tiles[0]
	if tile.ID != "api_change" || !tile.Added || tile.From != 2 || !tile.Filling || tile.State != tiles.StateReady || !strings.Contains(string(tile.Definition), `"bytes"`) {
		t.Errorf("added tile %+v", tile)
	}
	if got.View.Sections[0].ID != view.ContextSection || !reflect.DeepEqual(got.View.Sections[0].Tiles, []string{"api_change"}) {
		t.Errorf("sections %+v", got.View.Sections)
	}
	// Without a section, or on one the version no longer has, it goes where
	// its hunks are.
	for _, section := range []string{"", "sec_gone"} {
		got = BuildWith(raw, nil, []AddedTile{addedAPI(t, first, section)}, tiles.Options{})
		if got.View.Sections[1].ID != r.Sections[0].ID || !has(got.View.Sections[1].Tiles, "api_change") {
			t.Errorf("section %q: sections %+v", section, got.View.Sections)
		}
	}
	// The composition's own api_change wins.
	document := viewDocument(t)
	with := BuildWith(document, viewComposition(t, document), []AddedTile{addedAPI(t, first, "context")}, tiles.Options{})
	n := 0
	for _, tile := range with.View.Tiles {
		if tile.ID == "api_change" {
			n++
			if tile.Added {
				t.Error("the composition's tile is marked added")
			}
		}
	}
	if n != 1 {
		t.Errorf("api_change served %d times", n)
	}
	if AddedKey(nil) != "" || AddedKey([]AddedTile{added}) == "" || AddedKey([]AddedTile{added}) == AddedKey([]AddedTile{addedAPI(t, first, "context")}) {
		t.Error("AddedKey does not name the added tiles")
	}
}

func has(ids []string, id string) bool {
	for _, have := range ids {
		if have == id {
			return true
		}
	}
	return false
}
