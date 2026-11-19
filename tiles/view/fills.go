package view

import (
	"encoding/json"
	"slices"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// Fill is a library type the tile analysis filled, or planned to, for a
// review: its type, what it shows, the hunks the analysis's outline gave it
// and why, its state, and for a ready fill the library tile with its data.
type Fill struct {
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	UseWhen string   `json:"use_when,omitempty"`
	Glance  string   `json:"glance,omitempty"`
	Hunks   []string `json:"hunks,omitempty"`
	Why     string   `json:"why,omitempty"`
	State   string   `json:"state"`
	// Tile is the ready fill's library tile, nil otherwise.
	Tile *tiles.LibraryTile `json:"-"`
}

// FillOf describes a library type's fill: its entry, the outline's hunks
// and reason, its state, and for a ready fill the library tile with its
// data.
func FillOf(e library.Entry, hunks []string, why, state string, data json.RawMessage) Fill {
	def := e.Definition
	f := Fill{Type: def.Name, Title: def.Title, UseWhen: def.UseWhen, Glance: e.Glance, Hunks: slices.Clone(hunks), Why: why, State: state}
	if state == "ready" && len(data) > 0 {
		f.Tile = &tiles.LibraryTile{Definition: e.Encoded, Data: data}
	}
	return f
}

// Authored is an authored tile (an exhibit) the analysis wrote, with its own
// sentence on what it adds.
type Authored struct {
	Tile tiles.AuthoredTile
	Adds string
}
