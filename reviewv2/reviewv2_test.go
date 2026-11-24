package reviewv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

var fixtureNow = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authored(t *testing.T) Document {
	t.Helper()
	var d Document
	if err := json.Unmarshal(readFile(t, "v2.authored.json"), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func fixtureBundle(t *testing.T) *bundle.Bundle {
	t.Helper()
	var b bundle.Bundle
	if err := json.Unmarshal(readFile(t, "v2.bundle.json"), &b); err != nil {
		t.Fatal(err)
	}
	return &b
}

func completeFixture(t *testing.T, d Document, version int, previous []byte) Document {
	t.Helper()
	out, err := Complete(d, CompleteInput{Bundle: readFile(t, "v2.bundle.json"), Previous: previous, Version: version, Generator: []byte(`{"model": "fixture-model", "prompt_version": "v2-fixture"}`), Now: fixtureNow})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func encode(t *testing.T, d Document) []byte {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The hand-made example completes to a document of the schema's shape that
// every level's checks accept: the header, index and stats from the bundle,
// the risk score, the reading time, the code tile's excerpt and the risks'
// first version.
func TestCompleteExample(t *testing.T) {
	d := completeFixture(t, authored(t), 1, nil)
	goldentest.Golden(t, filepath.Join("testdata", "v2.golden.json"), d)
	if err := schema.ValidateReview2(encode(t, d)); err != nil {
		t.Fatalf("the completed example does not validate: %v", err)
	}
	if findings := CheckDocument(d, RefsOf(fixtureBundle(t))); len(findings) > 0 {
		t.Fatalf("the completed example has findings:\n%s", Findings(findings))
	}
	if findings := CheckDocument(d, nil); len(findings) > 0 {
		t.Fatalf("the completed example has findings against its own index:\n%s", Findings(findings))
	}
	var score struct {
		Score  int    `json:"score"`
		Status string `json:"status"`
		Rubric string `json:"rubric"`
	}
	if err := json.Unmarshal(d.Summary.RiskScore, &score); err != nil || score.Score != 3 || score.Status != "assessed" || score.Rubric != "risk-v1" {
		t.Fatalf("risk score = %s", d.Summary.RiskScore)
	}
	if d.Summary.Title != "Retry failed webhook deliveries" || d.Summary.ReviewMinutes == nil || *d.Summary.ReviewMinutes != 1 {
		t.Fatalf("summary = %+v", d.Summary)
	}
	var code CodeData
	if err := json.Unmarshal(d.Tiles[1].Data, &code); err != nil || !strings.HasPrefix(code.Text, "// Wait is") || strings.Count(code.Text, "\n") != 1 || d.Tiles[1].State != TileReady {
		t.Fatalf("code tile = %s (%s)", d.Tiles[1].Data, d.Tiles[1].State)
	}
	risk := d.Sections[0].Risks[0]
	if risk.State != "active" || *risk.IntroducedVersion != 1 || risk.MitigatedVersion != nil {
		t.Fatalf("risk = %+v", risk)
	}
	var derived struct {
		State       string `json:"state"`
		ReviewScore int    `json:"review_score"`
	}
	if err := json.Unmarshal(d.Sections[0].Derived, &derived); err != nil || derived.State != "new" || derived.ReviewScore != 3 {
		t.Fatalf("derived = %s", d.Sections[0].Derived)
	}
}

// A second version keeps the risks' identity: a risk the section no longer
// names is mitigated in this version, one it names again keeps the version
// it was introduced in, and each section's state compares its hunks.
func TestCompleteHistory(t *testing.T) {
	first := encode(t, completeFixture(t, authored(t), 1, nil))
	next := authored(t)
	next.Sections[0].Risks = []Risk{{ID: "rsk_wait_overflow", Tier: "low", Category: "correctness", Text: "A large attempt number overflows the shift.", Cites: []Anchor{HunkAnchor("h_a00000000001", []int{12, 12})}}}
	next.Sections[1].Hunks = []string{"h_a00000000002", "h_a00000000003"}
	next.Sections[0].Hunks = []string{"h_a00000000001"}
	d := completeFixture(t, next, 2, first)
	risks := d.Sections[0].Risks
	if len(risks) != 2 || risks[0].ID != "rsk_unbounded_retries" || risks[0].State != "mitigated" || *risks[0].IntroducedVersion != 1 || *risks[0].MitigatedVersion != 2 {
		t.Fatalf("carried risk = %+v", risks)
	}
	if risks[1].State != "active" || *risks[1].IntroducedVersion != 2 {
		t.Fatalf("new risk = %+v", risks[1])
	}
	states := map[string]string{}
	for _, s := range d.Sections {
		var derived struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(s.Derived, &derived)
		states[s.ID] = derived.State
	}
	if states["sec_retry_policy"] != "changed" || states["sec_delivery_errors"] != "changed" || states["sec_dependencies"] != "unchanged" {
		t.Fatalf("states = %v", states)
	}
	var header struct {
		Version         int  `json:"version"`
		PreviousVersion *int `json:"previous_version"`
	}
	if err := json.Unmarshal(d.Review, &header); err != nil || header.Version != 2 || header.PreviousVersion == nil || *header.PreviousVersion != 1 {
		t.Fatalf("header = %s", d.Review)
	}
	// The mitigated risk takes no part in the score: the section's level is.
	if !strings.Contains(string(d.Summary.RiskScore), `"score":3`) {
		t.Fatalf("risk score = %s", d.Summary.RiskScore)
	}

	// A section whose details failed keeps its previous risks as they were.
	failed := authored(t)
	failed.Sections[0].Details, failed.Sections[0].Risks, failed.Sections[0].Body = DetailsFailed, nil, nil
	d = completeFixture(t, failed, 2, first)
	if r := d.Sections[0].Risks; len(r) != 1 || r[0].State != "active" || *r[0].IntroducedVersion != 1 {
		t.Fatalf("retained risks = %+v", r)
	}
	if !strings.Contains(string(d.Summary.RiskScore), "incomplete_sections") {
		t.Fatalf("a failed section is a limitation: %s", d.Summary.RiskScore)
	}
}

// A baum.review/1 previous version is read through FromV1.
func TestCompleteAfterV1(t *testing.T) {
	d := completeFixture(t, authored(t), 2, readFile(t, "v1.review.json"))
	var header struct {
		PreviousVersion *int `json:"previous_version"`
	}
	if err := json.Unmarshal(d.Review, &header); err != nil || header.PreviousVersion == nil {
		t.Fatalf("header = %s", d.Review)
	}
}

// fromV1WithComposition is FromV1 with the tiles and overview of the review
// view built from the composition, its library and authored tiles unchecked:
// what a host serving the converted document replaces them with after
// checking them.
func fromV1WithComposition(t *testing.T, document, composition []byte) Document {
	t.Helper()
	d, err := FromV1(document)
	if err != nil {
		t.Fatal(err)
	}
	r, err := view.ParseReview(document)
	if err != nil {
		t.Fatal(err)
	}
	c, err := view.Parse(composition)
	if err != nil {
		t.Fatal(err)
	}
	var extras []tiles.Tile
	for _, lib := range c.Library {
		var def struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(lib.Definition, &def)
		if def.Name == "" {
			continue
		}
		state := tiles.StateReady
		if lib.Pending {
			state = tiles.StatePending
		}
		title := def.Title
		if title == "" {
			title = def.Name
		}
		extras = append(extras, tiles.Tile{ID: tiles.LibraryTileID(def.Name), Type: tiles.TypeCustom, Title: title, Data: lib.Data, State: state, Definition: lib.Definition})
	}
	for _, a := range c.Authored {
		kind, _, _ := strings.Cut(a.ID, "_")
		extras = append(extras, tiles.Tile{ID: a.ID, Type: kind, Title: a.Title, Data: a.Data, State: tiles.StateReady})
	}
	v := view.Build(r, c, extras, nil)
	d.Tiles = TilesOfView(v)
	d.Overview = OverviewOfView(v)
	d.Summary.RiskWhy = v.Overview.RiskSummary
	EnsureBodies(&d)
	return d
}

// The converted baum.review/1 example keeps its text and gets its tiles
// from its composition, owned where the composition placed them.
func TestFromV1Example(t *testing.T) {
	d := fromV1WithComposition(t, readFile(t, "v1.review.json"), readFile(t, "v1.composition.json"))
	goldentest.Golden(t, filepath.Join("testdata", "v1.v2.golden.json"), d)
	if err := schema.ValidateReview2(encode(t, d)); err != nil {
		t.Fatalf("the converted example does not validate: %v", err)
	}
	owners := map[string]string{}
	types := map[string]string{}
	for _, tile := range d.Tiles {
		owners[tile.ID], types[tile.ID] = tile.Owner, tile.Type
	}
	if owners["dg_delivery"] != OwnerOverview || owners["dg_flag"] != "sec_config_flag" || owners["schema_change"] != "sec_retry_policy" {
		t.Fatalf("owners = %v", owners)
	}
	if types["schema_change"] != "schema_change" || types["table_retry_states"] != "v1_table" || types["dg_delivery"] != TypeFlow {
		t.Fatalf("types = %v", types)
	}
	if d.Generation != nil {
		t.Fatal("a converted document has no generation record")
	}
	if d.Summary.Verdict != nil || d.Summary.RiskWhy == "" {
		t.Fatalf("summary = %+v", d.Summary)
	}
	var v1 map[string]any
	if err := json.Unmarshal(readFile(t, "v1.review.json"), &v1); err != nil {
		t.Fatal(err)
	}
	v1["summary"].(map[string]any)["recommendation"] = map[string]any{"decision": "look_first", "text": "Read the retry loop first.", "refs": []any{"sec_retry_policy"}}
	recommended, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	withVerdict, err := FromV1(recommended)
	if err != nil {
		t.Fatal(err)
	}
	if v := withVerdict.Summary.Verdict; v == nil || v.Decision != "look_first" || v.Text != "Read the retry loop first." || len(v.Points) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
	if d.Overview.Diagram == nil || *d.Overview.Diagram != "dg_delivery" || len(d.Overview.Intent) != 1 {
		t.Fatalf("overview = %+v", d.Overview)
	}
	for _, s := range d.Sections {
		if s.Gist == "" || len([]rune(s.Gist)) > MaxGist {
			t.Errorf("section %s gist = %q", s.ID, s.Gist)
		}
		if len(s.Body) == 0 {
			t.Errorf("section %s has no body: %+v", s.ID, s)
		}
	}

	// Without a composition there are no tiles and no overview parts.
	bare, err := FromV1(readFile(t, "v1.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Tiles) != 0 || bare.Overview.Diagram != nil || bare.Summary.RiskWhy != "" {
		t.Fatalf("bare = %+v %+v", bare.Tiles, bare.Overview)
	}
}

func TestReusableSections(t *testing.T) {
	first := encode(t, completeFixture(t, authored(t), 1, nil))
	next := authored(t)
	next.Sections[1].Hunks = []string{"h_a00000000002", "h_a00000000003"}
	next.Sections[0].Hunks = []string{"h_a00000000001"}
	next.Sections[2].Title = "Lock file"
	reused := ReusableSections(first, next.Sections)
	if len(reused) != 1 {
		t.Fatalf("reused = %v", reused)
	}
	r, ok := reused["sec_dependencies"]
	if !ok || r.Section.Title != "Lock file" || len(r.Section.Body) == 0 || r.Section.Derived != nil {
		t.Fatalf("reused = %+v", r)
	}
	all := ReusableSections(first, authored(t).Sections)
	policy := all["sec_retry_policy"]
	if len(policy.Tiles) != 1 || policy.Tiles[0].ID != "tl_wait_code" || len(policy.Section.Risks) != 1 || policy.Section.Risks[0].State != "" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestGistAndSentences(t *testing.T) {
	for _, tc := range []struct{ in, gist string }{
		{"One thing happens. Then another.", "One thing happens."},
		{"Uses `a.b` here, e.g. this. Next.", "Uses `a.b` here, e.g. this."},
		{"**Bold** start", "Bold start"},
		{strings.Repeat("word ", 40), strings.TrimSpace(strings.Repeat("word ", 27)) + "…"},
	} {
		if got := Gist(tc.in); got != tc.gist {
			t.Errorf("Gist(%q) = %q, want %q", tc.in, got, tc.gist)
		}
	}
	for in, want := range map[string]int{"": 0, "One.": 1, "One. Two!": 2, "No end": 1, "Uses `x.y. z` then e.g. stops. Two.": 2, `He said "stop." Then left.`: 2} {
		if got := Sentences(in); got != want {
			t.Errorf("Sentences(%q) = %d, want %d", in, got, want)
		}
	}
}

func has(findings []string, part string) bool {
	return slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, part) })
}

func TestChecks(t *testing.T) {
	refs := RefsOf(fixtureBundle(t))
	base := authored(t)
	for _, tc := range []struct {
		name  string
		check func(d Document) []string
		want  string
	}{
		{"evidence word", func(d Document) []string {
			d.Sections[0].Body[0].Text = "The evidence says so."
			return CheckSection(d.Sections[0], refs)
		}, `the word "evidence"`},
		{"long title", func(d Document) []string {
			d.Sections[0].Title = strings.Repeat("x", 61)
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, "61 characters"},
		{"joined title", func(d Document) []string {
			d.Sections[0].Title = "Policy, and its wait"
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, "without a comma"},
		{"hunk in no section", func(d Document) []string {
			d.Sections[2].Hunks = []string{"h_a00000000001"}
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, "h_a00000000004 are in no section"},
		{"two mechanical", func(d Document) []string {
			d.Sections[1].Kind = KindMechanical
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, "2 are mechanical"},
		{"owner missing", func(d Document) []string {
			d.Tiles[1].Owner = "sec_gone"
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, `"sec_gone" is not overview`},
		{"tile id prefix", func(d Document) []string {
			d.Tiles[1].ID = "dg_x"
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, refs)
		}, "not tl_"},
		{"unknown hunk", func(d Document) []string {
			d.Sections[0].Body[1].Items[0].Cites = []Anchor{HunkAnchor("h_ffffffffffff", nil)}
			return CheckSection(d.Sections[0], refs)
		}, "does not have"},
		{"lines outside hunk", func(d Document) []string {
			d.Sections[0].Body[1].Items[0].Cites = []Anchor{HunkAnchor("h_a00000000002", []int{1, 2})}
			return CheckSection(d.Sections[0], refs)
		}, "outside the hunk's lines 39 to 44"},
		{"image as a video", func(d Document) []string {
			d.Overview.Videos = d.Overview.Screenshots
			return CheckOverview(d.Summary, d.Overview, d.Sections, d.Tiles, refs)
		}, "is not a video clip"},
		{"unknown prompt", func(d Document) []string {
			d.Overview.Intent[0].Prompt = 9
			return CheckOverview(d.Summary, d.Overview, d.Sections, d.Tiles, refs)
		}, "prompt 9"},
		{"prose four sentences", func(d Document) []string {
			d.Sections[0].Body[0].Text = "A. B. C. D."
			return CheckSection(d.Sections[0], refs)
		}, "4 sentences"},
		{"note three sentences", func(d Document) []string {
			d.Sections[0].Body[3].Text = "A. B. C."
			return CheckSection(d.Sections[0], refs)
		}, "3 sentences; write at most 2"},
		{"text over its limit", func(d Document) []string {
			d.Sections[0].Body[0].Text = strings.Repeat("word ", 100)
			return CheckSection(d.Sections[0], refs)
		}, "over their limits"},
		{"point over its limit", func(d Document) []string {
			d.Sections[0].Body[1].Items[0].Text = strings.Repeat("x", MaxPoint+1)
			return CheckSection(d.Sections[0], refs)
		}, "over their limits"},
		{"no blocks", func(d Document) []string {
			d.Sections[0].Body = nil
			return CheckSection(d.Sections[0], refs)
		}, "it has no blocks"},
		{"too many blocks", func(d Document) []string {
			b := d.Sections[0].Body[0]
			d.Sections[0].Body = []Block{b, b, b, b, b, b, b}
			return CheckSection(d.Sections[0], refs)
		}, "more than 6 blocks"},
		{"empty block", func(d Document) []string {
			d.Sections[0].Body = append(d.Sections[0].Body, Block{Type: BlockProse, Text: " "})
			return CheckSection(d.Sections[0], refs)
		}, "empty or of a type"},
		{"hunk named in a point", func(d Document) []string {
			d.Sections[0].Body[1].Items[0].More = "See h_a00000000001."
			return CheckSection(d.Sections[0], refs)
		}, "names a hunk by its ID"},
		{"tile block of another section", func(d Document) []string {
			return CheckDocument(d, refs)
		}, `tile "tl_missing" is not a tile of this section`},
		{"native text over limit", func(d Document) []string {
			d.Tiles[0].Data = json.RawMessage(`{"lanes": [{"steps": [{"title": "` + strings.Repeat("y", 70) + `"}]}]}`)
			return CheckTile(d.Tiles[0], refs)
		}, "over their limits"},
		{"native shape", func(d Document) []string {
			d.Tiles[2].Data = json.RawMessage(`{"columns": [{"label": "only", "items": [{"title": "one"}]}]}`)
			return CheckTile(d.Tiles[2], refs)
		}, "1 columns"},
		{"code lines outside", func(d Document) []string {
			d.Tiles[1].Data = json.RawMessage(`{"hunk": "h_a00000000001", "lines": [11, 20]}`)
			return CheckTile(d.Tiles[1], refs)
		}, "outside the hunk's lines"},
		{"overview diagram missing", func(d Document) []string {
			missing := "tl_gone"
			d.Overview.Diagram = &missing
			return CheckOverview(d.Summary, d.Overview, d.Sections, d.Tiles, refs)
		}, `"tl_gone" is not a tile`},
		{"verdict points", func(d Document) []string {
			p := VerdictPoint{Text: "One."}
			d.Summary.Verdict.Points = []VerdictPoint{p, p, p, p}
			return CheckOverview(d.Summary, d.Overview, d.Sections, d.Tiles, refs)
		}, "give at most 3"},
		{"context sentences", func(d Document) []string {
			d.Context.Area.Text = "A. B. C."
			return CheckContext(*d.Context, refs)
		}, "3 sentences"},
		{"context area", func(d Document) []string {
			d.Context.Area.Text = ""
			return CheckContext(*d.Context, refs)
		}, "context.area.text: it is empty"},
		{"unknown tile type", func(d Document) []string {
			r := *refs
			r.TileTypes = map[string]bool{"schema_change": true}
			d.Tiles[1].Type = "api_change"
			return CheckMap(d.Summary.ChangeKind, d.Sections, d.Tiles, &r)
		}, `"api_change" is not a tile type`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Document
			if err := json.Unmarshal(encode(t, base), &d); err != nil {
				t.Fatal(err)
			}
			if got := tc.check(d); !has(got, tc.want) {
				t.Fatalf("findings %q lack %q", got, tc.want)
			}
		})
	}
	// The authored levels pass their own checks.
	if f := CheckMap(base.Summary.ChangeKind, base.Sections, base.Tiles, refs); len(f) > 0 {
		t.Fatalf("map: %v", f)
	}
	for _, s := range base.Sections {
		if f := CheckSection(s, refs); len(f) > 0 {
			t.Fatalf("section: %v", f)
		}
	}
	for _, tile := range base.Tiles {
		if f := CheckTile(tile, refs); len(f) > 0 {
			t.Fatalf("tile: %v", f)
		}
	}
}

// Substrate takes added library tiles as a converted review does.
func TestSubstrate(t *testing.T) {
	d := completeFixture(t, authored(t), 1, nil)
	doc := Substrate(d)
	if len(doc.Changes) != 3 || len(doc.Risks) != 1 || doc.Risks[0].Change != "sec_retry_policy" {
		t.Fatalf("substrate = %+v", doc)
	}
}

// FromV1 converts a document alone: a library tile (a saved type's HTML
// definition) only reaches a document after the host that serves it has
// checked it, never from the conversion.
func TestFromV1CarriesNoLibraryTiles(t *testing.T) {
	d, err := FromV1(readFile(t, "v1.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range d.Tiles {
		if len(tile.Definition) != 0 || tile.Type == tiles.TypeCustom {
			t.Errorf("tile %s carries a definition", tile.ID)
		}
	}
}

func TestFocusWithin(t *testing.T) {
	if got := FocusWithin([]int{9, 4, 4, 30, 5, 6}, 4, 10); !slices.Equal(got, []int{4, 5, 6}) {
		t.Errorf("focus %v", got)
	}
	if got := FocusWithin([]int{1, 2}, 4, 10); got != nil {
		t.Errorf("focus outside the lines kept: %v", got)
	}
}
