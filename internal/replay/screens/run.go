// Package screens reads the pictures and first-screen facts `just
// replay-screens` takes of a replay run's layouts, and builds on them: static
// contact sheets (sheet.go), a pairwise judge that compares two layouts of the
// same review from their pictures (judge.go), and a page and file format for
// an owner's own verdicts, with the agreement between those and a judge's
// (labels.go). Everything it writes holds private content and stays in the
// run directory or a directory the caller names; see "Pictures of a run" in
// cmd/replay/README.md.
package screens

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/baum-ai/baum/internal/replay"
)

// The layouts a run's pictures show: its own composition, and the template
// of the change kind for the same review.
const (
	Composed = "composed"
	Template = "template"
)

// Dir is the directory of a run's pictures.
const Dir = "screens"

// Signal is a must-know item and whether the first screen shows it.
type Signal struct {
	ID      string `json:"id"`
	Visible bool   `json:"visible"`
}

// Placed is a tile card the first screen shows: wholly ("full") or cut by
// the window's edge or a clipping region ("partial").
type Placed struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Size    string `json:"size"`
	Visible string `json:"visible"`
}

// FirstScreen is what the first screen of a tiles page shows, as the
// replay-screens spec measured it.
type FirstScreen struct {
	Width              int      `json:"width"`
	Height             int      `json:"height"`
	PageHeight         int      `json:"pageHeight"`
	Tiles              []Placed `json:"tiles"`
	LandingTiles       int      `json:"landingTiles"`
	BelowTiles         int      `json:"belowTiles"`
	WastedUnits        int      `json:"wastedUnits"`
	WastedUnitsLanding int      `json:"wastedUnitsLanding"`
	Recommendation     struct {
		Given   bool `json:"given"`
		Visible bool `json:"visible"`
	} `json:"recommendation"`
	SevereRisks  []Signal `json:"severeRisks"`
	NamedChanges []Signal `json:"namedChanges"`
	// RiskiestChanges are the current changes rated highest (level 3 or
	// more), MediumRisks the first three medium risks the Risks tile ranks.
	RiskiestChanges []Signal `json:"riskiestChanges"`
	MediumRisks     []Signal `json:"mediumRisks"`
	Breaking     struct {
		Given   bool `json:"given"`
		Visible bool `json:"visible"`
	} `json:"breaking"`
	MissingEvidence struct {
		Count   int  `json:"count"`
		Visible bool `json:"visible"`
	} `json:"missingEvidence"`
	Recall *float64 `json:"recall"`
}

// Issue is something the spec found wrong with a page.
type Issue struct {
	Where     string `json:"where"`
	What      string `json:"what"`
	Truncated string `json:"truncated,omitempty"`
}

// Case is one case's pictures and facts (screens/<case>/first-screen.json).
type Case struct {
	ID       string `json:"case"`
	Size     string `json:"size"`
	Version  int    `json:"version"`
	Viewport struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"viewport"`
	// Variants are the first screens at the measured size, by layout; Phone
	// the first screens on a phone.
	Variants map[string]FirstScreen `json:"variants"`
	Phone    map[string]FirstScreen `json:"phone"`
	Issues   []Issue                `json:"issues"`
}

// Run is a run directory with its pictures.
type Run struct {
	Dir      string
	Manifest replay.Manifest
	// Cases are the photographed cases in the run's order; Problems the
	// cases the fixture could not show, with why.
	Cases    []Case
	Problems []Problem
	// ConsoleErrors are the page errors the spec did not expect.
	ConsoleErrors []string
}

// Problem is a case that has no pictures.
type Problem struct {
	ID      string `json:"id"`
	Problem string `json:"problem"`
}

// Load reads a run's manifest and its pictures' facts. A run without
// pictures is an error that says how to take them.
func Load(dir string) (*Run, error) {
	manifest, err := replay.ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, Dir, "screens.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no pictures; take them with just replay-screens <dataset> %s", dir, dir)
	}
	if err != nil {
		return nil, err
	}
	var summary struct {
		Cases         []string  `json:"cases"`
		Problems      []Problem `json:"problems"`
		ConsoleErrors []string  `json:"unexpectedConsoleErrors"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, fmt.Errorf("%s: screens.json: %w", dir, err)
	}
	run := &Run{Dir: dir, Manifest: manifest, Problems: summary.Problems, ConsoleErrors: summary.ConsoleErrors}
	for _, id := range summary.Cases {
		raw, err := os.ReadFile(filepath.Join(dir, Dir, id, "first-screen.json"))
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", id, err)
		}
		var c Case
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("case %s: first-screen.json: %w", id, err)
		}
		run.Cases = append(run.Cases, c)
	}
	return run, nil
}

// Name is the run's name.
func (r *Run) Name() string {
	if r.Manifest.Run != "" {
		return r.Manifest.Run
	}
	return filepath.Base(r.Dir)
}

// Case returns the run's case of an id.
func (r *Run) Case(id string) (Case, bool) {
	for _, c := range r.Cases {
		if c.ID == id {
			return c, true
		}
	}
	return Case{}, false
}

// Picture is the path of a case's picture of a layout at a width, the
// viewport's or the full page's. The full page's falls back to the
// viewport's when the page fit in the window.
func (r *Run) Picture(id, variant string, width int, full bool) string {
	base := filepath.Join(r.Dir, Dir, id, variant+"-"+strconv.Itoa(width)+"-light")
	if full {
		if _, err := os.Stat(base + "-full.png"); err == nil {
			return base + "-full.png"
		}
	}
	return base + ".png"
}

// Side is one layout of a pair: a run's composed or template layout.
type Side struct {
	Run     *Run
	Variant string
}

// Label names a side: the variant, and the run's model for a composition.
func (s Side) Label() string {
	if s.Variant == Template {
		return "template"
	}
	if s.Run.Manifest.Model != "" {
		return "composed (" + s.Run.Manifest.Model + ")"
	}
	return "composed (" + s.Run.Name() + ")"
}

// Pairing is two sides compared case by case: a run's composition against
// its template, or the compositions of two runs of the same cases.
type Pairing struct {
	X, Y Side
}

// Pairs pairs a run's composed layout with its template, or, given another
// run, with that run's composed layout.
func Pairs(run, against *Run) Pairing {
	if against == nil {
		return Pairing{X: Side{run, Composed}, Y: Side{run, Template}}
	}
	return Pairing{X: Side{run, Composed}, Y: Side{against, Composed}}
}

// Cases are the ids both sides have pictures of, in X's order.
func (p Pairing) Cases() []string {
	var ids []string
	for _, c := range p.X.Run.Cases {
		y, ok := p.Y.Run.Case(c.ID)
		if !ok {
			continue
		}
		if _, ok := c.Variants[p.X.Variant]; !ok {
			continue
		}
		if _, ok := y.Variants[p.Y.Variant]; !ok {
			continue
		}
		ids = append(ids, c.ID)
	}
	return ids
}

// Facts returns a case's first screen for a side, on a phone or not.
func (s Side) Facts(id string, phone bool) (FirstScreen, bool) {
	c, ok := s.Run.Case(id)
	if !ok {
		return FirstScreen{}, false
	}
	if phone {
		f, ok := c.Phone[s.Variant]
		return f, ok
	}
	f, ok := c.Variants[s.Variant]
	return f, ok
}
