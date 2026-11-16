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

	"github.com/baum-ai/baum/agent"
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
// the staged pipeline, then the compose stage, the view call that presents
// it on its page (generate.Options.Tiles). Its document is the review, as
// ReviewGenerator's is, so the review metrics and chains apply unchanged.
// What the compose stage made is written beside it:
//
//   - composition.json, the composition the version would store (a
//     review view composition, baum.composition/2);
//   - compose.json, a ComposeRecord, which the compose metrics read; for a
//     review view composition only its fallback, repair, fixes, cost and
//     time are set (view.json holds the rest);
//   - view.json and tiles.json, the view and the tile document the tile
//     route would serve;
//   - provisional.json, the composition drafts showed while the review was
//     written;
//   - inventory.txt and reply.json, what the call read and answered.
//
// A review the compose stage could not present has none of them but
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
	composed map[[32]byte]*tiles.Composition
	previous map[string]*tiles.Composition
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
	} else if result.Composition != nil {
		composition, err := tiles.ParseComposition(result.Composition)
		if err != nil {
			return out, fmt.Errorf("the stored composition does not parse: %w", err)
		}
		_, doc, err := converted(result.Document)
		if err != nil {
			return out, err
		}
		composed := agent.ComposeOutcome{Composition: *composition, Inventory: report.Inventory, Fallback: report.Fallback,
			FallbackReason: report.FallbackReason, Repaired: report.Repaired}
		measured, _, _, err := recordOf(doc, composed)
		if err != nil {
			return out, err
		}
		measured.Problem, measured.Seconds, measured.CostUSD = record.Problem, record.Seconds, record.CostUSD
		record = measured
		record.Jump = jump(doc, report.Provisional, composition)
		served := tileview.Build(result.Document, result.Composition, tiles.Options{})
		if out.Artifacts["tiles.json"], err = json.MarshalIndent(served.Tiles, "", "  "); err != nil {
			return out, err
		}
		if err := analysisArtifacts(out.Artifacts, report.Tiles, served.Tiles, composition, list, o.Backend.Name()); err != nil {
			return out, err
		}
		out.Artifacts["composition.json"] = record.Composition
		if len(report.Provisional) > 0 {
			out.Artifacts["provisional.json"] = report.Provisional
		}
		if report.Inventory != "" {
			out.Artifacts["inventory.txt"] = []byte(report.Inventory)
		}
		if len(report.Reply) > 0 {
			out.Artifacts["reply.json"] = report.Reply
		}
		g.mu.Lock()
		if g.composed == nil {
			g.composed = map[[32]byte]*tiles.Composition{}
		}
		g.composed[sha256.Sum256(result.Document)] = composition
		g.mu.Unlock()
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

// viewArtifacts writes what the compose stage, the view call, made of a
// review: its composition, the view and tile document the tile route would
// serve, the analysis's artifacts, the drafts' composition, the call's input
// and reply. A chain hands the composition's library tiles to the next case.
func (g *TileGenerator) viewArtifacts(artifacts map[string][]byte, result generate.Result, report *generate.ComposeReport, list *pricing.List, backend string) error {
	grid, err := tiles.ParseComposition(result.Composition)
	if err != nil {
		return fmt.Errorf("the stored composition does not parse: %w", err)
	}
	served := tileview.Build(result.Document, result.Composition, tiles.Options{})
	if artifacts["tiles.json"], err = json.MarshalIndent(served.Tiles, "", "  "); err != nil {
		return err
	}
	if artifacts["view.json"], err = json.MarshalIndent(served.View, "", "  "); err != nil {
		return err
	}
	if err := analysisArtifacts(artifacts, report.Tiles, served.Tiles, grid, list, backend); err != nil {
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
		g.composed = map[[32]byte]*tiles.Composition{}
	}
	g.composed[sha256.Sum256(result.Document)] = grid
	g.mu.Unlock()
	return nil
}

// jump counts the landing entries whose tile or size differs between the
// provisional layout and the composition, position by position, both
// applied to the review's tile document.
func jump(doc *tiles.Document, provisional []byte, final *tiles.Composition) *int {
	if len(provisional) == 0 {
		return nil
	}
	before, err := tiles.ParseComposition(provisional)
	if err != nil {
		return nil
	}
	shown := func(c *tiles.Composition) ([]tiles.Entry, bool) {
		composed, err := tiles.Compose(doc, c)
		if err != nil {
			return nil, false
		}
		visible := tiles.Visible(composed.Layout)
		var entries []tiles.Entry
		for i, entry := range composed.Layout.Entries {
			if visible[i] {
				entries = append(entries, entry)
			}
		}
		return entries, true
	}
	a, ok := shown(before)
	if !ok {
		return nil
	}
	b, ok := shown(final)
	if !ok {
		return nil
	}
	n := 0
	for i := range max(len(a), len(b)) {
		if i >= len(a) || i >= len(b) || a[i].Tile != b[i].Tile || a[i].Size != b[i].Size {
			n++
		}
	}
	return &n
}

// Validate checks the review alone; the runner calls ValidateGenerated,
// which checks the composition too.
func (g *TileGenerator) Validate(document []byte, c Case) []string {
	return g.Review.Validate(document, c)
}

// ValidateGenerated checks the review as the stored form, and the
// composition as a host would before storing it (checkComposition). A review
// without a composition is valid: its version would show the default layout.
func (g *TileGenerator) ValidateGenerated(generated Generated, c Case) []string {
	findings := g.Review.Validate(generated.Document, c)
	switch raw := generated.Artifacts["composition.json"]; {
	case raw == nil:
	case view.IsComposition(raw):
		findings = append(findings, checkViewComposition(generated.Document, raw)...)
	default:
		findings = append(findings, checkComposition(generated.Document, raw)...)
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
			g.previous = map[string]*tiles.Composition{}
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

// The metrics the tiles generator adds to the compose metrics.
const (
	MetricComposeCost    = "compose_cost_usd"
	MetricComposeJump    = "compose_jump"
	MetricComposeProblem = "compose_no_composition"
)

// ArtifactMetrics measures compose.json with the compose metrics, plus the
// compose stage's cost, the jump from the provisional layout and whether the
// version would have no composition.
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
	values, err := (&ComposeGenerator{}).DocumentMetrics(raw)
	if err != nil {
		return nil, err
	}
	if err := analysisMetrics(read, values); err != nil {
		return nil, err
	}
	values[MetricComposeProblem] = 0
	if record.CostUSD != nil {
		values[MetricComposeCost] = *record.CostUSD
	}
	if record.Jump != nil {
		values[MetricComposeJump] = float64(*record.Jump)
	}
	return values, nil
}

// Definitions are the review's and the compose stage's.
func (g *TileGenerator) Definitions() map[string]string {
	definitions := g.Review.Definitions()
	maps.Copy(definitions, (&ComposeGenerator{}).Definitions())
	definitions[MetricComposeCost] = "what the compose stage cost at API prices (cost_usd includes it)"
	definitions[MetricComposeJump] = "landing entries whose tile or size differs between the provisional layout drafts showed and the composition"
	definitions[MetricComposeProblem] = "1 when the version would have no composition at all (the default layout)"
	maps.Copy(definitions, analysisDefinitions())
	for _, t := range g.Saved {
		for _, part := range []string{"selected", "ready", "not_applies", "dropped", "repaired", "reused", "landing"} {
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
