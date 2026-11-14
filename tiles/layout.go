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

// The landing page follows one rule, which the tiles page lays out exactly
// (app/src/tiles/layout.ts mirrors Hidden and Visible; the page reads
// Landing):
//
//   - A member of a group past its CollapseAfter is hidden wherever it
//     stands (Hidden). It takes no space, and its group's own "Show N more"
//     reveals it; that control counts all of the group's hidden members. A
//     group that shows some of its members never folds a single one: "Show
//     1 more" would take a row to hide one card, so that member is shown.
//   - The entries that are not hidden fill rows of GridUnits units in order
//     (Rows). An entry that does not fit what is left of its row starts the
//     next. A group is a block of its own: its heading spans the grid, so
//     its first member starts a row, and the entry after its last member
//     starts another, never sharing a row with the group's members.
//   - Landing is the index after the last entry of the first LandingRows
//     rows, unless a single entry that is not hidden would be left after
//     it: "Show 1 more tile" is never the landing page's end, so that entry
//     is on the landing page too.
//   - The landing page shows the entries before Landing that are not hidden
//     (Visible). "Show N more tiles" reveals the entries from Landing on
//     that are not hidden, More of them; hidden members there stay behind
//     their group's control.
//
// On the two-column grid of a narrow window the page lets the last card of
// a block span both columns when it would be alone in its row; that changes
// no row of the canonical grid, what the landing page holds, or any size.

// Hidden reports, for each entry, whether it is a member of a collapsed
// group past the group's CollapseAfter.
func Hidden(layout Layout) []bool {
	collapse := map[string]int{}
	for _, group := range layout.Groups {
		if group.CollapseAfter != nil {
			collapse[group.ID] = *group.CollapseAfter
		}
	}
	total := map[string]int{}
	for _, entry := range layout.Entries {
		if entry.Group != "" {
			total[entry.Group]++
		}
	}
	members := map[string]int{}
	hidden := make([]bool, len(layout.Entries))
	for i, entry := range layout.Entries {
		if entry.Group == "" {
			continue
		}
		members[entry.Group]++
		limit, ok := collapse[entry.Group]
		if ok && (limit == 0 || total[entry.Group]-limit >= 2) && members[entry.Group] > limit {
			hidden[i] = true
		}
	}
	return hidden
}

// Row is one row of the canonical grid as the page lays it out (Rows).
type Row struct {
	// Entries are the indexes in Layout.Entries of the row's tiles, in
	// order.
	Entries []int
	// Group is the group whose block holds the row, empty outside groups.
	Group string
	// Used is the grid units the row's tiles take.
	Used int
}

// Empty is the grid units the row leaves empty.
func (r Row) Empty() int { return GridUnits - r.Used }

// Rows lays out the entries that are not hidden in rows of GridUnits units,
// in order, as the landing page and "Show more" do: an entry that does not
// fit what is left of its row starts the next, and a group's first member
// and the entry after its last member each start a row (a group is a block
// under its own heading).
func Rows(layout Layout) []Row {
	hidden := Hidden(layout)
	var rows []Row
	split := true
	for i, entry := range layout.Entries {
		if i > 0 && entry.Group != layout.Entries[i-1].Group {
			split = true
		}
		if hidden[i] {
			continue
		}
		units := entry.Size.Units()
		if split || rows[len(rows)-1].Used+units > GridUnits {
			rows = append(rows, Row{Group: entry.Group})
		}
		split = false
		row := &rows[len(rows)-1]
		row.Entries = append(row.Entries, i)
		row.Used += units
	}
	return rows
}

// Landing returns the index after the last entry the landing page places.
func Landing(layout Layout) int {
	landing := 0
	for k, row := range Rows(layout) {
		if k >= LandingRows {
			break
		}
		landing = row.Entries[len(row.Entries)-1] + 1
	}
	hidden := Hidden(layout)
	var rest []int
	for i := landing; i < len(layout.Entries); i++ {
		if !hidden[i] {
			rest = append(rest, i)
		}
	}
	if len(rest) == 1 {
		landing = rest[0] + 1
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

// RowFills returns, by tile ID, the sizes that fill the rows (Rows) that
// end with empty units while another row follows: the row's last tile grows
// to the larger allowed size (SizesIn) that fills the row exactly, else every
// tile of the row grows to the one size that fills it in equal parts. A
// pinned tile is never grown, nothing shrinks, and no tile changes rows. An
// arranged document (Arrange) has its Landing set; nil means every row is
// full or cannot be filled that way.
func RowFills(doc *Document) map[string]Size {
	layout := doc.Layout
	byID := map[string]*Tile{}
	for i := range doc.Tiles {
		byID[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	var fills map[string]Size
	set := func(id string, size Size) {
		if fills == nil {
			fills = map[string]Size{}
		}
		fills[id] = size
	}
	rows := Rows(layout)
	for k, row := range rows {
		if row.Used >= GridUnits || k == len(rows)-1 {
			continue
		}
		growable := true
		for _, i := range row.Entries {
			entry := layout.Entries[i]
			growable = growable && !entry.Pin && byID[entry.Tile] != nil
		}
		if !growable {
			continue
		}
		last := layout.Entries[row.Entries[len(row.Entries)-1]]
		want := Size("")
		for _, size := range SizesIn(doc, *byID[last.Tile]) {
			if size.Units() > last.Size.Units() && row.Used-last.Size.Units()+size.Units() == GridUnits {
				want = size
			}
		}
		if want != "" {
			set(last.Tile, want)
			continue
		}
		if GridUnits%len(row.Entries) != 0 {
			continue
		}
		equal := Size("")
		for _, size := range []Size{SizeFull, SizeHalf, SizeThird} {
			if size.Units()*len(row.Entries) == GridUnits {
				equal = size
			}
		}
		fits := equal != ""
		for _, i := range row.Entries {
			entry := layout.Entries[i]
			fits = fits && entry.Size.Units() <= equal.Units() && slices.Contains(SizesIn(doc, *byID[entry.Tile]), equal)
		}
		if !fits {
			continue
		}
		for _, i := range row.Entries {
			if entry := layout.Entries[i]; entry.Size != equal {
				set(entry.Tile, equal)
			}
		}
	}
	return fills
}
