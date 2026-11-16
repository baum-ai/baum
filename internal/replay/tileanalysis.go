package replay

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// AnalysisRecordName is the schema of analysis.json, what a tiles case's
// tile analysis did.
const AnalysisRecordName = "baum.replay.tile-analysis/1"

// AnalysisRecord is analysis.json: the outline's library selection, each
// fill and the exhibits with their cost and time, where the served page
// placed their tiles, and the duplicated points that involve them.
type AnalysisRecord struct {
	Schema        string            `json:"schema"`
	PromptVersion string            `json:"prompt_version,omitempty"`
	PromptDigest  string            `json:"prompt_digest,omitempty"`
	Selection     []agent.Selection `json:"selection"`
	Fills         []FillRecord      `json:"fills"`
	Exhibits      *ExhibitsRecord   `json:"exhibits,omitempty"`
	// WaitedSeconds is how long the review waited for the fills and
	// exhibits once its own stages had ended.
	WaitedSeconds float64 `json:"waited_seconds"`
	// Duplicates are the pairs of points two tiles state, one of them a
	// library or authored tile (view.Duplicates on the served page).
	Duplicates []view.Pair `json:"duplicates,omitempty"`
}

// FillRecord is one fill: its state, reason, cost, time and placement.
type FillRecord struct {
	Type      string     `json:"type"`
	Hunks     int        `json:"hunks"`
	Why       string     `json:"why,omitempty"`
	State     string     `json:"state"`
	Reason    string     `json:"reason,omitempty"`
	Repaired  bool       `json:"repaired,omitempty"`
	Reused    bool       `json:"reused,omitempty"`
	Seconds   float64    `json:"seconds"`
	CostUSD   *float64   `json:"cost_usd,omitempty"`
	Calls     int        `json:"calls"`
	Placement *Placement `json:"placement,omitempty"`
}

// ExhibitsRecord is the exhibits call: what it returned, kept and dropped.
type ExhibitsRecord struct {
	Candidates int                    `json:"candidates"`
	Called     bool                   `json:"called"`
	Reused     []string               `json:"reused,omitempty"`
	Returned   int                    `json:"returned"`
	Checked    int                    `json:"checked"`
	Dropped    []agent.DroppedExhibit `json:"dropped,omitempty"`
	// Duplicates are the exhibits dropped as restating the review; Kept those
	// the composition holds, with their placement.
	Duplicates []string             `json:"duplicates,omitempty"`
	Kept       map[string]Placement `json:"kept,omitempty"`
	Adds       map[string]string    `json:"adds,omitempty"`
	Reason     string               `json:"reason,omitempty"`
	Seconds    float64              `json:"seconds"`
	CostUSD    *float64             `json:"cost_usd,omitempty"`
	Calls      int                  `json:"calls"`
}

// Placement is where the served page put a tile: the section it is on
// (context for Where this fits), or the overview as its main diagram.
type Placement struct {
	Section  string `json:"section,omitempty"`
	Overview bool   `json:"overview,omitempty"`
}

// onSection reports whether the tile is on one of the review's own
// sections, not on Where this fits.
func (p Placement) onSection() bool { return p.Section != "" && p.Section != view.ContextSection }

// placements returns where the served view places each tile.
func placements(v *view.View) map[string]Placement {
	out := map[string]Placement{}
	if v == nil {
		return out
	}
	if d := v.Overview.Diagram; d != nil {
		out[*d] = Placement{Overview: true}
	}
	for _, p := range v.Sections {
		for _, id := range p.Tiles {
			out[id] = Placement{Section: p.ID}
		}
	}
	return out
}

// analysisArtifacts writes analysis.json, fills/<type>.json and
// exhibits.json for a case whose run had a tile analysis.
func analysisArtifacts(artifacts map[string][]byte, report *generate.TilesReport, converted *tiles.Document, served *view.View, composition *view.Composition, list *pricing.List, backend string) error {
	if report == nil || report.TilesOutcome == nil {
		return nil
	}
	t := report.TilesOutcome
	placed := placements(served)
	record := AnalysisRecord{Schema: AnalysisRecordName, PromptVersion: t.PromptVersion, PromptDigest: t.PromptDigest,
		Selection: t.Selection, Fills: []FillRecord{}, WaitedSeconds: t.Waited.Seconds()}
	for _, f := range t.Fills {
		r := FillRecord{Type: f.Type, Hunks: len(f.Hunks), Why: f.Why, State: f.State, Reason: f.Reason, Repaired: f.Repaired, Reused: f.Reused,
			Seconds: f.Elapsed.Seconds(), CostUSD: list.BackendOutcomeCost(backend, f.Outcome.Model, f.Outcome), Calls: f.Outcome.Calls}
		if p, ok := placed[tiles.LibraryTileID(f.Type)]; ok {
			r.Placement = &p
		}
		record.Fills = append(record.Fills, r)
		encoded, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		artifacts["fills/"+f.Type+".json"] = encoded
	}
	only := map[string]bool{}
	if composition != nil {
		for _, lib := range composition.Library {
			var def struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(lib.Definition, &def)
			only[tiles.LibraryTileID(def.Name)] = true
		}
		for _, a := range composition.Authored {
			only[a.ID] = true
		}
	}
	if x := t.Exhibits; x != nil {
		r := &ExhibitsRecord{Candidates: x.Candidates, Called: x.Called, Reused: x.Reused, Returned: x.Returned, Checked: len(x.Tiles), Dropped: x.Dropped, Duplicates: report.Duplicates, Adds: x.Adds,
			Reason: x.Reason, Seconds: x.Elapsed.Seconds(), CostUSD: list.BackendOutcomeCost(backend, x.Outcome.Model, x.Outcome), Calls: x.Outcome.Calls}
		if composition != nil {
			for _, a := range composition.Authored {
				if r.Kept == nil {
					r.Kept = map[string]Placement{}
				}
				r.Kept[a.ID] = placed[a.ID]
			}
		}
		record.Exhibits = r
		encoded, err := json.MarshalIndent(x, "", "  ")
		if err != nil {
			return err
		}
		artifacts["exhibits.json"] = encoded
	}
	if converted != nil && served != nil && len(only) > 0 {
		units := view.DocumentUnits(converted)
		for _, t := range served.Tiles {
			if only[t.ID] {
				units = append(units, view.TileUnits(tiles.Tile{ID: t.ID, Type: t.Type, Data: t.Data})...)
			}
		}
		record.Duplicates = view.Duplicates(units, only)
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	artifacts["analysis.json"] = encoded
	return nil
}

// The tile analysis metrics. The per-type ones are named with the type:
// library_<type>_selected and so on.
const (
	MetricLibrarySelected    = "library_selected"
	MetricLibraryReady       = "library_ready"
	MetricLibraryOnSection   = "library_on_section"
	MetricFillsCost          = "fills_cost_usd"
	MetricFillsSeconds       = "fills_seconds_max"
	MetricLibraryReused      = "library_reused"
	MetricExhibitsCandidates = "exhibits_candidates"
	MetricExhibitsCalled     = "exhibits_called"
	MetricExhibitsReused     = "exhibits_reused"
	MetricExhibitsReturned   = "exhibits_returned"
	MetricExhibitsKept       = "exhibits_kept"
	MetricExhibitsDuplicate  = "exhibits_dropped_duplicate"
	MetricExhibitsOnSection  = "exhibits_on_section"
	MetricExhibitsCost       = "exhibits_cost_usd"
	MetricExhibitsSeconds    = "exhibits_seconds"
	MetricExtrasWait         = "extras_wait_seconds"
	MetricDuplicatePairs     = "extras_duplicate_pairs"
	MetricHasDuplicate       = "extras_has_duplicate"
)

// analysisMetrics adds the metrics of analysis.json, when the case has one.
func analysisMetrics(read func(string) ([]byte, error), values map[string]float64) error {
	raw, err := read("analysis.json")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nil
	}
	var r AnalysisRecord
	if json.Unmarshal(raw, &r) != nil || r.Schema != AnalysisRecordName {
		return errors.New("analysis.json is not a tile analysis record")
	}
	add := func(name string, v float64) { values[name] += v }
	one := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	for _, s := range r.Selection {
		add("library_"+s.Type+"_selected", one(s.Applies))
		add(MetricLibrarySelected, one(s.Applies))
	}
	fillsSeconds := 0.0
	for _, f := range r.Fills {
		prefix := "library_" + f.Type + "_"
		add(prefix+"ready", one(f.State == agent.FillReady))
		add(prefix+"not_applies", one(f.State == agent.FillNotApplies))
		add(prefix+"dropped", one(f.State == agent.FillDropped))
		add(prefix+"repaired", one(f.Repaired))
		add(prefix+"reused", one(f.Reused))
		add(MetricLibraryReused, one(f.Reused))
		add(MetricLibraryReady, one(f.State == agent.FillReady))
		onSection := f.Placement != nil && f.Placement.onSection()
		add(prefix+"on_section", one(onSection))
		add(MetricLibraryOnSection, one(onSection))
		if f.CostUSD != nil {
			add(MetricFillsCost, *f.CostUSD)
		}
		fillsSeconds = max(fillsSeconds, f.Seconds)
	}
	values[MetricFillsSeconds] = fillsSeconds
	if x := r.Exhibits; x != nil {
		add(MetricExhibitsReturned, float64(x.Returned))
		add(MetricExhibitsCandidates, float64(x.Candidates))
		add(MetricExhibitsCalled, one(x.Called))
		add(MetricExhibitsReused, float64(len(x.Reused)))
		add(MetricExhibitsKept, float64(len(x.Kept)))
		add(MetricExhibitsDuplicate, float64(len(x.Duplicates)))
		for _, d := range x.Dropped {
			add("exhibits_dropped_"+d.Reason, 1)
		}
		for _, p := range x.Kept {
			add(MetricExhibitsOnSection, one(p.onSection()))
		}
		if x.CostUSD != nil {
			add(MetricExhibitsCost, *x.CostUSD)
		}
		values[MetricExhibitsSeconds] = x.Seconds
	}
	values[MetricExtrasWait] = r.WaitedSeconds
	values[MetricDuplicatePairs] = float64(len(r.Duplicates))
	values[MetricHasDuplicate] = one(len(r.Duplicates) > 0)
	return nil
}

func analysisDefinitions() map[string]string {
	d := map[string]string{
		MetricLibrarySelected:    "library types the outline selected",
		MetricLibraryReady:       "library fills whose data passed its checks",
		MetricLibraryOnSection:   "library tiles the served page places on one of the review's sections (not Where this fits)",
		MetricFillsCost:          "what the fills cost at API prices (cost_usd includes it)",
		MetricFillsSeconds:       "the longest fill's wall time",
		MetricExhibitsReturned:   "exhibits the exhibits call returned",
		MetricLibraryReused:      "library fills taken from the previous version without a call",
		MetricExhibitsCandidates: "exhibit candidates the outline proposed",
		MetricExhibitsCalled:     "1 when the exhibits call ran (a candidate and room left)",
		MetricExhibitsReused:     "authored tiles kept from the previous version without a call",
		MetricExhibitsKept:       "authored tiles the composition holds",
		MetricExhibitsDuplicate:  "exhibits dropped as restating the review or another exhibit",
		MetricExhibitsOnSection:  "authored tiles the served page places on one of the review's sections (not Where this fits)",
		MetricExhibitsCost:       "what the exhibits call cost at API prices (cost_usd includes it)",
		MetricExhibitsSeconds:    "the exhibits call's wall time",
		MetricExtrasWait:         "how long the review waited for fills and exhibits after its own stages ended",
		MetricDuplicatePairs:     "pairs of points two tiles state where one is a library or authored tile",
		MetricHasDuplicate:       "1 when the page has any such pair",
	}
	for _, name := range library.Names {
		for _, part := range []string{"selected", "ready", "not_applies", "dropped", "repaired", "reused", "landing"} {
			d["library_"+name+"_"+part] = "the " + name + " type: " + part
		}
	}
	for _, reason := range []string{agent.ExhibitShape, agent.ExhibitCitation, agent.ExhibitSize, agent.ExhibitCode, agent.ExhibitLimit} {
		d["exhibits_dropped_"+reason] = "exhibits dropped by the host's checks: " + reason
	}
	return d
}
