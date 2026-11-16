package tileview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// viewSession is a linked session for the base review, which has none.
const viewSession = `{"id": "785faefe-40b5-5521-9932-4dc2d659ec1d", "vendor": "acme-agent", "models": ["acme-model-1"], "title": "Add retries", "started_at": "2026-09-08T08:20:00Z", "ended_at": "2026-09-09T15:00:00Z", "outcome": "committed", "prompt_count": 1, "tool_calls": 5, "restricted": false, "prompts": [{"index": 1, "id": "389a1221-117c-5a53-8ac6-f1e573239425", "at": "2026-09-08T08:20:00Z", "text": "Retry failed deliveries with a backoff, and add a flag to turn it off."}]}`

// viewDocument is the base review with a linked session.
func viewDocument(t *testing.T) []byte {
	t.Helper()
	doc := tileviewtest.Base(t)
	var s any
	if err := json.Unmarshal([]byte(viewSession), &s); err != nil {
		t.Fatal(err)
	}
	doc["index"].(map[string]any)["sessions"] = []any{s}
	return tileviewtest.Encode(t, doc)
}

// viewComposition is a review view composition of the document as a
// generation stores it: the view call's overview, two diagrams and a
// placement, the fixture's library and authored tiles (two of each), checked
// against the review, and the tiles the call left out placed by their hunks.
func viewComposition(t *testing.T, document []byte) []byte {
	t.Helper()
	grid, err := tiles.ParseComposition(tileviewtest.LibraryComposition(t, document))
	if err != nil {
		t.Fatal(err)
	}
	reply := `{"overview": {"risk_summary": "Retries change how every failed delivery behaves. A flag turns them off.",
	  "verdict": {"text": "Decide the retry budget before merging.", "points": [{"text": "The budget has no upper bound, so a down remote is retried forever. Set one in the policy.", "section": "sec_retry_policy"}]},
	  "screenshots": ["att_aa01"], "diagram": "dg_delivery",
	  "intent": [{"asked": "Retries with a backoff", "got": "Yes: failed deliveries retry with a doubling wait.", "session": "785faefe-40b5-5521-9932-4dc2d659ec1d", "prompt": 1}]},
	 "sections": [{"id": "sec_config_flag", "tiles": ["dg_flag"]}, {"id": "sec_retry_policy", "tiles": ["schema_change"]}],
	 "diagrams": [
	  {"id": "dg_delivery", "type": "flow", "title": "What happens to a failed delivery", "caption": "The flag skips the retry lane.", "source": ["h_000000000001", "h_000000000003"],
	   "lanes": [{"label": "Retries on", "steps": [{"title": "Deliver", "note": null, "tone": null, "join": null}, {"title": "Remote fails", "note": null, "tone": "bad", "join": "arrow"}, {"title": "Wait and retry", "note": "the wait doubles", "tone": "new", "join": "arrow"}]},
	             {"label": "Retries off", "steps": [{"title": "Deliver", "note": null, "tone": null, "join": null}, {"title": "Remote fails", "note": null, "tone": "bad", "join": "arrow"}, {"title": "Reported at once", "note": null, "tone": "muted", "join": "arrow"}]}]},
	  {"id": "dg_flag", "type": "columns", "title": "What the flag changes", "caption": null, "source": ["h_000000000003"],
	   "columns": [{"label": "Flag on", "tone": "good", "items": [{"title": "Failed deliveries retry", "note": "up to the budget"}]}, {"label": "Flag off", "tone": "muted", "items": [{"title": "A failure is reported at once", "note": null}]}]}]}`
	parsed, problems := view.ParseReply([]byte(reply))
	if problems != nil {
		t.Fatal(problems)
	}
	var report view.Report
	c := parsed.Composition(&report)
	c.Library, c.Authored = grid.Library, grid.Authored
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	n, checked := view.Normalize(r, doc, &c)
	if checked.Total() != 0 {
		t.Fatalf("the fixture's composition is fixed: %v %v", checked.Counts(), checked.Problems)
	}
	view.Place(r, &n, view.ExtraIDs(&n), view.Covers(view.ExtraTiles(doc, &n)))
	n.Generator = &tiles.Generator{PromptVersion: "fixture", Model: "fixture-model", ComposedAt: "2026-11-16T00:00:00Z"}
	encoded, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// The served view of a review with a review view composition: the golden
// is the shape the app reads (testdata/view-example.json).
func TestBuildServesTheView(t *testing.T) {
	document := viewDocument(t)
	r := Build(document, viewComposition(t, document), tiles.Options{})
	if r.View == nil || r.Tiles == nil || r.Problem != "" {
		t.Fatalf("view %v tiles %v problem %q", r.View != nil, r.Tiles != nil, r.Problem)
	}
	v := r.View
	if v.Overview.Diagram == nil || *v.Overview.Diagram != "dg_delivery" || v.Overview.Verdict == nil || len(v.Sections) != 4 {
		t.Fatalf("view %+v", v)
	}
	kinds := map[string]string{}
	for _, tile := range v.Tiles {
		kinds[tile.ID] = tile.Type
		if tile.Type == tiles.TypeCustom && !strings.Contains(string(tile.Definition), `"bytes"`) {
			t.Errorf("%s: the definition is not served without its HTML", tile.ID)
		}
	}
	want := map[string]string{"dg_delivery": "flow", "dg_flag": "columns", "schema_change": "custom", "api_change": "custom", "table_retry_states": "table", "code_sync_backoff": "code"}
	if len(kinds) != len(want) {
		t.Fatalf("tiles %v", kinds)
	}
	for id, kind := range want {
		if kinds[id] != kind {
			t.Errorf("%s: %q, want %q", id, kinds[id], kind)
		}
	}
	// Every tile is placed once: on the overview or a section.
	placed := map[string]int{*v.Overview.Diagram: 1}
	for _, p := range v.Sections {
		for _, id := range p.Tiles {
			placed[id]++
		}
	}
	for id := range want {
		if placed[id] != 1 {
			t.Errorf("%s placed %d times", id, placed[id])
		}
	}
	// The grid still serves the template's layout with the tiles.
	if r.Tiles.Origin.Kind != tiles.OriginGenerated {
		t.Errorf("the grid's origin %q", r.Tiles.Origin.Kind)
	}
	goldentest.Golden(t, "testdata/view-example.json", v)
}

// A version without a composition, or with a baum.composition/1, gets the
// empty view: every section, no tiles. A draft's pending library tile is
// pending in the view too.
func TestBuildServesAnEmptyView(t *testing.T) {
	document := viewDocument(t)
	for name, composition := range map[string][]byte{
		"none": nil,
		"v1":   tileviewtest.LibraryComposition(t, document),
	} {
		r := Build(document, composition, tiles.Options{})
		if r.View == nil || len(r.View.Tiles) != 0 || len(r.View.Sections) != 4 || r.View.Overview.Verdict != nil {
			t.Errorf("%s: %+v", name, r.View)
		}
		encoded, _ := json.Marshal(r.View)
		if !strings.Contains(string(encoded), `"schema":"baum.view/1"`) || !strings.Contains(string(encoded), `"intent":[]`) {
			t.Errorf("%s: %s", name, encoded)
		}
	}
	grid, err := tiles.ParseComposition(tileviewtest.LibraryComposition(t, document))
	if err != nil {
		t.Fatal(err)
	}
	pending := grid.Library[0]
	pending.Data, pending.Pending = json.RawMessage(`{}`), true
	draft, err := json.Marshal(view.Composition{Schema: view.CompositionName, Overview: view.Overview{Screenshots: []string{}, Intent: []view.Intent{}},
		Sections: []view.Placement{}, Library: []tiles.LibraryTile{pending}})
	if err != nil {
		t.Fatal(err)
	}
	r := Build(document, draft, tiles.Options{Draft: true})
	if r.View == nil || len(r.View.Tiles) != 1 || r.View.Tiles[0].State != tiles.StatePending || r.View.Sections[0].Tiles[0] != r.View.Tiles[0].ID {
		t.Fatalf("draft view %+v", r.View)
	}
}
