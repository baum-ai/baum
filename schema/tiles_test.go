package schema

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCompileTiles(t *testing.T) {
	compiled, err := CompileTiles()
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Schema().ID != TilesURL {
		t.Errorf("$id = %q, want %q", compiled.Schema().ID, TilesURL)
	}
}

func TestCompileTilesDefinitions(t *testing.T) {
	merged, err := MergedTiles()
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(merged, &root); err != nil {
		t.Fatal(err)
	}
	for name := range root.Defs {
		if !strings.HasPrefix(name, "tileData_") {
			continue
		}
		if _, err := CompileTilesDefinition(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := CompileTilesDefinition("noSuchDefinition"); err == nil {
		t.Error("an unknown definition compiled")
	}
}

// The tile schema copies parts of the review schema under fixed names; a
// name both define is an error, so the tile schema never shadows one.
func TestMergeTilesRejectsSharedDefinitions(t *testing.T) {
	for _, name := range []string{"hunkId", "reviewRisk"} {
		var root map[string]any
		if err := json.Unmarshal(Tiles, &root); err != nil {
			t.Fatal(err)
		}
		root["$defs"].(map[string]any)[name] = map[string]any{"type": "string"}
		tiles, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mergeTiles(tiles, Document); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want a shared-definition error", name, err)
		}
	}
}

// The substrate risk, evidence and decision items take every property of
// the review item they extend, except the retired per-risk tiles, by
// reference to the review item's own schema, require what it requires, and
// apply its conditional rules, so a property or rule the review schema adds
// is not silently rejected or skipped.
func TestTileItemsExtendTheReviewItems(t *testing.T) {
	merged, err := MergedTiles()
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
		AllOf      []map[string]any          `json:"allOf"`
		If         any                       `json:"if"`
		Then       any                       `json:"then"`
	}
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(merged, &root); err != nil {
		t.Fatal(err)
	}
	def := func(raw json.RawMessage) item {
		var value item
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	var decisions struct {
		Properties struct {
			Items struct {
				Items json.RawMessage `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(root.Defs["tileData_decisions"], &decisions); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		review, rules string
		tile          item
	}{
		{"reviewRisk", "reviewRiskRules", def(root.Defs["risk"])},
		{"reviewEvidence", "reviewEvidenceRules", def(root.Defs["evidence"])},
		{"reviewDecision", "reviewDecisionRules", def(decisions.Properties.Items.Items)},
	} {
		review := def(root.Defs[tc.review])
		for name := range review.Properties {
			if name == "tiles" {
				continue
			}
			want := "#/$defs/" + tc.review + "/properties/" + name
			if got := tc.tile.Properties[name]["$ref"]; got != want {
				t.Errorf("%s: property %s refers to %v, want %s", tc.review, name, got, want)
			}
		}
		for _, name := range review.Required {
			if !slices.Contains(tc.tile.Required, name) {
				t.Errorf("%s: %s is required in the review item, not in the tile item", tc.review, name)
			}
		}
		if len(tc.tile.AllOf) != 1 || tc.tile.AllOf[0]["$ref"] != "#/$defs/"+tc.rules {
			t.Errorf("%s: the tile item's rules are %v, want a reference to %s", tc.review, tc.tile.AllOf, tc.rules)
		}
		rules := def(root.Defs[tc.rules])
		wantRules, _ := json.Marshal(map[string]any{"allOf": review.AllOf, "if": review.If, "then": review.Then})
		gotRules, _ := json.Marshal(map[string]any{"allOf": rules.AllOf, "if": rules.If, "then": rules.Then})
		if string(wantRules) != string(gotRules) {
			t.Errorf("%s: the copied rules differ from the review item's", tc.review)
		}
		if len(review.AllOf) == 0 && review.If == nil {
			t.Errorf("%s has no rules; the test no longer checks them", tc.review)
		}
	}
}
