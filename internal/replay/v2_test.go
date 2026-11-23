package replay_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/reviewv2"
)

func TestV2GeneratorOptions(t *testing.T) {
	if _, err := replay.NewGenerator("v2", replay.GeneratorConfig{Options: map[string]string{"concurrency": "2"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []replay.GeneratorConfig{
		{Options: map[string]string{"library": "off"}},
		{Options: map[string]string{"concurrency": "0"}},
		{PromptOverrides: map[string]string{"review-staged-outline": "x"}},
	} {
		if _, err := replay.NewGenerator("v2", c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if _, err := replay.NewGenerator("v2", replay.GeneratorConfig{PromptOverrides: map[string]string{"review-map": "Map it."}}); err != nil {
		t.Errorf("a map override: %v", err)
	}
}

func TestV2Metrics(t *testing.T) {
	g := &replay.V2Generator{}
	level := 2
	doc, _ := json.Marshal(reviewv2.Document{Schema: reviewv2.Name,
		Sections:   []reviewv2.Section{{ID: "sec_a", Kind: "logical", Level: &level, Body: []reviewv2.Block{{Type: reviewv2.BlockProse, Text: "a"}, {Type: reviewv2.BlockPoints, Items: []reviewv2.Point{{Text: "b"}, {Text: "c"}}}}}, {ID: "sec_b", Kind: "logical", Details: "failed"}},
		Tiles:      []reviewv2.Tile{{ID: "tl_a", Type: "flow", State: reviewv2.TileReady}, {ID: "tl_b", Type: "table", State: reviewv2.TileFailed}},
		Generation: &reviewv2.Generation{Overview: reviewv2.CallRecord{Status: "failed"}}})
	m, err := g.DocumentMetrics(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{replay.MetricV2Sections: 2, replay.MetricV2Logical: 2, replay.MetricV2SectionsFailed: 1, replay.MetricV2Tiles: 2,
		replay.MetricV2TilesReady: 1, replay.MetricV2TilesFailed: 1, "v2_tiles_type_flow": 1, replay.MetricV2Blocks: 2, replay.MetricV2Points: 2, replay.MetricV2ContextMissing: 1, replay.MetricV2OverviewFailed: 1}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	cost := 0.5
	levels, _ := json.Marshal(replay.V2Record{Schema: replay.V2RecordName, Repairs: 3, Levels: map[string]replay.V2Level{"map": {Calls: 4, Tokens: 10, Seconds: 2, CostUSD: &cost}}})
	am, err := g.ArtifactMetrics(func(name string) ([]byte, error) {
		if name != "levels.json" {
			return nil, errors.New("no " + name)
		}
		return levels, nil
	})
	if err != nil || am["v2_map_calls"] != 4 || am["v2_map_cost_usd"] != 0.5 || am[replay.MetricV2Repairs] != 3 {
		t.Fatalf("artifact metrics: %v %v", am, err)
	}
	for name := range am {
		if g.Definitions()[name] == "" {
			t.Errorf("%s has no definition", name)
		}
	}
}
