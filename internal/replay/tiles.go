package replay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// TileGenerator generates a review as the desktop does with tile reviews on:
// the staged pipeline, then the view call that presents it on its page
// (generate.Options.Tiles). Its document is the review, as ReviewGenerator's
// is, so the review metrics and chains apply unchanged. What the view stage
// made is written beside it:
//
//   - composition.json, the review view composition the version would
//     store (baum.composition/2);
//   - compose.json, a ComposeRecord, which the stage metrics read: its
//     fallback, repair, fixes, cost and time (view.json holds the rest);
//   - view.json, the view the tile route would serve;
//   - provisional.json, the composition drafts showed while the review was
//     written;
//   - inventory.txt and reply.json, what the call read and answered.
//
// A review the view stage could not present has none of them but
// compose.json, with its problem.
type TileGenerator struct {
	// Review writes the review: it is staged, and its Reuse and staged prompt
	// overrides apply.
	Review *ReviewGenerator
	// ViewPrompt, when set, replaces the view prompt.
	ViewPrompt string
	// NoLibrary offers no library type and NoExhibits writes no authored
	// tile (--generator-option library=off, exhibits=off).
	NoLibrary, NoExhibits bool
	// Saved are the person's saved tile types, read from exported files
	// (--library-dir, LoadLibrary): each case offers them as the desktop
	// does (tilelibrary.Offer), with no record of use, so they rank by
	// name.
	Saved []tilelibrary.Type

	mu sync.Mutex
	// composed holds each generated review's composition by the review's
	// digest, and previous the composition a chain hands to a case (Next).
	composed map[[32]byte]*view.Composition
	previous map[string]*view.Composition
}

var _ interface {
	Generator
	GeneratedValidator
	Chainer
	Meter
	ArtifactMeter
	Definer
	Deriver
} = (*TileGenerator)(nil)

func init() {
	RegisterGenerator("tiles", func(c GeneratorConfig) (Generator, error) {
		g := &TileGenerator{Review: &ReviewGenerator{Staged: true, Reuse: c.Reuse}}
		for _, key := range slices.Sorted(maps.Keys(c.Options)) {
			value := c.Options[key]
			if value != "on" && value != "off" {
				return nil, fmt.Errorf("--generator-option %s=%s: use on or off", key, value)
			}
			switch key {
			case "library":
				g.NoLibrary = value == "off"
			case "exhibits":
				g.NoExhibits = value == "off"
			default:
				return nil, fmt.Errorf("the tiles generator takes --generator-option library=on|off and exhibits=on|off (got %s)", key)
			}
		}
		if !c.Staged {
			return nil, errors.New("the tiles generator writes the review in stages; drop --no-staged")
		}
		if c.LibraryDir != "" {
			saved, err := LoadLibrary(c.LibraryDir)
			if err != nil {
				return nil, err
			}
			g.Saved = saved
		}
		for id, text := range c.PromptOverrides {
			if prompt.ID(id) == prompt.IDReviewView {
				if err := prompt.Validate(prompt.ID(id), text); err != nil {
					return nil, fmt.Errorf("prompt override %s: %w", id, err)
				}
				g.ViewPrompt = text
				continue
			}
			if g.Review.PromptOverrides == nil {
				g.Review.PromptOverrides = prompt.Overrides{}
			}
			g.Review.PromptOverrides[prompt.ID(id)] = text
		}
		return g, nil
	})
	RegisterMeter("tiles", &TileGenerator{Review: &ReviewGenerator{}})
}

func (g *TileGenerator) Name() string { return "tiles" }

func (g *TileGenerator) prompts() prompt.TilesPrompts {
	if g.ViewPrompt == "" {
		return prompt.TilesPrompts{}
	}
	return prompt.TilesPrompts{Overrides: prompt.Overrides{prompt.IDReviewView: g.ViewPrompt}}
}

// Describe is the review's description with the view prompt's version and
// digest.
func (g *TileGenerator) Describe(ctx context.Context, backend string) (map[string]any, error) {
	described, err := g.Review.Describe(ctx, backend)
	if err != nil {
		return nil, err
	}
	instructions, err := g.prompts().ViewInstructions(ctx, backend)
	if err != nil {
		return nil, err
	}
	described["view_prompt_version"], described["view_prompt_digest"] = instructions.Version, instructions.Digest
	if len(g.Saved) > 0 {
		saved := map[string]string{}
		for _, t := range g.Saved {
			saved[t.Name] = t.Entry.Definition.Digest
		}
		described["saved_tile_types"] = saved
	}
	return described, nil
}

func (g *TileGenerator) Generate(ctx context.Context, o generate.Options, c Case) (Generated, error) {
	validator, err := g.Review.validate()
	if err != nil {
		return Generated{}, err
	}
	if err := g.Review.check(); err != nil {
		return Generated{}, err
	}
	o.Staged, o.NoReuse, o.Validator = true, !g.Review.Reuse, validator
	o.StagedPrompts = prompt.StagedPrompts{Overrides: g.Review.PromptOverrides}
	g.mu.Lock()
	previous := g.previous[c.ID]
	g.mu.Unlock()
	o.Tiles = &generate.Tiles{Prompts: g.prompts(), Previous: previous, NoLibrary: g.NoLibrary, NoExhibits: g.NoExhibits}
	if len(g.Saved) > 0 {
		repository := c.Repository.String()
		o.Tiles.Saved = func(_ context.Context, diffPaths, taken []string) ([]library.Entry, []library.Entry) {
			return tilelibrary.Offer(g.Saved, tilelibrary.Offering{Repository: repository, DiffPaths: diffPaths, Taken: taken})
		}
	}
	result, err := generate.Run(ctx, o)
	out := Generated{Outcome: result.Outcome}
	if err != nil {
		return out, err
	}
	out.Document = result.Document
	out.PromptVersion, out.PromptDigest, out.BundleDigest = result.Metadata.PromptVersion, result.Metadata.PromptDigest, result.Metadata.BundleDigest
	out.Notes = slices.Clone(result.FailedSections)
	if result.ContextFailed {
		out.Notes = append(out.Notes, "context")
	}
	out.Details = map[string]any{"failed_sections": result.FailedSections, "context_failed": result.ContextFailed,
		"outline_repairs": result.OutlineRepairs, "outline_rejections": result.OutlineRejections}
	report := result.Compose
	if report == nil {
		return out, errors.New("the run did not arrange the tiles")
	}
	record := ComposeRecord{Schema: ComposeRecordName, Problem: report.Problem, Seconds: report.Elapsed.Seconds()}
	list := o.PriceList
	if list == nil {
		list = pricing.Active()
	}
	record.CostUSD = list.BackendOutcomeCost(o.Backend.Name(), report.Outcome.Model, report.Outcome)
	out.Artifacts = map[string][]byte{}
	if result.Composition != nil && view.IsComposition(result.Composition) {
		record.Fallback, record.FallbackReason, record.Repaired, record.Fixes = report.Fallback, report.FallbackReason, report.Repaired, report.Fixes
		record.InventoryBytes = len(report.Inventory)
		if err := g.viewArtifacts(out.Artifacts, result, report, list, o.Backend.Name()); err != nil {
			return out, err
		}
	}
	if out.Artifacts["compose.json"], err = json.MarshalIndent(record, "", "  "); err != nil {
		return out, err
	}
	switch {
	case record.Problem != "":
		out.Notes = append(out.Notes, "no-composition")
	case record.Fallback:
		out.Notes = append(out.Notes, "compose-fallback")
	}
	out.Details["compose_fallback"], out.Details["compose_fallback_reason"] = record.Fallback, record.FallbackReason
	out.Details["compose_problem"], out.Details["compose_fixes"] = record.Problem, record.Fixes
	return out, nil
}

// viewArtifacts writes what the view stage made of a review: its
// composition, the view the tile route would serve, the analysis's
// artifacts, the drafts' composition, the call's input and reply. A chain
// hands the composition's library and authored tiles to the next case.
func (g *TileGenerator) viewArtifacts(artifacts map[string][]byte, result generate.Result, report *generate.ComposeReport, list *pricing.List, backend string) error {
	composition, err := view.Parse(result.Composition)
	if err != nil {
		return fmt.Errorf("the stored composition does not parse: %w", err)
	}
	served := tileview.Build(result.Document, result.Composition, tiles.Options{})
	if artifacts["view.json"], err = json.MarshalIndent(served.View, "", "  "); err != nil {
		return err
	}
	_, doc, err := converted(result.Document)
	if err != nil {
		return err
	}
	if err := analysisArtifacts(artifacts, report.Tiles, doc, served.View, composition, list, backend); err != nil {
		return err
	}
	artifacts["composition.json"] = result.Composition
	if len(report.Provisional) > 0 {
		artifacts["provisional.json"] = report.Provisional
	}
	if report.Inventory != "" {
		artifacts["inventory.txt"] = []byte(report.Inventory)
	}
	if len(report.Reply) > 0 {
		artifacts["reply.json"] = report.Reply
	}
	g.mu.Lock()
	if g.composed == nil {
		g.composed = map[[32]byte]*view.Composition{}
	}
	g.composed[sha256.Sum256(result.Document)] = composition
	g.mu.Unlock()
	return nil
}

// Validate checks the review alone; the runner calls ValidateGenerated,
// which checks the composition too.
func (g *TileGenerator) Validate(document []byte, c Case) []string {
	return g.Review.Validate(document, c)
}

// ValidateGenerated checks the review as the stored form, and the
// composition as a host would before storing it (checkViewComposition). A
// review without a composition is valid: its version would show the review
// alone.
func (g *TileGenerator) ValidateGenerated(generated Generated, c Case) []string {
	findings := g.Review.Validate(generated.Document, c)
	if raw := generated.Artifacts["composition.json"]; raw != nil {
		findings = append(findings, checkViewComposition(generated.Document, raw)...)
	}
	return findings
}

// Next carries the previous revision's review as ReviewGenerator does, and
// hands its composition to the case as the previous composition, when this
// generator wrote it in this process.
func (g *TileGenerator) Next(c Case, document []byte) (bundle.Bundle, error) {
	b, err := g.Review.Next(c, document)
	if err != nil {
		return b, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if composition := g.composed[sha256.Sum256(document)]; composition != nil {
		if g.previous == nil {
			g.previous = map[string]*view.Composition{}
		}
		g.previous[c.ID] = composition
	}
	return b, nil
}

// DocumentMetrics measures the review (ReviewGenerator's metrics).
func (g *TileGenerator) DocumentMetrics(document []byte) (map[string]float64, error) {
	return g.Review.DocumentMetrics(document)
}

// PairMetrics compares the reviews of a chain (ReviewGenerator's metrics).
func (g *TileGenerator) PairMetrics(previous, next []byte) (map[string]float64, error) {
	return g.Review.PairMetrics(previous, next)
}

// Derived is ReviewGenerator's.
func (g *TileGenerator) Derived(sums map[string]float64) map[string]float64 {
	return g.Review.Derived(sums)
}

// ComposeRecordName is the schema of compose.json, a tiles case's record of
// its view stage.
const ComposeRecordName = "replay.compose/2"

// ComposeRecord is compose.json: what the view stage did, which the stage
// metrics read. Problem says why the version would store no composition
// at all.
type ComposeRecord struct {
	Schema         string                `json:"schema"`
	Fallback       bool                  `json:"fallback"`
	FallbackReason string                `json:"fallback_reason,omitempty"`
	Repaired       bool                  `json:"repaired"`
	Fixes          map[tiles.FixKind]int `json:"fixes,omitempty"`
	InventoryBytes int                   `json:"inventory_bytes"`
	Seconds        float64               `json:"seconds"`
	CostUSD        *float64              `json:"cost_usd,omitempty"`
	Problem        string                `json:"problem,omitempty"`
}

// The view stage's metrics.
const (
	MetricComposeFallback = "compose_fallback"
	MetricComposeRepaired = "compose_repaired"
	MetricComposeFixes    = "compose_fixes"
	MetricComposeBytes    = "compose_inventory_bytes"
	MetricComposeSeconds  = "compose_seconds"
	MetricComposeCost     = "compose_cost_usd"
	MetricComposeProblem  = "compose_no_composition"
)

// ArtifactMetrics measures compose.json (the view stage's fallback, repair,
// fixes, input size, time and cost, and whether the version would have no
// composition) and analysis.json.
func (g *TileGenerator) ArtifactMetrics(read func(string) ([]byte, error)) (map[string]float64, error) {
	raw, err := read("compose.json")
	if err != nil {
		return nil, errors.New("the case has no compose.json")
	}
	var record ComposeRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != ComposeRecordName {
		return nil, errors.New("compose.json is not a compose record")
	}
	if record.Problem != "" {
		return map[string]float64{MetricComposeProblem: 1, MetricComposeSeconds: record.Seconds}, nil
	}
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	fixes := 0
	for _, n := range record.Fixes {
		fixes += n
	}
	values := map[string]float64{MetricComposeFallback: flag(record.Fallback), MetricComposeRepaired: flag(record.Repaired), MetricComposeFixes: float64(fixes),
		MetricComposeBytes: float64(record.InventoryBytes), MetricComposeSeconds: record.Seconds, MetricComposeProblem: 0}
	if err := analysisMetrics(read, values); err != nil {
		return nil, err
	}
	if record.CostUSD != nil {
		values[MetricComposeCost] = *record.CostUSD
	}
	return values, nil
}

// Definitions are the review's and the view stage's.
func (g *TileGenerator) Definitions() map[string]string {
	definitions := g.Review.Definitions()
	maps.Copy(definitions, map[string]string{
		MetricComposeFallback: "1 when the view call's reply was not used (see the record's fallback_reason)",
		MetricComposeRepaired: "1 when an invalid reply needed the repair call",
		MetricComposeFixes:    "fixes the host's checks made to the reply, and tiles it placed",
		MetricComposeBytes:    "bytes of the view call's user message",
		MetricComposeSeconds:  "seconds the view stage took",
		MetricComposeCost:     "what the view stage cost at API prices (cost_usd includes it)",
		MetricComposeProblem:  "1 when the version would have no composition at all",
	})
	maps.Copy(definitions, analysisDefinitions())
	for _, t := range g.Saved {
		for _, part := range []string{"selected", "ready", "not_applies", "dropped", "repaired", "reused", "on_section"} {
			definitions["library_"+t.Name+"_"+part] = "the saved " + t.Name + " type: " + part
		}
	}
	return definitions
}

// LoadLibrary reads the tile type files (*.baumtile.json) in dir as an
// in-memory library: each passes every check an import does
// (tilelibrary.ReadFile and Prepare) and counts as checked. Two files of
// one name are refused.
func LoadLibrary(dir string) ([]tilelibrary.Type, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*"+tilelibrary.FileExtension))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("--library-dir %s holds no %s file", dir, tilelibrary.FileExtension)
	}
	slices.Sort(paths)
	var out []tilelibrary.Type
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		c, err := tilelibrary.ReadFile(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		p, err := tilelibrary.Prepare(c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if slices.ContainsFunc(out, func(t tilelibrary.Type) bool { return t.Name == p.Name() }) {
			return nil, fmt.Errorf("%s: a tile type named %s is loaded already", filepath.Base(path), p.Name())
		}
		out = append(out, tilelibrary.Type{Name: p.Name(), Entry: p.Entry, Paths: p.Paths, Origin: p.Origin})
	}
	return out, nil
}
