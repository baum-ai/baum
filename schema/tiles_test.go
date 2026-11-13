package schema

import (
	"encoding/json"
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

// The substrate risk, evidence and decision items list every property of
// the review item they extend, except the retired per-risk tiles, and every
// conditional rule of it, so a property or rule the review schema adds is not
// silently rejected or skipped.
func TestTileItemsExtendTheReviewItems(t *testing.T) {
	merged, err := MergedTiles()
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]any `json:"properties"`
			AllOf      []any          `json:"allOf"`
			If         any            `json:"if"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(merged, &root); err != nil {
		t.Fatal(err)
	}
	var decisions struct {
		Properties struct {
			Items struct {
				Items struct {
					Properties map[string]any `json:"properties"`
					AllOf      []any          `json:"allOf"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	var raw struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(merged, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw.Defs["tileData_decisions"], &decisions); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		review     string
		properties map[string]any
		rules      int
	}{
		{"reviewRisk", root.Defs["risk"].Properties, len(root.Defs["risk"].AllOf)},
		{"reviewEvidence", root.Defs["evidence"].Properties, len(root.Defs["evidence"].AllOf)},
		{"reviewDecision", decisions.Properties.Items.Items.Properties, len(decisions.Properties.Items.Items.AllOf)},
	} {
		review := root.Defs[tc.review]
		for name := range review.Properties {
			if name == "tiles" {
				continue
			}
			if _, ok := tc.properties[name]; !ok {
				t.Errorf("%s: property %s is missing", tc.review, name)
			}
		}
		rules := len(review.AllOf)
		if review.If != nil {
			rules++
		}
		if rules != tc.rules {
			t.Errorf("%s has %d conditional rules, the tile item references %d", tc.review, rules, tc.rules)
		}
	}
}
