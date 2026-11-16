package replay_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/review"
)

func tilesSettings(t *testing.T, backend *completing, config replay.GeneratorConfig) replay.Settings {
	t.Helper()
	config.Staged = true
	g, err := replay.NewGenerator("tiles", config)
	if err != nil {
		t.Fatal(err)
	}
	s := staged(backend.Backend)
	s.Generator = g
	s.Backends.Claude = func(string) generate.CheckedBackend { return backend }
	return s
}

// The tiles generator runs the whole pipeline with tile reviews on: the
// review is the document, valid as ReviewGenerator's, and the composition
// and what the compose stage did are beside it, measured by the compose
// metrics with its cost and jump.
func TestTilesGeneratorWritesTheReviewAndItsComposition(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0", RunFunc: pricedReplies(nil)}}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: tilesSettings(t, backend, replay.GeneratorConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 || backend.calls.Load() != 1 || len(backend.Requests) != 3 {
		t.Fatalf("%+v, %d completions, %d runs", summary.Totals, backend.calls.Load(), len(backend.Requests))
	}
	caseDir := filepath.Join(outDir, "run", "cases", "c1")
	out, err := replay.ReadOutcome(caseDir)
	if err != nil || !out.DocumentValid || out.Generator != "tiles" || out.Calls != 4 || len(out.CallLog) != 4 {
		t.Fatalf("%v %+v", err, out)
	}
	document, err := os.ReadFile(filepath.Join(caseDir, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := review.NormalizeStored(document); err != nil {
		t.Errorf("the document is not a review: %v", err)
	}
	for _, name := range []string{"composition.json", "compose.json", "tiles.json", "provisional.json", "inventory.txt", "reply.json"} {
		if _, err := os.Stat(filepath.Join(caseDir, name)); err != nil {
			t.Error(err)
		}
	}
	metrics, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	stats := metrics.All.Stats
	if stats[replay.MetricComposeFallback].N != 1 || stats[replay.MetricComposeFallback].Sum != 0 || stats[replay.MetricComposeDiffers].Sum != 1 ||
		stats[replay.MetricComposeJump].N != 1 || stats[replay.MetricComposeCost].N != 1 || stats["sections"].N != 1 {
		t.Errorf("metrics %+v", stats)
	}
	if stats[replay.MetricComposeCost].Sum <= 0 || stats[replay.MetricComposeCost].Sum >= stats[replay.MetricCost].Sum {
		t.Errorf("compose cost %v of %v", stats[replay.MetricComposeCost].Sum, stats[replay.MetricCost].Sum)
	}
}

// A chain hands each revision the composition the one before it got.
func TestTilesGeneratorChainsTheComposition(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0", RunFunc: generatetest.StagedReplies(nil)}}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: tilesSettings(t, backend, replay.GeneratorConfig{}),
		Selection: replay.Selection{IDs: []string{"r-a", "r-b"}}})
	if err != nil || summary.Totals.OK != 2 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	inventory, err := os.ReadFile(filepath.Join(outDir, "run", "cases", "r-b", "inventory.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory), `"previous":{"entries"`) {
		t.Error("the second revision was not offered the first's composition")
	}
}

func TestTilesGeneratorRefusesWhatItCannotUse(t *testing.T) {
	for _, c := range []replay.GeneratorConfig{
		{Staged: false},
		{Staged: true, Options: map[string]string{"x": "y"}},
		{Staged: true, PromptOverrides: map[string]string{string(prompt.IDReviewTilesCompose): "## Submitting\n"}},
	} {
		if _, err := replay.NewGenerator("tiles", c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	g, err := replay.NewGenerator("tiles", replay.GeneratorConfig{Staged: true, PromptOverrides: map[string]string{string(prompt.IDReviewTilesCompose): "Arrange.\n"}})
	if err != nil {
		t.Fatal(err)
	}
	described, err := g.Describe(ctx(), "claude")
	if err != nil || described["compose_prompt_version"] != prompt.TilesVersion+"+custom" {
		t.Errorf("%v %v", err, described)
	}
}
