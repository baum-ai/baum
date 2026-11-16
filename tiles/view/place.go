package view

import (
	"slices"

	"github.com/baum-ai/baum/tiles"
)

// ExtraTiles are a composition's library and authored tiles as the tile
// document composed with them shows them (tiles.Compose of its Grid): each
// checked, with its state, problems and the hunks it covers. A tile the
// document cannot take is missing; doc nil or a composition that cannot be
// composed gives none.
func ExtraTiles(doc *tiles.Document, c *Composition) []tiles.Tile {
	if doc == nil || len(c.Library) == 0 && len(c.Authored) == 0 {
		return nil
	}
	grid := c.Grid()
	grid.View = false
	composed, err := tiles.Compose(doc, grid)
	if err != nil {
		return nil
	}
	wanted := ExtraIDs(c)
	var out []tiles.Tile
	for _, id := range wanted {
		for _, t := range composed.Tiles {
			if t.ID == id {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// Covers maps tiles to the hunks they cover.
func Covers(ts []tiles.Tile) map[string][]string {
	out := map[string][]string{}
	for _, t := range ts {
		out[t.ID] = t.Covers
	}
	return out
}

// Place places every tile of ids the composition does not place yet, in
// order, on the section whose hunks it covers most (covers), ties going to
// the first such section in review order, and on context when it covers no
// section's hunk. A stale section, whose code is gone, is never chosen. It
// places past the view call's MaxPlaced: the call chose what leads a
// section, and the host only makes sure every tile has a place. It returns
// how many tiles it placed.
func Place(r *Review, c *Composition, ids []string, covers map[string][]string) int {
	placed := c.placed()
	n := 0
	for _, id := range ids {
		if placed[id] {
			continue
		}
		placed[id] = true
		target := bestSection(r, covers[id])
		i := slices.IndexFunc(c.Sections, func(p Placement) bool { return p.ID == target })
		if i < 0 {
			c.Sections = append(c.Sections, Placement{ID: target})
			i = len(c.Sections) - 1
		}
		c.Sections[i].Tiles = append(c.Sections[i].Tiles, id)
		n++
	}
	order := sectionOrder(r)
	slices.SortStableFunc(c.Sections, func(a, b Placement) int {
		return slices.Index(order, a.ID) - slices.Index(order, b.ID)
	})
	return n
}

// bestSection is the current section whose hunks the covered hunks meet
// most, the first on a tie, else context.
func bestSection(r *Review, covered []string) string {
	best, most := ContextSection, 0
	for _, s := range r.Sections {
		if s.Stale {
			continue
		}
		n := 0
		for _, h := range covered {
			if slices.Contains(s.Hunks, h) {
				n++
			}
		}
		if n > most {
			best, most = s.ID, n
		}
	}
	return best
}
