package view

import (
	"encoding/json"
	"slices"

	"github.com/baum-ai/baum/tiles"
)

// View is the review page's view as the daemon serves it beside the review
// (baum.view/1): the overview's model-written parts, every section with the
// tiles placed on it (context first, then the review's sections in order),
// and the tiles.
type View struct {
	Schema   string      `json:"schema"`
	Overview Overview    `json:"overview"`
	Sections []Placement `json:"sections"`
	Tiles    []Tile      `json:"tiles"`
}

// Tile is a tile of the view: a native tile (flow, columns, sequence,
// table) with its data, an authored tile (table, code, prose) with the data
// of today's generic tiles, or a library or chat-made tile (custom) with its
// served definition, drawn in its sandboxed frame.
type Tile struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
	// State is ready, pending (a library tile still being filled), failed or
	// unavailable (with its problems).
	State string `json:"state"`
	// Covers are the hunks the tile presents.
	Covers     []string        `json:"covers,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	Problems   []string        `json:"problems,omitempty"`
}

// Empty is the view of a version without a composition: no overview parts,
// no tiles, and every section listed with none, so the page shows the fixed
// overview from the review alone.
func Empty(r *Review) View {
	return Build(r, nil, nil)
}

// Build makes the served view of a review r with its composition c
// (normalised: Normalize), nil for none, and extras: the composition's
// library and authored tiles, and any custom tile the person added, as the
// served tile document shows them (checked, custom definitions in their
// served form). A tile of extras the composition does not place is placed
// on the section whose hunks it covers most (Place). The overview's diagram
// and the placements name only tiles the view has.
func Build(r *Review, c *Composition, extras []tiles.Tile) View {
	v := View{Schema: Name, Overview: Overview{Screenshots: []string{}, Intent: []Intent{}}, Tiles: []Tile{}}
	placed := &Composition{Sections: []Placement{}}
	if c != nil {
		v.Overview = c.Overview
		v.Overview.Screenshots = nonNil(c.Overview.Screenshots)
		v.Overview.Intent = nonNil(c.Overview.Intent)
		for _, t := range c.Diagrams {
			v.Tiles = append(v.Tiles, Tile{ID: t.ID, Type: t.Type, Title: t.Title, Data: t.Data, State: tiles.StateReady, Covers: sourceHunks(t.Data)})
		}
		placed.Overview.Diagram = c.Overview.Diagram
		for _, p := range c.Sections {
			placed.Sections = append(placed.Sections, Placement{ID: p.ID, Tiles: slices.Clone(p.Tiles)})
		}
	}
	var extraIDs []string
	for _, t := range extras {
		if slices.ContainsFunc(v.Tiles, func(have Tile) bool { return have.ID == t.ID }) {
			continue
		}
		v.Tiles = append(v.Tiles, Tile{ID: t.ID, Type: t.Type, Title: t.Title, Data: t.Data, State: t.State, Covers: t.Covers, Definition: t.Definition, Problems: t.Problems})
		extraIDs = append(extraIDs, t.ID)
	}
	has := map[string]bool{}
	for _, t := range v.Tiles {
		has[t.ID] = true
	}
	if d := v.Overview.Diagram; d != nil && !has[*d] {
		v.Overview.Diagram = nil
		placed.Overview.Diagram = nil
	}
	for i := range placed.Sections {
		placed.Sections[i].Tiles = slices.DeleteFunc(placed.Sections[i].Tiles, func(id string) bool { return !has[id] })
	}
	Place(r, placed, extraIDs, Covers(extras))
	by := map[string][]string{}
	for _, p := range placed.Sections {
		by[p.ID] = append(by[p.ID], p.Tiles...)
	}
	for _, id := range sectionOrder(r) {
		v.Sections = append(v.Sections, Placement{ID: id, Tiles: nonNil(by[id])})
	}
	return v
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// sourceHunks are the hunks a native tile's source cites, once each.
func sourceHunks(data json.RawMessage) []string {
	var d struct {
		Source []Anchor `json:"source"`
	}
	_ = json.Unmarshal(data, &d)
	var out []string
	for _, a := range d.Source {
		if a.Type == "hunk" && !slices.Contains(out, a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}
