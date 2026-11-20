package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/baum-ai/baum/agent/orchestrate"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/reviewv2"
)

// V2Generator writes a pull request's review as a baum.review/2 document in
// the levels of agent/orchestrate, as the desktop does with tile reviews on
// (generate.Options.V2), on a frozen case. Its document is the completed
// baum.review/2 document; beside it are:
//
//   - calls/<level>/<id>/prompt.md, input.txt and reply.json (reply-2.json
//     and so on for a repair, findings.txt when the host sent a reply
//     back): every call's instructions, the data after them and its reply;
//   - levels.json, a V2Record: calls, tokens, cost and seconds per level,
//     and what each call did.
//
// A case has no clone and no daemon, so its calls run over the unpacked
// trees and bundle.json, without baum's MCP server: the fallback a backend
// without MCP gets.
type V2Generator struct {
	PromptOverrides prompt.Overrides
	// Concurrency bounds a case's calls at once; zero is the orchestration's
	// default.
	Concurrency int
	// Reuse keeps the orchestration's reuse of the previous version's
	// unchanged sections; by default every section is written.
	Reuse bool

	mu sync.Mutex
	// previous is the document handed to a chained case (Next), by case id,
	// which Generate reads as the run's previous version.
	previous map[string]v2Previous
}

type v2Previous struct {
	document []byte
	version  int
}

var _ interface {
	Generator
	Chainer
	Deriver
	Meter
	ArtifactMeter
	Definer
} = (*V2Generator)(nil)

func init() {
	RegisterGenerator("v2", func(c GeneratorConfig) (Generator, error) {
		g := &V2Generator{Reuse: c.Reuse}
		for key, value := range c.Options {
			if key != "concurrency" {
				return nil, fmt.Errorf("the v2 generator takes the option concurrency, not %q", key)
			}
			if _, err := fmt.Sscanf(value, "%d", &g.Concurrency); err != nil || g.Concurrency < 1 {
				return nil, fmt.Errorf("concurrency %q is not a positive number", value)
			}
		}
		for id, text := range c.PromptOverrides {
			if !slices.Contains(prompt.V2ReviewIDs(), prompt.ID(id)) {
				return nil, fmt.Errorf("prompt override %q is not a baum.review/2 prompt; use one of %v", id, prompt.V2ReviewIDs())
			}
			if err := prompt.Validate(prompt.ID(id), text); err != nil {
				return nil, fmt.Errorf("prompt override %s: %w", id, err)
			}
			if g.PromptOverrides == nil {
				g.PromptOverrides = prompt.Overrides{}
			}
			g.PromptOverrides[prompt.ID(id)] = text
		}
		return g, nil
	})
	RegisterMeter("v2", &V2Generator{})
}

func (g *V2Generator) Name() string { return "v2" }

func (g *V2Generator) Describe(ctx context.Context, backend string) (map[string]any, error) {
	p, err := prompt.V2Prompts{Overrides: g.PromptOverrides}.ReviewV2Instructions(ctx, backend)
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompt_version": p.Version, "prompt_digest": p.Digest, "reuse": g.Reuse, "concurrency": g.Concurrency}, nil
}

// V2RecordName is the schema of levels.json.
const V2RecordName = "replay.v2/1"

// V2Record is levels.json: what each level spent and did.
type V2Record struct {
	Schema     string                    `json:"schema"`
	Levels     map[string]V2Level        `json:"levels"`
	Generation reviewv2.Generation       `json:"generation"`
	Repairs    int                       `json:"repairs"`
	ShellOff   string                    `json:"shell_off,omitempty"`
	Calls      map[string]map[string]int `json:"calls_by_level"`
}

// V2Level is one level's spending.
type V2Level struct {
	Calls   int      `json:"calls"`
	Tokens  int      `json:"tokens"`
	Repairs int      `json:"repairs"`
	Seconds float64  `json:"seconds"`
	CostUSD *float64 `json:"cost_usd"`
	Ran     int      `json:"ran"`
	Failed  int      `json:"failed"`
	Reused  int      `json:"reused"`
	Skipped int      `json:"skipped"`
}

func (g *V2Generator) Generate(ctx context.Context, o generate.Options, c Case) (Generated, error) {
	var mu sync.Mutex
	artifacts := map[string][]byte{}
	record := func(call orchestrate.Call) {
		mu.Lock()
		defer mu.Unlock()
		dir := fmt.Sprintf("calls/%s/%s/", call.Level, call.ID)
		reply := "reply.json"
		if call.Pass > 1 {
			reply = fmt.Sprintf("reply-%d.json", call.Pass)
		}
		if _, ok := artifacts[dir+"prompt.md"]; !ok {
			artifacts[dir+"prompt.md"] = []byte(call.Prompt)
			artifacts[dir+"input.txt"] = []byte(call.Input)
		}
		artifacts[dir+reply] = call.Reply
		if call.Findings != "" {
			artifacts[dir+fmt.Sprintf("findings-%d.txt", call.Pass)] = []byte(call.Findings)
		}
	}
	o = g.options(o, c, record)
	result, err := generate.Run(ctx, o)
	out := Generated{Outcome: result.Outcome}
	if result.V2 != nil {
		out.PromptVersion, out.PromptDigest = result.V2.PromptVersion, result.V2.PromptDigest
		levels := V2Record{Schema: V2RecordName, Levels: map[string]V2Level{}, Generation: result.V2.Generation, Repairs: result.V2.ValidationRepairs, ShellOff: result.V2.ShellOff}
		list := o.PriceList
		if list == nil {
			list = pricing.Active()
		}
		for name, l := range result.V2.Levels {
			level := V2Level{Calls: l.Calls, Tokens: l.Tokens, Repairs: l.ValidationRepairs, Seconds: l.Seconds, Ran: l.Ran, Failed: l.Failed, Reused: l.Reused, Skipped: l.Skipped}
			if l.Calls > 0 {
				level.CostUSD = list.BackendOutcomeCost(o.Backend.Name(), cmpOr(l.Model, result.Outcome.Model), l.Outcome)
			}
			levels.Levels[name] = level
		}
		encoded, _ := json.MarshalIndent(levels, "", "  ")
		mu.Lock()
		artifacts["levels.json"] = encoded
		mu.Unlock()
	}
	mu.Lock()
	out.Artifacts = maps.Clone(artifacts)
	mu.Unlock()
	if err != nil {
		return out, err
	}
	out.Document = result.Document
	out.BundleDigest = result.Metadata.BundleDigest
	out.Notes = slices.Clone(result.FailedSections)
	if result.ContextFailed {
		out.Notes = append(out.Notes, "context")
	}
	out.Details = map[string]any{"failed_sections": result.FailedSections, "context_failed": result.ContextFailed}
	return out, nil
}

// options is the run's options for c: the v2 levels, every section written
// unless Reuse, and the document Next handed c as its previous version.
func (g *V2Generator) options(o generate.Options, c Case, record func(orchestrate.Call)) generate.Options {
	o.Staged, o.NoReuse = true, !g.Reuse
	o.Tiles = &generate.Tiles{}
	o.V2 = &generate.V2{Prompts: prompt.V2Prompts{Overrides: g.PromptOverrides}, Record: record, NoReuse: !g.Reuse, Concurrency: g.Concurrency}
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.previous[c.ID]; ok {
		o.V2.Previous, o.V2.PreviousVersion = p.document, p.version
	}
	return o
}

// Next implements Chainer: the previous revision's document becomes the run's
// previous version (generate.V2.Previous and PreviousVersion, as Publish sets
// them), which Generate reads by case id. The bundle carries no previous
// version, as the app's does not for a v2 run, so the version written is the
// document's own plus one.
func (g *V2Generator) Next(c Case, document []byte) (bundle.Bundle, error) {
	doc, err := reviewv2.Read(document)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("case %s: the previous document is not a review: %w", c.ID, err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(doc.Review, &header); err != nil || header.Version < 1 {
		return bundle.Bundle{}, fmt.Errorf("case %s: the previous document has no review version", c.ID)
	}
	b, err := c.FreshBundle()
	if err != nil {
		return bundle.Bundle{}, err
	}
	b.Previous = nil
	g.mu.Lock()
	if g.previous == nil {
		g.previous = map[string]v2Previous{}
	}
	g.previous[c.ID] = v2Previous{document: document, version: header.Version}
	g.mu.Unlock()
	return b, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Validate checks the document is a baum.review/2 document whose sections
// cover the case's hunks.
func (g *V2Generator) Validate(document []byte, c Case) []string {
	var doc reviewv2.Document
	if err := json.Unmarshal(document, &doc); err != nil {
		return []string{"the document is not a baum.review/2 document: " + err.Error()}
	}
	var findings []string
	if doc.Schema != reviewv2.Name {
		findings = append(findings, "schema is "+doc.Schema)
	}
	if len(doc.Sections) == 0 {
		findings = append(findings, "the document has no section")
	}
	b, err := c.FreshBundle()
	if err != nil {
		return append(findings, err.Error())
	}
	covered := map[string]bool{}
	for _, s := range doc.Sections {
		for _, h := range s.Hunks {
			covered[h] = true
		}
	}
	for _, h := range b.Diff {
		if !covered[h.ID] {
			findings = append(findings, "hunk "+h.ID+" is in no section")
		}
	}
	return findings
}

// The v2 metrics.
const (
	MetricV2Sections       = "v2_sections"
	MetricV2Logical        = "v2_logical_sections"
	MetricV2SectionsFailed = "v2_sections_failed"
	MetricV2Tiles          = "v2_tiles"
	MetricV2TilesReady     = "v2_tiles_ready"
	MetricV2TilesFailed    = "v2_tiles_failed"
	MetricV2ContextMissing = "v2_context_missing"
	MetricV2OverviewFailed = "v2_overview_failed"
	MetricV2Blocks         = "v2_blocks"
	MetricV2Points         = "v2_points"
	MetricV2Risks          = "v2_risks"
	MetricV2Repairs        = "v2_repairs"
)

// DocumentMetrics measures a baum.review/2 document: its sections, tiles by
// type and state, and what failed.
func (g *V2Generator) DocumentMetrics(document []byte) (map[string]float64, error) {
	var doc reviewv2.Document
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, err
	}
	m := map[string]float64{MetricV2Sections: float64(len(doc.Sections)), MetricV2Tiles: float64(len(doc.Tiles))}
	for _, s := range doc.Sections {
		if s.Kind == "logical" {
			m[MetricV2Logical]++
		}
		if s.Details == "failed" {
			m[MetricV2SectionsFailed]++
		}
		body := s.Body
		m[MetricV2Blocks] += float64(len(body))
		for _, b := range body {
			m[MetricV2Points] += float64(len(b.Items))
		}
		m[MetricV2Risks] += float64(len(s.Risks))
	}
	for _, t := range doc.Tiles {
		m["v2_tiles_type_"+t.Type]++
		switch t.State {
		case reviewv2.TileReady:
			m[MetricV2TilesReady]++
		case reviewv2.TileFailed:
			m[MetricV2TilesFailed]++
		}
	}
	if doc.Context == nil {
		m[MetricV2ContextMissing] = 1
	}
	if doc.Generation != nil && doc.Generation.Overview.Status == orchestrate.StatusFailed {
		m[MetricV2OverviewFailed] = 1
	}
	return m, nil
}

// ArtifactMetrics reads levels.json: calls, tokens, cost and seconds per
// level.
func (g *V2Generator) ArtifactMetrics(read func(string) ([]byte, error)) (map[string]float64, error) {
	raw, err := read("levels.json")
	if err != nil {
		return nil, errors.New("the case has no levels.json")
	}
	var record V2Record
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != V2RecordName {
		return nil, errors.New("levels.json is not a v2 record")
	}
	m := map[string]float64{MetricV2Repairs: float64(record.Repairs)}
	for _, name := range orchestrate.Levels {
		l := record.Levels[name]
		m["v2_"+name+"_calls"] = float64(l.Calls)
		m["v2_"+name+"_tokens"] = float64(l.Tokens)
		m["v2_"+name+"_seconds"] = l.Seconds
		m["v2_"+name+"_failed"] = float64(l.Failed)
		if l.CostUSD != nil {
			m["v2_"+name+"_cost_usd"] = *l.CostUSD
		}
	}
	return m, nil
}

func (g *V2Generator) Definitions() map[string]string {
	d := map[string]string{
		MetricV2Sections: "sections of the document", MetricV2Logical: "logical sections", MetricV2SectionsFailed: "sections whose agent failed",
		MetricV2Tiles: "tiles of the document", MetricV2TilesReady: "tiles ready", MetricV2TilesFailed: "tiles that failed",
		MetricV2ContextMissing: "1 when the document has no context", MetricV2OverviewFailed: "1 when the overview fell back",
		MetricV2Blocks: "body blocks over every section", MetricV2Points: "points over every section's points blocks", MetricV2Risks: "risks over every section", MetricV2Repairs: "validation repairs over every call",
	}
	for _, name := range orchestrate.Levels {
		d["v2_"+name+"_calls"] = "model calls of the " + name + " level"
		d["v2_"+name+"_tokens"] = "tokens of the " + name + " level"
		d["v2_"+name+"_seconds"] = "wall seconds of the " + name + " level"
		d["v2_"+name+"_failed"] = "calls of the " + name + " level that failed"
		d["v2_"+name+"_cost_usd"] = "what the " + name + " level cost at API prices"
	}
	maps.Copy(d, v2PairDefinitions)
	return d
}
