// Package view is a review's page view: the stored review view composition
// (baum.composition/2, schema/composition-2.json) that says how a generated
// review is presented on its review page, the native tiles the view call
// writes, the checks a host runs on both, and the view the daemon serves
// (baum.view/1) beside the review.
//
// The page has three tabs. The overview is a fixed layout of the review's
// summary with the composition's model-written parts (a risk summary, a
// verdict, screenshots, a main diagram, what the linked sessions asked
// for); "How it works" lists the review's sections, each with the tiles the
// composition places on it; "Code" is the diff. Tiles are the
// model-customisable parts: native tiles (flow, columns, sequence, table)
// the app draws with its own components, and the library and authored tiles
// of the tile analysis, which keep drawing as custom and generic tiles.
package view

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

// CompositionName is the value of a review view composition's "schema"
// field.
const CompositionName = schema.ViewCompositionName

// Name is the value of a served view's "schema" field.
const Name = "baum.view/1"

// ContextSection is the ID of "Where this fits", the review's summary
// context, which tiles may be placed on like a section.
const ContextSection = "context"

// Composition is a stored review view composition. The overview, the
// sections' placements and the diagrams are the view call's; the library
// and authored tiles are the tile analysis's, as in baum.composition/1.
// Only the host writes the generator.
type Composition struct {
	Schema    string           `json:"schema"`
	Generator *tiles.Generator `json:"generator,omitempty"`
	Overview  Overview         `json:"overview"`
	// Sections place tiles on the review's sections and on ContextSection,
	// in order; a section with no tiles may be left out.
	Sections []Placement          `json:"sections"`
	Diagrams []NativeTile         `json:"diagrams,omitempty"`
	Library  []tiles.LibraryTile  `json:"library,omitempty"`
	Authored []tiles.AuthoredTile `json:"authored,omitempty"`
}

// Overview holds the overview tab's model-written parts. Every one is
// optional: the page falls back to the review's own.
type Overview struct {
	// RiskSummary says why the risk score is what it is, in at most two
	// sentences.
	RiskSummary string `json:"risk_summary,omitempty"`
	// Verdict is set only when the reader must do something before merging.
	Verdict *Verdict `json:"verdict"`
	// Screenshots are attachment IDs of the review's index.
	Screenshots []string `json:"screenshots"`
	// Diagram is the overview's main diagram, a tile of the composition.
	Diagram *string  `json:"diagram"`
	Intent  []Intent `json:"intent"`
}

// Verdict is one line and the points it rests on, each saying what the
// problem is and what to do, optionally linking to its section.
type Verdict struct {
	Text   string  `json:"text"`
	Points []Point `json:"points"`
}

// Point is one point of a verdict.
type Point struct {
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// Intent is what a person asked in a linked session (its prompt, by index)
// against what the pull request does.
type Intent struct {
	Asked   string `json:"asked"`
	Got     string `json:"got"`
	Session string `json:"session"`
	Prompt  int    `json:"prompt"`
}

// Placement is the tiles placed on a section or on ContextSection, in order.
type Placement struct {
	ID    string   `json:"id"`
	Tiles []string `json:"tiles"`
}

// The limits of a composition.
const (
	MaxRiskSummary   = 240
	MaxVerdictText   = 120
	MaxVerdictPoints = 3
	MaxPointText     = 200
	MaxScreenshots   = 3
	MaxIntent        = 3
	MaxIntentText    = 160
	MaxDiagrams      = 8
	MaxTitle         = 80
	MaxCaption       = 200
	MaxSource        = 20
	// MaxPlaced is the most tiles the view call places on one section; the
	// host may place more when it places the tiles the call left out
	// (Place).
	MaxPlaced = 3
)

// MaxStored is the most bytes of a composition a host stores or reads.
const MaxStored = tiles.MaxStoredComposition

// ErrOtherSchema is returned by Parse for a composition of another schema,
// such as a baum.composition/1, which the view does not read.
var ErrOtherSchema = errors.New("view: the composition is not a baum.composition/2")

// Parse decodes a stored review view composition. It fails on invalid JSON
// and on another schema (ErrOtherSchema); members it does not know are
// ignored. It does not check the composition against its review: Normalize
// does.
func Parse(raw []byte) (*Composition, error) {
	if len(raw) > MaxStored {
		return nil, fmt.Errorf("view: the composition is larger than %d bytes", MaxStored)
	}
	var c Composition
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("view: decoding the composition: %w", err)
	}
	if c.Schema != CompositionName {
		return nil, ErrOtherSchema
	}
	return &c, nil
}

// IsComposition reports whether raw is a review view composition by its
// schema field, without checking anything else.
func IsComposition(raw []byte) bool {
	var head struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(raw, &head) == nil && head.Schema == CompositionName
}

// Grid is the composition as the tile grid reads it: its library and
// authored tiles, with no layout, which the grid lays out with the change
// kind's template (tiles.Composition.View).
func (c *Composition) Grid() *tiles.Composition {
	return &tiles.Composition{Schema: tiles.CompositionName, Generator: c.Generator, Library: c.Library, Authored: c.Authored, View: true}
}

// placed returns every tile the composition places, the overview's diagram
// first.
func (c *Composition) placed() map[string]bool {
	out := map[string]bool{}
	if c.Overview.Diagram != nil {
		out[*c.Overview.Diagram] = true
	}
	for _, p := range c.Sections {
		for _, id := range p.Tiles {
			out[id] = true
		}
	}
	return out
}
