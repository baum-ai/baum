package tiles

import "slices"

// PinEntries stamps a repository's pins on layout entries: a pinned tile's
// entry is pinned and leaves its group, and a pinned tile the entries omit is
// added, pinned and at half width, at the front, in the order of pins. Pins
// naming no tile of the document are ignored.
func PinEntries(entries []Entry, doc *Document, pins []string) []Entry {
	if len(pins) == 0 {
		return entries
	}
	entries = slices.Clone(entries)
	present := map[string]bool{}
	for _, tile := range doc.Tiles {
		present[tile.ID] = true
	}
	placed := map[string]bool{}
	for i, entry := range entries {
		placed[entry.Tile] = true
		if slices.Contains(pins, entry.Tile) {
			entries[i].Pin, entries[i].Group = true, ""
		}
	}
	var front []Entry
	for _, id := range pins {
		if present[id] && !placed[id] {
			placed[id] = true
			front = append(front, Entry{Tile: id, Size: SizeHalf, Pin: true})
		}
	}
	return append(front, entries...)
}
