package view

import (
	"slices"

	"github.com/baum-ai/baum/tiles"
)

// ExtraTiles are a composition's library and authored tiles as tiles of
// the document converted from its review (tiles.ExtraTiles): each checked,
// with its state, problems and the hunks it covers. doc nil, or a document
// that cannot take them, gives none.
func ExtraTiles(doc *tiles.Document, c *Composition) []tiles.Tile {
	if doc == nil || len(c.Library) == 0 && len(c.Authored) == 0 {
		return nil
	}
	out, err := tiles.ExtraTiles(doc, c.Library, c.Authored)
	if err != nil {
		return nil
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

// SectionFor is where a tile is placed by the hunks it covers: the current
// section of r whose hunks they meet most, the first on a tie, else
// context. A section that is not one of r's current sections, or empty,
// gives way to it; a current section or context is kept.
func SectionFor(r *Review, section string, covered []string) string {
	if section == ContextSection {
		return section
	}
	for _, s := range r.Sections {
		if s.ID == section && !s.Stale {
			return section
		}
	}
	return bestSection(r, covered)
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
