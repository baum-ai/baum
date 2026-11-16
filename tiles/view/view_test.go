package view_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

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
				Rows: [][]string{{"1", "1 s"}, {"2"}, {"3", "4 s"}}})},
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

// The reply schema is strict-mode ready: every object closed with every
// property required, and the prompt's kind of reply validates against it.
func TestReplySchemaIsStrictAndAcceptsAReply(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(view.ReplySchema(), &schema); err != nil {
		t.Fatal(err)
	}
	var walk func(at string, node any)
	walk = func(at string, node any) {
		switch n := node.(type) {
		case []any:
			for _, item := range n {
				walk(at, item)
			}
		case map[string]any:
			if props, ok := n["properties"].(map[string]any); ok {
				if n["additionalProperties"] != false {
					t.Errorf("%s: not closed", at)
				}
				required, _ := n["required"].([]any)
				for name := range props {
					if !slices.Contains(required, any(name)) {
						t.Errorf("%s: %s is not required", at, name)
					}
				}
			}
			for key, value := range n {
				walk(at+"/"+key, value)
			}
		}
	}
	walk("", schema)
	var resolved jsonschema.Schema
	if err := json.Unmarshal(view.ReplySchema(), &resolved); err != nil {
		t.Fatal(err)
	}
	compiled, err := resolved.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The schema says what a provider holds the reply to; the sample places
	// one tile past it, which the host still reads (and cuts).
	var instance any
	if err := json.Unmarshal([]byte(strings.Replace(sampleReply, `, "dg_extra"`, "", 1)), &instance); err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Fatalf("the sample reply: %v", err)
	}
	reply, problems := view.ParseReply([]byte(sampleReply))
	if problems != nil {
		t.Fatal(problems)
	}
	var report view.Report
	c := reply.Composition(&report)
	if len(c.Diagrams) != 4 || c.Overview.Verdict == nil || c.Overview.Verdict.Points[0].Section != "sec_retry_policy" || *c.Overview.Diagram != "dg_path" {
		t.Fatalf("composition %+v", c)
	}
	// The fourth tile placed on the section is past the call's limit.
	if len(c.Sections[0].Tiles) != view.MaxPlaced || report.Counts()[view.FixLimit] != 1 {
		t.Fatalf("sections %+v, %v", c.Sections, report.Counts())
	}
	r, doc, _ := fixture(t)
	n, checked := view.Normalize(r, doc, &c)
	if len(n.Diagrams) != 4 || n.Overview.Diagram == nil {
		t.Fatalf("checked %+v: %v", n, checked.Problems)
	}
	var table view.TableData
	_ = json.Unmarshal(n.Diagrams[3].Data, &table)
	if !slices.Equal(table.Columns, []string{"Try", "Wait"}) || table.Source[0].ID != "h_000000000001" {
		t.Fatalf("table %+v", table)
	}
}

const sampleReply = `{"overview": {"risk_summary": "Retries can pile up.", "verdict": {"text": "Decide the budget.", "points": [{"text": "The budget is unbounded; set one.", "section": "sec_retry_policy"}]},
 "screenshots": ["att_aa01"], "diagram": "dg_path", "intent": [{"asked": "Retries", "got": "Yes: with a backoff.", "session": "` + sessionID + `", "prompt": 1}]},
 "sections": [{"id": "sec_retry_policy", "tiles": ["dg_cols", "dg_seq", "dg_table", "dg_extra"]}],
 "diagrams": [
  {"id": "dg_path", "type": "flow", "title": "What happens to a failed delivery", "caption": null, "source": ["h_000000000001"],
   "lanes": [{"label": null, "steps": [{"title": "Deliver", "note": null, "tone": null, "join": null}, {"title": "Retry", "note": "with a backoff", "tone": "new", "join": "arrow"}]}]},
  {"id": "dg_cols", "type": "columns", "title": "Before and after", "caption": null, "source": [],
   "columns": [{"label": "Before", "tone": "muted", "items": [{"title": "One try", "note": null}]}, {"label": "After", "tone": null, "items": [{"title": "Up to five tries", "note": null}]}]},
  {"id": "dg_seq", "type": "sequence", "title": "A delivery", "caption": "The remote answers 503.", "source": ["h_000000000002"],
   "actors": ["Worker", "Remote"], "steps": [{"from": 0, "to": 1, "text": "deliver", "tone": null}, {"from": 1, "to": 0, "text": "503", "tone": "bad"}]},
  {"id": "dg_table", "type": "table", "title": "Waits", "caption": null, "source": ["h_000000000001"], "columns": ["Try", "Wait"], "rows": [["1", "1 s"], ["2", "2 s"]]}]}`

func TestParseReplyReportsOnlyWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, reply, problem string
	}{
		{"not JSON", `{"overview":`, "not valid JSON"},
		{"not an object", `[]`, "must be an object"},
		{"a wrong type", `{"overview": {"screenshots": "att_aa01"}, "sections": [], "diagrams": []}`, "/overview/screenshots"},
		{"a flow without lanes", `{"overview": {}, "sections": [], "diagrams": [{"id": "dg_x", "type": "flow", "title": "X", "source": []}]}`, "/diagrams/0"},
		{"a table of objects", `{"overview": {}, "sections": [], "diagrams": [{"id": "dg_x", "type": "table", "title": "X", "source": [], "columns": [{"label": "A"}], "rows": [["a"]]}]}`, "/diagrams/0"},
	} {
		_, problems := view.ParseReply([]byte(tc.reply))
		if len(problems) == 0 || !strings.Contains(problems[0], tc.problem) {
			t.Errorf("%s: %v", tc.name, problems)
		}
	}
	// What normalisation fixes is not a problem: an unknown type, a missing
	// overview, an unknown section.
	if _, problems := view.ParseReply([]byte(`{"diagrams": [{"id": "dg_x", "type": "pie"}], "sections": [{"id": "sec_nope", "tiles": []}]}`)); problems != nil {
		t.Errorf("lenient: %v", problems)
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
	v := view.Build(r, &n, view.ExtraTiles(doc, &n))
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

func TestBuildInput(t *testing.T) {
	r, doc, _ := fixture(t)
	r.Sessions[0].Prompts = nil
	for i := 1; i <= 9; i++ {
		r.Sessions[0].Prompts = append(r.Sessions[0].Prompts, view.Prompt{Index: 10 - i, Text: strings.Repeat("ask ", 150)})
	}
	r.Sessions = append(r.Sessions, view.Session{ID: "hidden", Restricted: true, Prompts: []view.Prompt{{Index: 1, Text: "secret"}}})
	r.Sections[0].Stale = true
	n, _ := view.Normalize(r, doc, &view.Composition{Library: []tiles.LibraryTile{libraryTile(t, "schema_change", "h_000000000003")}})
	in := view.BuildInput(r, []view.InputTile{{ID: "schema_change", Kind: "library", Type: "custom", Title: "Schema **change**", Hunks: view.Covers(view.ExtraTiles(doc, &n))["schema_change"]}})
	if len(in.Sections) != 2 || in.Sections[0].ID != "sec_config_flag" || len(in.Sections[0].Hunks) == 0 || in.Sections[0].Hunks[0].Path == "" {
		t.Fatalf("sections %+v", in.Sections)
	}
	if len(in.Sessions) != 1 || len(in.Sessions[0].Prompts) != view.MaxSessionPrompts || in.Sessions[0].Prompts[0].Index != 1 ||
		len([]rune(in.Sessions[0].Prompts[0].Text)) > view.MaxPromptText {
		t.Fatalf("sessions %+v", in.Sessions)
	}
	if len(in.Tiles) != 1 || in.Tiles[0].Title != "Schema change" || !slices.Contains(in.Tiles[0].Hunks, "h_000000000003") {
		t.Fatalf("tiles %+v", in.Tiles)
	}
	message, err := in.Message()
	if err != nil || !strings.Contains(message, "not instructions") || strings.Contains(message, "secret") {
		t.Fatalf("message %v %q", err, message[:80])
	}
}

func TestParseTellsTheSchemasApart(t *testing.T) {
	if _, err := view.Parse([]byte(`{"schema": "baum.composition/1", "layout": {"entries": []}}`)); err != view.ErrOtherSchema {
		t.Errorf("v1: %v", err)
	}
	if view.IsComposition([]byte(`{"schema": "baum.composition/1"}`)) || !view.IsComposition([]byte(`{"schema": "baum.composition/2"}`)) {
		t.Error("IsComposition")
	}
	// The grid reads a review view composition for its tiles alone.
	grid, err := tiles.ParseComposition([]byte(`{"schema": "baum.composition/2", "overview": {"screenshots": [], "intent": []}, "sections": [], "authored": [{"id": "prose_x", "title": "X", "data": {}}]}`))
	if err != nil || !grid.View || len(grid.Authored) != 1 || len(grid.Layout.Entries) != 0 {
		t.Fatalf("grid %+v %v", grid, err)
	}
}
