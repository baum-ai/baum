package view_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// session is a linked session for the base review, which has none.
const session = `{"id": "785faefe-40b5-5521-9932-4dc2d659ec1d", "vendor": "acme-agent", "models": ["acme-model-1"], "title": "Add retries", "started_at": "2026-09-08T08:20:00Z", "ended_at": "2026-09-09T15:00:00Z", "outcome": "committed", "prompt_count": 2, "tool_calls": 5, "restricted": false, "prompts": [{"index": 1, "id": "389a1221-117c-5a53-8ac6-f1e573239425", "at": "2026-09-08T08:20:00Z", "text": "Retry failed deliveries with a backoff."}, {"index": 2, "id": "b7656325-4a15-5b3b-bbb4-4b2eb86f9a3d", "at": "2026-09-08T11:40:00Z", "text": "And a flag to turn it off."}]}`

const sessionID = "785faefe-40b5-5521-9932-4dc2d659ec1d"

// fixture is the base review with a linked session, normalised as stored,
// read for the view and converted to tiles.
func fixture(t *testing.T) (*view.Review, *tiles.Document, []byte) {
	t.Helper()
	doc := tileviewtest.Base(t)
	var s any
	if err := json.Unmarshal([]byte(session), &s); err != nil {
		t.Fatal(err)
	}
	doc["index"].(map[string]any)["sessions"] = []any{s}
	normalized, err := review.NormalizeStored(tileviewtest.Encode(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r, converted, normalized
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// libraryTile is a library type's first sample, citing hunk.
func libraryTile(t *testing.T, name, hunk string) tiles.LibraryTile {
	t.Helper()
	entry, ok := library.Get(name)
	if !ok {
		t.Fatalf("no %s type", name)
	}
	var value map[string]any
	if err := json.Unmarshal(entry.Samples[0].Data, &value); err != nil {
		t.Fatal(err)
	}
	delete(value, library.Related)
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			if v["type"] == "hunk" {
				v["id"] = hunk
				delete(v, "lines")
				return
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	return tiles.LibraryTile{Definition: entry.Encoded, Data: raw(t, value)}
}

func TestReadsTheReview(t *testing.T) {
	r, _, _ := fixture(t)
	if r.Title == "" || r.Risk == nil || r.Risk.Score != 4 || len(r.Context) != 1 || r.Context[0].Name != "area" {
		t.Fatalf("summary %+v", r)
	}
	ids := []string{}
	for _, s := range r.Sections {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"sec_retry_policy", "sec_config_flag", "sec_generated_client"}) {
		t.Fatalf("sections %v", ids)
	}
	retry := r.Section("sec_retry_policy")
	if len(retry.Steps) == 0 || len(retry.Risks) != 2 || retry.Risks[0].Tier != "high" || !slices.Contains(retry.Hunks, "h_000000000001") {
		t.Fatalf("retry %+v", retry)
	}
	if len(r.Attachments) != 1 || r.Attachments[0].Origin != "description" || len(r.Sessions) != 1 || len(r.Sessions[0].Prompts) != 2 {
		t.Fatalf("index %+v %+v", r.Attachments, r.Sessions)
	}
	if !r.HasHunk("h_000000000007") || r.HasHunk("h_00000000000f") {
		t.Fatal("hunks")
	}
}

func TestNormalizeDropsOnlyWhatBreaksARule(t *testing.T) {
	r, doc, _ := fixture(t)
	hunk := view.Anchor{Type: "hunk", ID: "h_000000000001"}
	steps := []view.FlowStep{{Title: "Delivery fails"}, {Title: "Wait and retry", Tone: "new", Join: "arrow"}}
	good := view.NativeTile{ID: "dg_retry", Type: view.TypeFlow, Title: "What happens to a failed delivery",
		Data: raw(t, view.FlowData{Source: []view.Anchor{hunk, {Type: "hunk", ID: "h_0000000000ff"}, {Type: "file", ID: "x"}}, Lanes: []view.Lane{{Steps: steps}}})}
	long := view.NativeTile{ID: "dg_long", Type: view.TypeColumns, Title: "**Before** and after",
		Data: raw(t, view.ColumnsData{Source: []view.Anchor{}, Columns: []view.Column{
			{Label: "Before", Items: []view.ColumnItem{{Title: "One try"}}},
			{Label: "After", Tone: "sparkly", Items: []view.ColumnItem{{Title: strings.Repeat("retried again ", 10)}}}}})}
	c := &view.Composition{
		Overview: view.Overview{
			RiskSummary: "Retries can pile up when the remote is down. The budget bounds them.",
			Verdict: &view.Verdict{Text: "Decide the retry budget first.", Points: []view.Point{
				{Text: "The budget is unbounded.", Section: "sec_retry_policy"}, {Text: "Point two.", Section: "sec_nope"},
				{Text: "Point three."}, {Text: "Point four."}}},
			Screenshots: []string{"att_aa01", "att_aa01", "att_ff"},
			Diagram:     ptr("dg_retry"),
			Intent: []view.Intent{
				{Asked: "Retries", Got: "Yes: with a backoff.", Session: sessionID, Prompt: 1},
				{Asked: "Unknown", Got: "No.", Session: "nope", Prompt: 1},
				{Asked: "A prompt it lacks", Got: "No.", Session: sessionID, Prompt: 9}},
		},
		Sections: []view.Placement{
			{ID: "sec_retry_policy", Tiles: []string{"dg_retry", "dg_long"}},
			{ID: "sec_nope", Tiles: []string{"dg_seq"}},
			{ID: "context", Tiles: []string{"dg_seq", "dg_missing"}},
		},
		Diagrams: []view.NativeTile{good, long,
			{ID: "bad id", Type: view.TypeFlow, Title: "X", Data: good.Data},
			{ID: "dg_hunky", Type: view.TypeFlow, Title: "See h_000000000001", Data: good.Data},
			{ID: "dg_seq", Type: view.TypeSequence, Title: "Who calls whom", Data: raw(t, view.SequenceData{Source: []view.Anchor{}, Actors: []string{"Worker", "Remote"},
				Steps: []view.SequenceStep{{From: 0, To: 1, Text: "deliver"}, {From: 1, To: 1, Text: "fails"}}})},
			{ID: "dg_badseq", Type: view.TypeSequence, Title: "Too far", Data: raw(t, view.SequenceData{Source: []view.Anchor{}, Actors: []string{"A", "B"},
				Steps: []view.SequenceStep{{From: 0, To: 2, Text: "nobody"}}})},
			{ID: "dg_table", Type: view.TypeTable, Title: "Waits", Data: raw(t, view.TableData{Source: []view.Anchor{}, Columns: []string{"Try", "Wait"},
				Rows: [][]view.Cell{{{Text: "1"}, {Text: "1 s"}}, {{Text: "2"}}, {{Text: "3"}, {Text: "4 s"}}}})},
		},
	}
	n, report := view.Normalize(r, doc, c)
	got := []string{}
	for _, d := range n.Diagrams {
		got = append(got, d.ID)
	}
	if !slices.Equal(got, []string{"dg_retry", "dg_long", "dg_seq", "dg_table"}) {
		t.Fatalf("diagrams %v: %v", got, report.Problems)
	}
	var f view.FlowData
	_ = json.Unmarshal(n.Diagrams[0].Data, &f)
	if len(f.Source) != 1 || f.Source[0].ID != "h_000000000001" || f.Lanes[0].Steps[1].Join != "" || f.Lanes[0].Steps[1].Tone != "new" {
		t.Errorf("flow %+v", f)
	}
	var cols view.ColumnsData
	_ = json.Unmarshal(n.Diagrams[1].Data, &cols)
	if n.Diagrams[1].Title != "Before and after" || cols.Columns[1].Tone != "" || len([]rune(cols.Columns[1].Items[0].Title)) > view.MaxItemTitle ||
		!strings.HasSuffix(cols.Columns[1].Items[0].Title, "…") {
		t.Errorf("columns %q %+v", n.Diagrams[1].Title, cols)
	}
	var table view.TableData
	_ = json.Unmarshal(n.Diagrams[3].Data, &table)
	if len(table.Rows) != 2 {
		t.Errorf("table rows %v", table.Rows)
	}
	o := n.Overview
	if o.Verdict == nil || len(o.Verdict.Points) != 3 || o.Verdict.Points[0].Section != "sec_retry_policy" || o.Verdict.Points[1].Section != "" {
		t.Errorf("verdict %+v", o.Verdict)
	}
	if !slices.Equal(o.Screenshots, []string{"att_aa01"}) || o.Diagram == nil || *o.Diagram != "dg_retry" || len(o.Intent) != 1 || o.RiskSummary == "" {
		t.Errorf("overview %+v", o)
	}
	// The overview's diagram is placed there, so not on its section; an
	// unknown section and tile are dropped, and a tile is placed once.
	want := []view.Placement{{ID: "context", Tiles: []string{"dg_seq"}}, {ID: "sec_retry_policy", Tiles: []string{"dg_long"}}}
	if !reflect.DeepEqual(n.Sections, want) {
		t.Errorf("sections %+v", n.Sections)
	}
	counts := report.Counts()
	for _, kind := range []tiles.FixKind{view.FixNative, view.FixAnchor, view.FixCut, view.FixOverview, view.FixPlacement, view.FixLimit, view.FixNativeItem} {
		if counts[kind] == 0 {
			t.Errorf("no %s fix: %v", kind, counts)
		}
	}
	// A fixed point.
	again, second := view.Normalize(r, doc, &n)
	if second.Total() != 0 || !reflect.DeepEqual(again, n) {
		t.Errorf("normalising again: %v %v\n%+v\n%+v", second.Counts(), second.Problems, again, n)
	}
}

func ptr(s string) *string { return &s }

func TestNormalizeChecksLibraryAndAuthoredTiles(t *testing.T) {
	r, doc, _ := fixture(t)
	c := &view.Composition{
		Library: []tiles.LibraryTile{libraryTile(t, "schema_change", "h_000000000003"), {Definition: json.RawMessage(`{"name": "Bad!"}`), Data: json.RawMessage(`{}`)}},
		Authored: []tiles.AuthoredTile{{ID: "prose_why", Title: "Why", Data: json.RawMessage(`{"text": "Because.", "anchors": [{"type": "hunk", "id": "h_000000000001"}]}`)},
			{ID: "nonsense", Title: "X", Data: json.RawMessage(`{}`)}},
		Sections: []view.Placement{{ID: "sec_config_flag", Tiles: []string{"schema_change"}}},
	}
	n, report := view.Normalize(r, doc, c)
	if len(n.Library) != 1 || len(n.Authored) != 1 || report.Counts()[tiles.FixLibraryInvalid] != 1 || report.Counts()[tiles.FixAuthoredInvalid] != 1 {
		t.Fatalf("library %d authored %d fixes %v", len(n.Library), len(n.Authored), report.Counts())
	}
	if !slices.Equal(view.ExtraIDs(&n), []string{"schema_change", "prose_why"}) || len(n.Sections) != 1 {
		t.Fatalf("ids %v sections %v", view.ExtraIDs(&n), n.Sections)
	}
	// A review that does not convert keeps neither.
	if none, report := view.Normalize(r, nil, c); len(none.Library) != 0 || report.Counts()[view.FixUnusable] != 1 {
		t.Fatalf("without a document: %v", report.Counts())
	}
}

func TestPlacePutsATileWhereItsHunksAre(t *testing.T) {
	r, doc, _ := fixture(t)
	c := &view.Composition{
		// h_000000000002 is in both retry and config: the first wins; h_3 is
		// only config's.
		Library: []tiles.LibraryTile{libraryTile(t, "schema_change", "h_000000000003"), libraryTile(t, "api_change", "h_000000000002")},
		Authored: []tiles.AuthoredTile{{ID: "prose_why", Title: "Why", Data: json.RawMessage(`{"text": "Because.", "anchors": [{"type": "file", "path": "nowhere.go"}]}`)},
			{ID: "table_x", Title: "X", Data: json.RawMessage(`{"columns": ["A"], "rows": [["a"]], "source": [{"type": "hunk", "id": "h_000000000005"}]}`)}},
	}
	n, report := view.Normalize(r, doc, c)
	if len(n.Library) != 2 {
		t.Fatalf("library %v", report.Problems)
	}
	extras := view.ExtraTiles(doc, &n)
	if len(extras) != len(view.ExtraIDs(&n)) {
		t.Fatalf("extras %d, ids %v", len(extras), view.ExtraIDs(&n))
	}
	placed := view.Place(r, &n, view.ExtraIDs(&n), view.Covers(extras))
	where := map[string]string{}
	for _, p := range n.Sections {
		for _, id := range p.Tiles {
			where[id] = p.ID
		}
	}
	want := map[string]string{"schema_change": "sec_config_flag", "api_change": "sec_retry_policy", "table_x": "sec_generated_client"}
	if len(n.Authored) == 2 {
		// A tile that covers no hunk goes on context.
		want["prose_why"] = "context"
	}
	if placed != len(want) || !reflect.DeepEqual(where, want) {
		t.Fatalf("placed %d: %v, want %v", placed, where, want)
	}
	// Placing again places nothing.
	if again := view.Place(r, &n, view.ExtraIDs(&n), view.Covers(extras)); again != 0 {
		t.Fatalf("placed %d again", again)
	}
}

// A tile the person added goes on the section it was added on, when the
// review has that current section or it is context; else by its hunks.
func TestBuildPlacesAddedTilesOnTheirSection(t *testing.T) {
	r, doc, _ := fixture(t)
	extras := view.ExtraTiles(doc, &view.Composition{Library: []tiles.LibraryTile{
		libraryTile(t, "schema_change", "h_000000000003"), libraryTile(t, "api_change", "h_000000000003"),
	}})
	if len(extras) != 2 {
		t.Fatalf("extras %+v", extras)
	}
	for _, tc := range []struct {
		on   map[string]string
		want map[string]string
	}{
		{map[string]string{"schema_change": "context", "api_change": "sec_retry_policy"}, map[string]string{"schema_change": "context", "api_change": "sec_retry_policy"}},
		{map[string]string{"schema_change": "sec_gone", "api_change": ""}, map[string]string{"schema_change": "sec_config_flag", "api_change": "sec_config_flag"}},
	} {
		v := view.Build(r, nil, extras, tc.on)
		where := map[string]string{}
		for _, p := range v.Sections {
			for _, id := range p.Tiles {
				where[id] = p.ID
			}
		}
		if !reflect.DeepEqual(where, tc.want) {
			t.Errorf("on %v: placed %v, want %v", tc.on, where, tc.want)
		}
	}
}

func TestBuildListsEverySection(t *testing.T) {
	r, doc, _ := fixture(t)
	empty := view.Empty(r)
	encoded := string(raw(t, empty))
	if empty.Schema != view.Name || len(empty.Tiles) != 0 || len(empty.Sections) != 4 || empty.Sections[0].ID != view.ContextSection ||
		!strings.Contains(encoded, `"screenshots":[]`) || !strings.Contains(encoded, `"tiles":[]`) || !strings.Contains(encoded, `"verdict":null`) {
		t.Fatalf("empty view %s", encoded)
	}
	c := &view.Composition{Library: []tiles.LibraryTile{libraryTile(t, "schema_change", "h_000000000003")}}
	n, _ := view.Normalize(r, doc, c)
	n.Diagrams = []view.NativeTile{{ID: "dg_x", Type: view.TypeTable, Title: "X", Data: json.RawMessage(`{"source":[{"type":"hunk","id":"h_000000000004"}],"columns":["A","B"],"rows":[["a","b"]]}`)}}
	n.Overview.Diagram = ptr("dg_gone")
	n.Sections = []view.Placement{{ID: "sec_generated_client", Tiles: []string{"dg_x", "dg_gone"}}}
	v := view.Build(r, &n, view.ExtraTiles(doc, &n), nil)
	if len(v.Tiles) != 2 || v.Tiles[0].Covers[0] != "h_000000000004" || v.Tiles[1].Type != tiles.TypeCustom || v.Tiles[1].Definition == nil {
		t.Fatalf("tiles %+v", v.Tiles)
	}
	if v.Overview.Diagram != nil {
		t.Errorf("a diagram the view does not have: %v", *v.Overview.Diagram)
	}
	where := map[string][]string{}
	for _, p := range v.Sections {
		where[p.ID] = p.Tiles
	}
	if !slices.Equal(where["sec_generated_client"], []string{"dg_x"}) || !slices.Equal(where["sec_config_flag"], []string{"schema_change"}) || len(v.Sections) != 4 {
		t.Fatalf("sections %+v", v.Sections)
	}
}

func TestParseTellsTheSchemasApart(t *testing.T) {
	if _, err := view.Parse([]byte(`{"schema": "baum.composition/1", "layout": {"entries": []}}`)); err != view.ErrOtherSchema {
		t.Errorf("v1: %v", err)
	}
	if view.IsComposition([]byte(`{"schema": "baum.composition/1"}`)) || !view.IsComposition([]byte(`{"schema": "baum.composition/2"}`)) {
		t.Error("IsComposition")
	}
}
