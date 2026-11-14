package compose

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// Authored is an authored tile (an exhibit) the analysis wrote, with its own
// sentence on what it adds, which the composer reads.
type Authored struct {
	Tile tiles.AuthoredTile
	Adds string
}

// AuthoredFact is an authored tile as a composer sees it.
type AuthoredFact struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Sizes    []tiles.Size `json:"sizes"`
	Headline string       `json:"headline,omitempty"`
	Adds     string       `json:"adds,omitempty"`
	Rows     int          `json:"rows,omitempty"`
	Columns  []string     `json:"columns,omitempty"`
	Lines    int          `json:"lines,omitempty"`
	Hunks    int          `json:"hunks"`
	// Size is the size that fits it (tiles.AuthoredSize) and Supports the
	// change whose hunks it cites most (tiles.SupportedChange).
	Size     tiles.Size `json:"size"`
	Supports string     `json:"supports,omitempty"`
}

// FillOf describes a library type's fill for the inventory: its entry, the
// outline's hunks and reason, its state, and for a ready fill the library
// tile with its data and what the data lists.
func FillOf(e library.Entry, hunks []string, why, state string, data json.RawMessage) Fill {
	def := e.Definition
	f := Fill{Type: def.Name, Title: def.Title, UseWhen: def.UseWhen, Glance: e.Glance, Sizes: slices.Clone(def.Sizes),
		Hunks: slices.Clone(hunks), Why: why, State: state}
	if state == "ready" && len(data) > 0 {
		f.Tile = &tiles.LibraryTile{Definition: e.Encoded, Data: data}
		f.Items = arrayCounts(data)
		f.Leads = tiles.Leads(data)
	}
	return f
}

// arrayCounts counts the items of a data object's top-level arrays that
// have any.
func arrayCounts(data json.RawMessage) map[string]int {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil
	}
	counts := map[string]int{}
	for name, raw := range top {
		if name == library.Related {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			counts[name] = len(items)
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

// extrasContent is the composition of the analysis's tiles, which the
// template places: the ready fills' library tiles and the authored tiles.
func extrasContent(fills []Fill, authored []Authored) *tiles.Composition {
	c := &tiles.Composition{Schema: tiles.CompositionName}
	for _, f := range fills {
		if f.Tile != nil {
			c.Library = append(c.Library, *f.Tile)
		}
	}
	for _, a := range authored {
		c.Authored = append(c.Authored, a.Tile)
	}
	return c
}

func authoredFacts(doc *tiles.Document, authored []Authored) []AuthoredFact {
	var out []AuthoredFact
	for _, a := range authored {
		kind, _, _ := strings.Cut(a.Tile.ID, "_")
		fact := AuthoredFact{ID: a.Tile.ID, Type: kind, Title: clip(a.Tile.Title, maxTitle), Adds: clip(a.Adds, maxAttention),
			Sizes: tiles.AllowedSizes(tiles.Tile{ID: a.Tile.ID, Type: kind})}
		var data struct {
			Columns []string          `json:"columns"`
			Rows    [][]string        `json:"rows"`
			Content string            `json:"content"`
			Caption string            `json:"caption"`
			Text    string            `json:"text"`
			Source  []json.RawMessage `json:"source"`
			Anchors []json.RawMessage `json:"anchors"`
		}
		_ = json.Unmarshal(a.Tile.Data, &data)
		switch kind {
		case tiles.TypeTable:
			fact.Rows = len(data.Rows)
			for _, column := range data.Columns[:min(len(data.Columns), maxColumns)] {
				fact.Columns = append(fact.Columns, clip(column, maxColumn))
			}
		case tiles.TypeCode:
			fact.Lines = strings.Count(data.Content, "\n") + 1
			fact.Headline = clip(data.Caption, maxHeadline)
		case tiles.TypeProse:
			fact.Headline = clip(data.Text, maxHeadline)
		}
		fact.Hunks = len(data.Source) + len(data.Anchors)
		fact.Size, fact.Supports = tiles.AuthoredSize(a.Tile), tiles.SupportedChange(doc, a.Tile.Data)
		out = append(out, fact)
	}
	return out
}
