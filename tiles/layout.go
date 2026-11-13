package tiles

import "slices"

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

// Arrange stably moves pinned entries to the front and sets Landing. A
// converted document pins nothing, so its order is unchanged.
func Arrange(layout Layout) Layout {
	entries := slices.Clone(layout.Entries)
	slices.SortStableFunc(entries, func(a, b Entry) int {
		switch {
		case a.Pin && !b.Pin:
			return -1
		case b.Pin && !a.Pin:
			return 1
		}
		return 0
	})
	layout.Entries = entries
	layout.Landing = Landing(layout)
	return layout
}

// Landing returns the number of leading entries on the landing page. Entries
// are placed in order, each row filled greedily: an entry that does not fit
// the rest of its row starts the next one, and the first entry that does not
// fit in LandingRows rows ends the landing page. A member of a collapsed group
// past its CollapseAfter takes no space. The result is the index after the
// last entry placed.
func Landing(layout Layout) int {
	collapse := map[string]int{}
	for _, group := range layout.Groups {
		if group.CollapseAfter != nil {
			collapse[group.ID] = *group.CollapseAfter
		}
	}
	members := map[string]int{}
	landing, row, used := 0, 0, 0
	for i, entry := range layout.Entries {
		if entry.Group != "" {
			members[entry.Group]++
			if limit, ok := collapse[entry.Group]; ok && members[entry.Group] > limit {
				continue
			}
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

// Visible reports, for each entry, whether the landing page shows it: it is
// before Landing and not a collapsed group member past its CollapseAfter.
func Visible(layout Layout) []bool {
	collapse := map[string]int{}
	for _, group := range layout.Groups {
		if group.CollapseAfter != nil {
			collapse[group.ID] = *group.CollapseAfter
		}
	}
	members := map[string]int{}
	visible := make([]bool, len(layout.Entries))
	for i, entry := range layout.Entries {
		if i >= layout.Landing {
			break
		}
		visible[i] = true
		if entry.Group != "" {
			members[entry.Group]++
			if limit, ok := collapse[entry.Group]; ok && members[entry.Group] > limit {
				visible[i] = false
			}
		}
	}
	return visible
}
