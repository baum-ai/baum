package tiles

import (
	"encoding/json"
	"slices"
)

// The landing page is packed on a canonical grid GridUnits wide and
// LandingRows rows tall, whatever the window: a narrow window stacks the same
// order into fewer columns, so what is on the landing page is a property of
// the layout, not of the viewport.
const (
	GridUnits   = 6
	LandingRows = 3
)

// Units returns how many grid units a tile of the size takes.
func (s Size) Units() int {
	switch s {
	case SizeHalf:
		return GridUnits / 2
	case SizeThird:
		return GridUnits / 3
	default:
		return GridUnits
	}
}

// Arrange orders a document's layout for its landing page and sets Landing.
// A custom tile's entry of a size its definition does not declare gets the
// first size it declares. Pinned entries move to the front. Then two tiles
// must be on the landing page, in this order: the risks tile when it ranks
// an active critical or high risk, and the overview tile. Each that is not
// moves to the front with the pinned entries and those before it in that
// order; should the pinned entries alone fill the landing page, it goes
// before them. Every move is stable. A converted document pins nothing and
// places the overview first and the risks tile second, where they are always
// on the landing page, so its order is unchanged.
func Arrange(doc *Document) {
	layout := doc.Layout
	layout.Entries = declaredSizes(doc, layout.Entries)
	layout.Entries = frontFirst(layout.Entries, func(entry Entry) bool { return entry.Pin })
	layout.Landing = Landing(layout)
	var required []string
	if mustShowRisks(doc) {
		required = append(required, TypeRisks)
	}
	if slices.ContainsFunc(doc.Tiles, func(tile Tile) bool { return tile.ID == TypeOverview }) {
		required = append(required, TypeOverview)
	}
	for k, id := range required {
		lead := required[:k+1]
		if onLanding(layout, id) {
			continue
		}
		layout.Entries = frontFirst(layout.Entries, func(entry Entry) bool { return entry.Pin || slices.Contains(lead, entry.Tile) })
		layout.Landing = Landing(layout)
		if !onLanding(layout, id) {
			layout.Entries = frontFirst(layout.Entries, func(entry Entry) bool { return slices.Contains(lead, entry.Tile) })
			layout.Landing = Landing(layout)
		}
	}
	doc.Layout = layout
}

// onLanding reports whether the layout places the tile on its landing page,
// when it places it.
func onLanding(layout Layout, id string) bool {
	i := slices.IndexFunc(layout.Entries, func(entry Entry) bool { return entry.Tile == id })
	return i < 0 || Visible(layout)[i]
}

// declaredSizes returns the entries with each custom tile's size one its
// definition declares.
func declaredSizes(doc *Document, entries []Entry) []Entry {
	var sizes map[string][]Size
	for _, tile := range doc.Tiles {
		if tile.Type == TypeCustom {
			if sizes == nil {
				sizes = map[string][]Size{}
			}
			sizes[tile.ID] = definitionSizes(tile.Definition)
		}
	}
	if sizes == nil {
		return entries
	}
	entries = slices.Clone(entries)
	for i, entry := range entries {
		if declared := sizes[entry.Tile]; len(declared) > 0 && !slices.Contains(declared, entry.Size) {
			entries[i].Size = declared[0]
		}
	}
	return entries
}

func frontFirst(entries []Entry, front func(Entry) bool) []Entry {
	entries = slices.Clone(entries)
	slices.SortStableFunc(entries, func(a, b Entry) int {
		switch {
		case front(a) && !front(b):
			return -1
		case front(b) && !front(a):
			return 1
		}
		return 0
	})
	return entries
}

// mustShowRisks reports whether the risks tile ranks an active critical or
// high risk.
func mustShowRisks(doc *Document) bool {
	for _, tile := range doc.Tiles {
		if tile.Type != TypeRisks {
			continue
		}
		var data RisksData
		if json.Unmarshal(tile.Data, &data) != nil {
			return false
		}
		severe := map[string]bool{}
		for _, risk := range doc.Risks {
			if risk.State == "active" && (risk.Tier == "critical" || risk.Tier == "high") {
				severe[risk.ID] = true
			}
		}
		for _, id := range data.Ranked {
			if severe[id] {
				return true
			}
		}
	}
	return false
}

// The landing page follows one rule:
//
//   - A member of a group past its CollapseAfter is hidden wherever it
//     stands (Hidden). It takes no space, and its group's own "Show N more"
//     reveals it; that control counts all of the group's hidden members.
//   - Landing is the index after the last entry the landing page places:
//     the entries fill rows of GridUnits units in order, an entry that does
//     not fit the rest of its row starts the next, and the first entry that
//     does not fit in LandingRows rows ends the landing page.
//   - The landing page shows the entries before Landing that are not hidden
//     (Visible). "Show N more tiles" reveals the entries from Landing on
//     that are not hidden, More of them; hidden members there stay behind
//     their group's control.

// Hidden reports, for each entry, whether it is a member of a collapsed
// group past the group's CollapseAfter.
func Hidden(layout Layout) []bool {
	collapse := map[string]int{}
	for _, group := range layout.Groups {
		if group.CollapseAfter != nil {
			collapse[group.ID] = *group.CollapseAfter
		}
	}
	members := map[string]int{}
	hidden := make([]bool, len(layout.Entries))
	for i, entry := range layout.Entries {
		if entry.Group == "" {
			continue
		}
		members[entry.Group]++
		if limit, ok := collapse[entry.Group]; ok && members[entry.Group] > limit {
			hidden[i] = true
		}
	}
	return hidden
}

// Landing returns the index after the last entry the landing page places.
func Landing(layout Layout) int {
	hidden := Hidden(layout)
	landing, row, used := 0, 0, 0
	for i, entry := range layout.Entries {
		if hidden[i] {
			continue
		}
		units := entry.Size.Units()
		if used+units > GridUnits {
			row, used = row+1, 0
		}
		if row >= LandingRows {
			break
		}
		used += units
		landing = i + 1
	}
	return landing
}

// Visible reports, for each entry, whether the landing page shows it.
func Visible(layout Layout) []bool {
	hidden := Hidden(layout)
	visible := make([]bool, len(layout.Entries))
	for i := range layout.Entries {
		visible[i] = i < layout.Landing && !hidden[i]
	}
	return visible
}

// More returns how many entries "Show N more tiles" reveals.
func More(layout Layout) int {
	hidden := Hidden(layout)
	n := 0
	for i := max(layout.Landing, 0); i < len(layout.Entries); i++ {
		if !hidden[i] {
			n++
		}
	}
	return n
}
