package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// ViewRecordName is the schema of a view case's document.json.
const ViewRecordName = "replay.view/1"

// ViewGenerator runs only the view call (agent.ViewRun) over reviews that
// already exist, with no analysis of its own:
// the saved reviews of the dataset (Source ComposeFromStore), or the
// documents of an earlier run (ComposeFromRun), whose library fills and
// exhibits it places again. Its document is a ViewRecord; the composition,
// the served view, the call's input and its reply are artifacts beside it:
//
//   - composition.json, the review view composition a version would store;
//   - view.json, the view the daemon would serve (tileview);
//   - input.txt and reply.json, what the call read and answered.
//
// So a prompt can be iterated on a frozen set of reviews:
//
//	replay --generator view --compose-from-store --case ... --prompt-override review-view=<file>
//	replay --generator view --compose-only --from-run <run> ...
type ViewGenerator struct {
	// Source, Dataset and Run say where the reviews come from
	// (savedReviews).
	Source, Dataset, Run string
	// PromptOverrides replaces the view prompt.
	PromptOverrides prompt.Overrides

	reviews *savedReviews
}

var _ interface {
	Generator
	Meter
	Definer
} = (*ViewGenerator)(nil)

func init() {
	RegisterGenerator("view", func(c GeneratorConfig) (Generator, error) {
		if c.LibraryDir != "" {
			return nil, errors.New("--library-dir goes with --generator tiles")
		}
		g := &ViewGenerator{Source: c.Options["source"], Dataset: c.Options["dataset"], Run: c.Options["run"]}
		for key := range c.Options {
			if !slices.Contains([]string{"source", "dataset", "run"}, key) {
				return nil, fmt.Errorf("the view generator takes the options source, dataset and run, not %q", key)
			}
		}
		for id, text := range c.PromptOverrides {
			if prompt.ID(id) != prompt.IDReviewView {
				return nil, fmt.Errorf("prompt override %q is not the view prompt %s", id, prompt.IDReviewView)
			}
			if err := prompt.Validate(prompt.ID(id), text); err != nil {
				return nil, fmt.Errorf("prompt override %s: %w", id, err)
			}
			g.PromptOverrides = prompt.Overrides{prompt.ID(id): text}
		}
		switch {
		case g.Source == ComposeFromStore && g.Dataset == "":
			return nil, errors.New("presenting saved reviews needs the dataset directory")
		case g.Source == ComposeFromRun && g.Run == "":
			return nil, errors.New("--compose-only needs --from-run <run directory>")
		case g.Source != ComposeFromStore && g.Source != ComposeFromRun:
			return nil, errors.New("the view generator needs --compose-from-store or --compose-only --from-run <run>")
		}
		g.reviews = &savedReviews{Source: g.Source, Dataset: g.Dataset, Run: g.Run}
		return g, nil
	})
	RegisterMeter("view", &ViewGenerator{})
}

func (g *ViewGenerator) Name() string { return "view" }

// Close closes the review stores the generator copied.
func (g *ViewGenerator) Close() error {
	if g.reviews == nil {
		return nil
	}
	return g.reviews.Close()
}

func (g *ViewGenerator) prompts() prompt.TilesPrompts {
	return prompt.TilesPrompts{Overrides: g.PromptOverrides}
}

func (g *ViewGenerator) Describe(ctx context.Context, backend string) (map[string]any, error) {
	instructions, err := g.prompts().ViewInstructions(ctx, backend)
	if err != nil {
		return nil, err
	}
	described := map[string]any{"source": g.Source, "prompt_version": instructions.Version, "prompt_digest": instructions.Digest}
	if g.Source == ComposeFromRun {
		described["from_run"] = filepath.Base(g.Run)
	}
	return described, nil
}

// ViewRecord is a view case's document: what the call produced and what the
// metrics read.
type ViewRecord struct {
	Schema         string          `json:"schema"`
	Review         ComposedReview  `json:"review"`
	Composition    json.RawMessage `json:"composition"`
	Fallback       bool            `json:"fallback"`
	FallbackReason string          `json:"fallback_reason,omitempty"`
	Repaired       bool            `json:"repaired"`
	// Fixes counts what the host's checks fixed and the tiles it placed, by
	// kind; Problems are the checks' first reasons.
	Fixes    map[tiles.FixKind]int `json:"fixes,omitempty"`
	Problems []string              `json:"problems,omitempty"`
	// Diagrams counts the diagrams by type.
	Diagrams    map[string]int `json:"diagrams,omitempty"`
	Overview    bool           `json:"overview_diagram"`
	Verdict     bool           `json:"verdict"`
	RiskSummary bool           `json:"risk_summary"`
	Screenshots int            `json:"screenshots"`
	Intent      int            `json:"intent"`
	// Placed counts the tiles placed on sections, and Sections the sections
	// (context included) with any.
	Placed     int      `json:"placed"`
	Sections   int      `json:"sections"`
	InputBytes int      `json:"input_bytes"`
	InputCut   []string `json:"input_cut,omitempty"`
	Seconds    float64  `json:"seconds"`
}

func (g *ViewGenerator) Generate(ctx context.Context, base generate.Options, c Case) (Generated, error) {
	if g.reviews == nil {
		return Generated{}, errors.New("the view generator has no source")
	}
	document, which, err := g.reviews.review(c)
	if err != nil {
		return Generated{}, err
	}
	normalized, doc, err := converted(document)
	if err != nil {
		return Generated{}, fmt.Errorf("case %s: %w", c.ID, err)
	}
	started := time.Now()
	fills, authored := g.reviews.analysis(c, doc)
	result, err := agent.ViewRun(ctx, agent.ViewRequest{Completer: generate.CompleterOf(base.Backend), Backend: base.Backend.Name(), Prompts: g.prompts(),
		Document: normalized, Converted: doc, Fills: fills, Authored: authored, Redactor: g.reviews.redactor, CallTimeout: base.PassTimeout})
	out := Generated{Outcome: result.Outcome}
	if errors.Is(err, agent.ErrViewInterrupted) || ctx.Err() != nil {
		return out, &generate.Error{Category: generate.CategoryInterrupted, Err: errors.Join(err, ctx.Err())}
	}
	if err != nil {
		return out, err
	}
	record := viewRecordOf(result)
	record.Review, record.Seconds = which, time.Since(started).Seconds()
	instructions, _ := g.prompts().ViewInstructions(ctx, base.Backend.Name())
	out.PromptVersion, out.PromptDigest = instructions.Version, instructions.Digest
	if out.Document, err = json.MarshalIndent(record, "", "  "); err != nil {
		return out, err
	}
	out.Artifacts = map[string][]byte{"composition.json": record.Composition, "input.txt": []byte(result.Input)}
	served := tileview.Build(document, record.Composition, tiles.Options{})
	if out.Artifacts["view.json"], err = json.MarshalIndent(served.View, "", "  "); err != nil {
		return out, err
	}
	if len(result.Reply) > 0 {
		out.Artifacts["reply.json"] = result.Reply
	}
	if result.Fallback {
		out.Notes = append(out.Notes, "fallback")
	}
	if result.Repaired {
		out.Notes = append(out.Notes, "repaired")
	}
	out.Details = map[string]any{"fallback": result.Fallback, "fallback_reason": result.FallbackReason, "fixes": record.Fixes, "match": which.Match}
	return out, nil
}

// viewRecordOf measures a view call's outcome.
func viewRecordOf(result agent.ViewOutcome) ViewRecord {
	c := result.Composition
	record := ViewRecord{Schema: ViewRecordName, Fallback: result.Fallback, FallbackReason: result.FallbackReason, Repaired: result.Repaired,
		Fixes: result.Report.Counts(), Problems: result.Report.Problems, Overview: c.Overview.Diagram != nil, Verdict: c.Overview.Verdict != nil,
		RiskSummary: c.Overview.RiskSummary != "", Screenshots: len(c.Overview.Screenshots), Intent: len(c.Overview.Intent), InputBytes: len(result.Input)}
	record.Composition, _ = json.MarshalIndent(c, "", "  ")
	for _, d := range c.Diagrams {
		if record.Diagrams == nil {
			record.Diagrams = map[string]int{}
		}
		record.Diagrams[d.Type]++
	}
	for _, p := range c.Sections {
		if len(p.Tiles) > 0 {
			record.Sections++
			record.Placed += len(p.Tiles)
		}
	}
	if i := strings.Index(result.Input, "\n"); i >= 0 {
		var in struct {
			Cut []string `json:"cut"`
		}
		_ = json.Unmarshal([]byte(result.Input[i+1:]), &in)
		record.InputCut = in.Cut
	}
	return record
}

// Validate checks the record's composition as a host would before storing
// it (checkViewComposition).
func (g *ViewGenerator) Validate(document []byte, c Case) []string {
	var record ViewRecord
	if err := json.Unmarshal(document, &record); err != nil || record.Schema != ViewRecordName {
		return []string{"the document is not a view record"}
	}
	if g.reviews == nil {
		return []string{"the view generator has no source"}
	}
	raw, _, err := g.reviews.review(c)
	if err != nil {
		return []string{err.Error()}
	}
	return checkViewComposition(raw, record.Composition)
}

// checkViewComposition checks a review view composition of a review
// document as a host would before storing it: it passes its schema
// (schema/composition-2.json), checking it again against the review changes
// nothing, and the daemon serves a view from it.
func checkViewComposition(document, raw []byte) []string {
	c, err := view.Parse(raw)
	if err != nil {
		return []string{err.Error()}
	}
	normalized, doc, err := converted(document)
	if err != nil {
		return []string{err.Error()}
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		return []string{err.Error()}
	}
	var findings []string
	compiled, err := schema.CompileViewComposition()
	if err != nil {
		return []string{err.Error()}
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return []string{err.Error()}
	}
	if err := compiled.Validate(instance); err != nil {
		findings = append(findings, "the composition does not pass its schema: "+err.Error())
	}
	again, report := view.Normalize(r, doc, c)
	if report.Total() > 0 {
		findings = append(findings, fmt.Sprintf("checking the stored composition again fixes %d things", report.Total()))
	}
	againRaw, _ := json.Marshal(again)
	if served := tileview.Build(document, againRaw, tiles.Options{}); served.View == nil {
		findings = append(findings, "the daemon serves no view from it")
	}
	return findings
}

// The view metrics.
const (
	MetricViewFallback    = "view_fallback"
	MetricViewRepaired    = "view_repaired"
	MetricViewFixes       = "view_fixes"
	MetricViewDiagrams    = "view_diagrams"
	MetricViewOverview    = "view_overview_diagram"
	MetricViewVerdict     = "view_verdict"
	MetricViewRiskSummary = "view_risk_summary"
	MetricViewScreenshots = "view_screenshots"
	MetricViewIntent      = "view_intent"
	MetricViewPlaced      = "view_placed"
	MetricViewSections    = "view_sections_with_tiles"
	MetricViewBytes       = "view_input_bytes"
	MetricViewCut         = "view_input_cut"
	MetricViewSeconds     = "view_seconds"
	// metricViewFix prefixes the count of one kind of fix, and
	// metricViewDiagram the diagrams of one type.
	metricViewFix     = "view_fix_"
	metricViewDiagram = "view_diagrams_"
)

// DocumentMetrics measures a view record.
func (g *ViewGenerator) DocumentMetrics(document []byte) (map[string]float64, error) {
	var record ViewRecord
	if err := json.Unmarshal(document, &record); err != nil || record.Schema != ViewRecordName {
		return nil, errors.New("the document is not a view record")
	}
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	values := map[string]float64{
		MetricViewFallback: flag(record.Fallback), MetricViewRepaired: flag(record.Repaired), MetricViewOverview: flag(record.Overview),
		MetricViewVerdict: flag(record.Verdict), MetricViewRiskSummary: flag(record.RiskSummary), MetricViewScreenshots: float64(record.Screenshots),
		MetricViewIntent: float64(record.Intent), MetricViewPlaced: float64(record.Placed), MetricViewSections: float64(record.Sections),
		MetricViewBytes: float64(record.InputBytes), MetricViewCut: flag(len(record.InputCut) > 0), MetricViewSeconds: record.Seconds,
	}
	diagrams := 0
	for kind, n := range record.Diagrams {
		values[metricViewDiagram+kind] = float64(n)
		diagrams += n
	}
	values[MetricViewDiagrams] = float64(diagrams)
	total := 0
	for kind, n := range record.Fixes {
		values[metricViewFix+string(kind)] = float64(n)
		if kind != view.FixPlaced {
			total += n
		}
	}
	values[MetricViewFixes] = float64(total)
	return values, nil
}

// PairMetrics is not measured: a view run has no chains.
func (g *ViewGenerator) PairMetrics(_, _ []byte) (map[string]float64, error) {
	return nil, errors.New("view runs are not chained")
}

// Definitions says what each view metric means.
func (g *ViewGenerator) Definitions() map[string]string {
	return map[string]string{
		MetricViewFallback:    "1 when no reply was used (see the record's fallback_reason)",
		MetricViewRepaired:    "1 when an invalid reply needed the repair call",
		MetricViewFixes:       "fixes the host's checks made to the reply (view_fix_<kind> by kind; view_fix_placed counts the tiles the host placed and is not a fix)",
		MetricViewDiagrams:    "diagrams drawn (view_diagrams_<type> by type)",
		MetricViewOverview:    "1 when the overview has a main diagram",
		MetricViewVerdict:     "1 when the overview has a verdict",
		MetricViewRiskSummary: "1 when the overview has a risk summary",
		MetricViewScreenshots: "screenshots the overview shows",
		MetricViewIntent:      "session asks the overview sets against the pull request",
		MetricViewPlaced:      "tiles placed on sections, context included",
		MetricViewSections:    "sections, context included, with any tile",
		MetricViewBytes:       "bytes of the call's user message",
		MetricViewCut:         "1 when the input was cut to fit its budget",
		MetricViewSeconds:     "seconds the view call took",
	}
}
