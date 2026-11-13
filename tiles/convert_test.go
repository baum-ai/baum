package tiles

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/review"
)

// convert normalizes a fixture's input as a reader of a stored document does
// and converts it.
func convert(t *testing.T, f fixture) ([]byte, *Document) {
	t.Helper()
	normalized, err := review.NormalizeStored(f.input(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := FromReview(normalized, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	return normalized, doc
}

func fixtureNamed(t *testing.T, name string) fixture {
	t.Helper()
	for _, f := range fixtures {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("no fixture %s", name)
	return fixture{}
}

// The inputs are stored reviews as the review package accepts them, except
// those that keep a shape only older documents have.
func TestFixturesAreStoredReviews(t *testing.T) {
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			findings := validator.ValidateStored(f.input(t), nil)
			if f.invalid {
				if len(findings) == 0 {
					t.Fatal("the input is a valid stored review; it need not be marked invalid")
				}
				return
			}
			if len(findings) > 0 {
				t.Fatalf("the input is not a valid stored review:\n%s", strings.Join(findings, "\n"))
			}
		})
	}
}

// Every fixture converts to its golden tile document, which passes the
// schema and every invariant, and converts back to the input.
func TestConvertFixtures(t *testing.T) {
	validator := NewValidator()
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			normalized, doc := convert(t, f)
			goldentest.Golden(t, "testdata/"+f.name+".tiles.json", doc)
			for _, failure := range CheckConversion(normalized, doc) {
				t.Error(failure)
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			validated, err := validator.Validate(encoded)
			if err != nil {
				t.Fatal(err)
			}
			for _, tile := range validated.Tiles {
				if tile.State == StateUnavailable {
					t.Errorf("tile %s is unavailable: %v", tile.ID, tile.Problems)
				}
			}
			back, err := ToReview(doc)
			if err != nil {
				t.Fatal(err)
			}
			if equal, err := RoundTripEqual(normalized, back); err != nil || !equal {
				t.Fatalf("the round trip differs (err %v)", err)
			}
			// The decoded golden converts back the same way, so a consumer
			// that reads a stored tile document gets the review.
			var decoded Document
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			again, err := ToReview(&decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(back, again) {
				t.Error("the decoded document converts back differently")
			}
		})
	}
}

func TestConversionIsDeterministic(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			normalized, doc := convert(t, f)
			first, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				again, err := FromReview(normalized, f.opts)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(again)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(first, encoded) {
					t.Fatal("converting the same review twice gave different bytes")
				}
				back, err := ToReview(again)
				if err != nil {
					t.Fatal(err)
				}
				backAgain, err := ToReview(doc)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(back, backAgain) {
					t.Fatal("converting the same tile document back twice gave different bytes")
				}
			}
		})
	}
}

func tileByID(t *testing.T, doc *Document, id string) Tile {
	t.Helper()
	for _, tile := range doc.Tiles {
		if tile.ID == id {
			return tile
		}
	}
	t.Fatalf("no tile %s", id)
	return Tile{}
}

func hasTile(doc *Document, id string) bool {
	return slices.ContainsFunc(doc.Tiles, func(tile Tile) bool { return tile.ID == id })
}

func entryOrder(doc *Document) []string {
	var order []string
	for _, entry := range doc.Layout.Entries {
		order = append(order, entry.Tile)
	}
	return order
}

func TestDefaultLayout(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "base"))
	want := []string{"overview", "risks", "sec_retry_policy", "sec_config_flag", "media", "evidence", "intent", "context", "decisions", "everything_else"}
	if got := entryOrder(doc); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if doc.Layout.Landing != 8 {
		t.Errorf("landing = %d, want 8 (two halves, then two rows of thirds)", doc.Layout.Landing)
	}
	data := tileByID(t, doc, "sec_retry_policy").Data
	var change ChangeData
	if err := json.Unmarshal(data, &change); err != nil {
		t.Fatal(err)
	}
	if len(change.Blocks) != 1 {
		t.Errorf("the change tile keeps %d blocks, want only the behaviour block", len(change.Blocks))
	}

	_, stale := convert(t, fixtureNamed(t, "stale-without-hunks"))
	order := entryOrder(stale)
	if got := order[len(order)-2:]; !slices.Equal(got, []string{"sec_old_approach", "sec_old_formatting"}) {
		t.Errorf("the layout ends with %v, want the obsolete changes", got)
	}
	var obsolete ChangeData
	if err := json.Unmarshal(tileByID(t, stale, "sec_old_approach").Data, &obsolete); err != nil {
		t.Fatal(err)
	}
	if len(obsolete.Blocks) != 2 {
		t.Errorf("an obsolete change tile keeps %d blocks, want all 2", len(obsolete.Blocks))
	}
	if covers := tileByID(t, stale, "sec_old_approach").Covers; covers != nil {
		t.Errorf("an obsolete change tile covers %v", covers)
	}

	_, many := convert(t, fixtureNamed(t, "many-changes"))
	var changes []string
	for _, entry := range many.Layout.Entries {
		if entry.Group == GroupChanges {
			changes = append(changes, entry.Tile)
		}
	}
	// Review score descending, attention first within a score, otherwise
	// document order.
	wantChanges := []string{"sec_retry_policy", "sec_part_3", "sec_part_2", "sec_part_5", "sec_config_flag", "sec_part_1", "sec_part_0", "sec_part_4"}
	if !slices.Equal(changes, wantChanges) {
		t.Errorf("changes = %v, want %v", changes, wantChanges)
	}
	visible := Visible(many.Layout)
	shown := 0
	for i, entry := range many.Layout.Entries {
		if entry.Group == GroupChanges && visible[i] {
			shown++
		}
	}
	if shown != 4 {
		t.Errorf("%d changes are shown on the landing page, want 4", shown)
	}

	_, unscored := convert(t, fixtureNamed(t, "unscored"))
	if got := entryOrder(unscored)[2:4]; !slices.Equal(got, []string{"sec_retry_policy", "sec_config_flag"}) {
		t.Errorf("unscored changes are in order %v, want document order", got)
	}
}

func TestOptionalTiles(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		present map[string]bool
		state   string
	}{
		{"base", map[string]bool{"context": true, "everything_else": true, "media": true, "decisions": true, "intent": true}, StateReady},
		{"context-missing", map[string]bool{"context": false}, ""},
		{"draft-context-missing", map[string]bool{"context": true}, StatePending},
		{"no-mechanical", map[string]bool{"everything_else": true}, ""},
		{"no-everything-else", map[string]bool{"everything_else": false}, ""},
		{"no-current-logical", map[string]bool{"everything_else": true, "media": false, "decisions": false, "risks": true}, ""},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, doc := convert(t, fixtureNamed(t, tc.fixture))
			for id, want := range tc.present {
				if hasTile(doc, id) != want {
					t.Errorf("tile %s present = %v, want %v", id, !want, want)
				}
			}
			if tc.state != "" && tileByID(t, doc, "context").State != tc.state {
				t.Errorf("context state = %s, want %s", tileByID(t, doc, "context").State, tc.state)
			}
		})
	}
}

func TestDetailsStates(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "details-states"))
	for id, want := range map[string]string{
		"sec_retry_policy":    StatePending,
		"sec_config_flag":     StateFailed,
		"sec_dropped_pending": StateFailed,
		"sec_dropped_failed":  StateFailed,
		"everything_else":     StateReady,
	} {
		if got := tileByID(t, doc, id).State; got != want {
			t.Errorf("%s state = %s, want %s", id, got, want)
		}
	}
}

func TestRiskRanking(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "critical-risks"))
	var data RisksData
	if err := json.Unmarshal(tileByID(t, doc, "risks").Data, &data); err != nil {
		t.Fatal(err)
	}
	// By tier, then the change's review order (the retry change is read
	// first), then document order; the mitigated risk is left out.
	want := []string{"rsk_flag_secret", "rsk_retry_storm", "rsk_flag_reload", "rsk_flag_default", "rsk_generated_drift"}
	if !slices.Equal(data.Ranked, want) {
		t.Errorf("ranked = %v, want %v", data.Ranked, want)
	}

	_, stale := convert(t, fixtureNamed(t, "stale-without-hunks"))
	if err := json.Unmarshal(tileByID(t, stale, "risks").Data, &data); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(data.Ranked, "rsk_old_approach") {
		t.Error("an active risk of an obsolete change is ranked")
	}
}

func TestDuplicateIDsAreRenamed(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "duplicate-ids"))
	risks := map[string]Risk{}
	for _, risk := range doc.Risks {
		risks[risk.ID] = risk
	}
	// The name the rule gives first occurs in the source, so it takes the
	// numeric suffix.
	renamed, ok := risks["rsk_retry_storm_queue_limits_2"]
	if !ok {
		t.Fatalf("risks = %v, want the repeated risk renamed with a suffix", slices.Collect(func(yield func(string) bool) {
			for id := range risks {
				if !yield(id) {
					return
				}
			}
		}))
	}
	if renamed.Origin == nil || *renamed.Origin != (RenameOrigin{Change: "sec_queue_limits", ID: "rsk_retry_storm"}) {
		t.Errorf("origin = %+v", renamed.Origin)
	}
	if risks["rsk_retry_storm"].Origin != nil || risks["rsk_retry_storm"].Change != "sec_retry_policy" {
		t.Error("the first occurrence must keep its ID")
	}
	if !strings.Contains(renamed.Text, "ev_retry_test_queue_limits") {
		t.Errorf("the renamed risk's text still cites the earlier section's evidence: %q", renamed.Text)
	}

	var queue ChangeData
	if err := json.Unmarshal(tileByID(t, doc, "sec_queue_limits").Data, &queue); err != nil {
		t.Fatal(err)
	}
	var attention struct {
		Text string   `json:"text"`
		Refs []string `json:"refs"`
	}
	if err := json.Unmarshal(queue.Attention, &attention); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(attention.Refs, []string{"rsk_retry_storm_queue_limits_2", "ev_retry_test_queue_limits"}) || attention.Text != "See rsk_retry_storm_queue_limits_2." {
		t.Errorf("attention = %+v", attention)
	}
	if !bytes.Contains(queue.Blocks[0], []byte(`"evidence":["ev_retry_test_queue_limits"]`)) {
		t.Errorf("a behaviour step still cites the earlier section's evidence: %s", queue.Blocks[0])
	}
	var decisions DecisionsData
	if err := json.Unmarshal(tileByID(t, doc, "decisions").Data, &decisions); err != nil {
		t.Fatal(err)
	}
	if len(decisions.Items) != 2 || !bytes.Contains(decisions.Items[1], []byte(`"id":"dec_doubling_delay_queue_limits"`)) {
		t.Errorf("decisions = %s", decisions.Items)
	}

	// The earlier section, the overview and the risk score are untouched.
	var retry ChangeData
	if err := json.Unmarshal(tileByID(t, doc, "sec_retry_policy").Data, &retry); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(retry.Attention, []byte(`"refs":["rsk_retry_storm"]`)) {
		t.Errorf("the first section's attention changed: %s", retry.Attention)
	}
	var score struct {
		Reasons []struct {
			Section string `json:"section"`
			Risk    string `json:"risk"`
		} `json:"reasons"`
	}
	if err := json.Unmarshal(doc.RiskScore, &score); err != nil {
		t.Fatal(err)
	}
	if last := score.Reasons[len(score.Reasons)-1]; last.Section != "sec_queue_limits" || last.Risk != "rsk_retry_storm" {
		t.Errorf("the risk score changed: %s", doc.RiskScore)
	}
}

func TestWithinSectionDuplicateKeepsReferences(t *testing.T) {
	_, doc := convert(t, fixtureNamed(t, "within-section-duplicate"))
	var ids []string
	for _, risk := range doc.Risks {
		if risk.Change == "sec_config_flag" {
			ids = append(ids, risk.ID)
		}
	}
	if !slices.Equal(ids, []string{"rsk_flag_default", "rsk_flag_default_config_flag"}) {
		t.Errorf("ids = %v", ids)
	}
	// References keep naming the first item, as the viewer resolves them.
	if !strings.Contains(tileByID(t, doc, "sec_config_flag").Headline, "(rsk_flag_default)") {
		t.Errorf("headline = %q", tileByID(t, doc, "sec_config_flag").Headline)
	}
}

func TestHeadline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"One sentence.", "One sentence"},
		{"First one. Second one.", "First one"},
		{"Version 1.2 is out. Then more.", "Version 1.2 is out"},
		{"A clause; then more. Next.", "A clause"},
		{"No end", "No end"},
		{"First line\nSecond line.", "First line"},
		{"Why? Because.", "Why"},
		{strings.Repeat("x", 400), strings.Repeat("x", 299) + "…"},
	} {
		if got := headline(tc.in); got != tc.want {
			t.Errorf("headline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFromReviewRejects(t *testing.T) {
	base := func(t *testing.T) map[string]any {
		var doc map[string]any
		if err := json.Unmarshal(fixtureNamed(t, "base").input(t), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	for _, tc := range []struct {
		name   string
		mutate func(doc map[string]any)
	}{
		{"another schema", func(doc map[string]any) { doc["schema"] = "baum.review/2" }},
		{"an unknown top-level field", func(doc map[string]any) { doc["extra"] = true }},
		{"an unknown section field", func(doc map[string]any) { sectionOf(doc, "sec_config_flag")["extra"] = 1 }},
		{"an unknown risk field", func(doc map[string]any) {
			array(sectionOf(doc, "sec_config_flag"), "risks")[0].(map[string]any)["extra"] = 1
		}},
		{"an unknown evidence field", func(doc map[string]any) {
			array(sectionOf(doc, "sec_config_flag"), "evidence")[0].(map[string]any)["extra"] = 1
		}},
		{"a repeated section ID", func(doc map[string]any) { sectionOf(doc, "sec_config_flag")["id"] = "sec_retry_policy" }},
		{"two current mechanical sections", func(doc map[string]any) {
			sectionOf(doc, "sec_config_flag")["kind"] = "mechanical"
		}},
		{"implements that is not an array", func(doc map[string]any) { object(doc, "summary")["implements"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := base(t)
			tc.mutate(doc)
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := FromReview(encoded, Options{}); err == nil {
				t.Fatal("FromReview accepted the document")
			}
		})
	}
	if _, err := FromReview([]byte(`{"schema":`), Options{}); err == nil {
		t.Error("FromReview accepted invalid JSON")
	}
}

// Each invariant catches the loss it guards against.
func TestCheckConversionDetectsLoss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		invariant string
		mutate    func(doc *Document)
	}{
		{"an uncovered hunk", "invariant 1", func(doc *Document) {
			for i := range doc.Tiles {
				if doc.Tiles[i].ID == TypeEverythingElse {
					doc.Tiles[i].Covers = doc.Tiles[i].Covers[1:]
				}
			}
		}},
		{"a dropped risk", "invariant 2", func(doc *Document) { doc.Risks = doc.Risks[1:] }},
		{"an unranked active risk", "invariant 2", func(doc *Document) {
			setData(doc, TypeRisks, RisksData{Ranked: []string{"rsk_retry_storm"}})
		}},
		{"a dropped evidence item", "invariant 3", func(doc *Document) { doc.Evidence = doc.Evidence[:len(doc.Evidence)-1] }},
		{"a dropped block", "invariant 4", func(doc *Document) {
			var data ChangeData
			_ = json.Unmarshal(tileOf(doc, "sec_retry_policy").Data, &data)
			data.Blocks = nil
			setData(doc, "sec_retry_policy", data)
		}},
		{"a changed risk score", "invariant 5", func(doc *Document) { doc.RiskScore = json.RawMessage(`{"rubric":"risk-v1"}`) }},
		{"a recommendation that appeared", "invariant 5", func(doc *Document) { doc.Recommendation = json.RawMessage(`null`) }},
		{"risks past the landing page", "invariant 6", func(doc *Document) {
			entries := doc.Layout.Entries
			risks := entries[1]
			doc.Layout.Entries = append(append([]Entry{entries[0]}, entries[2:]...), risks)
			doc.Layout.Landing = Landing(doc.Layout)
		}},
		{"a tile placed twice", "invariant 7", func(doc *Document) {
			doc.Layout.Entries = append(doc.Layout.Entries, doc.Layout.Entries[0])
		}},
		{"a stale landing count", "invariant 7", func(doc *Document) { doc.Layout.Landing = 2 }},
		{"an unavailable tile", "invariant 8", func(doc *Document) { tileOf(doc, TypeEvidence).State = StateUnavailable }},
		{"a tile with bad data", "invariant 8", func(doc *Document) { tileOf(doc, TypeEvidence).Data = json.RawMessage(`{"x":1}`) }},
		{"changed text", "invariant 9", func(doc *Document) { doc.Risks[0].Text += " Changed." }},
		{"a changed change title", "invariant 9", func(doc *Document) { doc.Changes[0].Title = "Another title" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized, doc := convert(t, fixtureNamed(t, "base"))
			tc.mutate(doc)
			failures := CheckConversion(normalized, doc)
			if !slices.ContainsFunc(failures, func(failure string) bool { return strings.HasPrefix(failure, tc.invariant+":") }) {
				t.Errorf("failures = %v, want one of %s", failures, tc.invariant)
			}
		})
	}
}

func TestCheckStability(t *testing.T) {
	_, previous := convert(t, fixtureNamed(t, "base"))
	_, next := convert(t, fixtureNamed(t, "duplicate-ids"))
	if failures := CheckStability(previous, next); len(failures) > 0 {
		t.Errorf("failures = %v", failures)
	}
	tileOf(next, "sec_config_flag").Type = TypeProse
	if failures := CheckStability(previous, next); len(failures) == 0 {
		t.Error("a tile that changed type passed")
	}
}
