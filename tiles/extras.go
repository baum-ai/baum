package tiles

import (
	"encoding/json"
	"slices"
)

// PlaceExtras gives the library and authored tiles of a composition that its
// layout leaves out a place, before it is normalised (which would append
// them last): a library tile whose data has a destructive or breaking item
// (Leads) right after the overview and risks tiles that lead the layout;
// any other library tile just before the changes group; an authored tile
// just after the changes group. A library tile takes its LibrarySize, an
// authored tile its AuthoredSize. With no changes group, both go after the leading tiles. A
// composer that places a tile itself keeps its place.
func PlaceExtras(c *Composition, doc *Document) {
	placed := map[string]bool{}
	for _, entry := range c.Layout.Entries {
		placed[entry.Tile] = true
	}
	var lead, before, after []Entry
	for _, lib := range c.Library {
		var def struct {
			Name  string `json:"name"`
			Sizes []Size `json:"sizes"`
		}
		if json.Unmarshal(lib.Definition, &def) != nil || def.Name == "" {
			continue
		}
		id := LibraryTileID(def.Name)
		if placed[id] {
			continue
		}
		placed[id] = true
		entry := Entry{Tile: id, Size: LibrarySize(lib)}
		if !lib.Pending && Leads(lib.Data) {
			lead = append(lead, entry)
		} else {
			before = append(before, entry)
		}
	}
	for _, a := range c.Authored {
		if placed[a.ID] {
			continue
		}
		placed[a.ID] = true
		after = append(after, Entry{Tile: a.ID, Size: AuthoredSize(a)})
	}
	if len(lead)+len(before)+len(after) == 0 {
		return
	}
	entries := c.Layout.Entries
	// The leading tiles: the overview and risks entries before the first
	// change of the group (or of the layout).
	firstGroup, lastGroup := -1, -1
	for i, entry := range entries {
		if entry.Group == GroupChanges {
			if firstGroup < 0 {
				firstGroup = i
			}
			lastGroup = i
		}
	}
	leadEnd := 0
	for i, entry := range entries {
		if firstGroup >= 0 && i >= firstGroup {
			break
		}
		if entry.Tile == TypeOverview || entry.Tile == TypeRisks {
			leadEnd = i + 1
		}
	}
	out := slices.Clone(entries[:leadEnd])
	out = append(out, lead...)
	rest := entries[leadEnd:]
	if firstGroup < 0 {
		out = append(out, before...)
		out = append(out, after...)
		out = append(out, rest...)
		c.Layout.Entries = out
		return
	}
	first, last := firstGroup-leadEnd, lastGroup-leadEnd
	out = append(out, rest[:first]...)
	out = append(out, before...)
	out = append(out, rest[first:last+1]...)
	out = append(out, after...)
	out = append(out, rest[last+1:]...)
	c.Layout.Entries = out
}

// Leads reports whether a library tile's data has an item a reviewer must
// see first, as the built-in types draw it: a member "destructive" or
// "breaking" that is true, or an item whose "change" is "dropped" (a
// dropped table), at any depth.
func Leads(data json.RawMessage) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case []any:
			return slices.ContainsFunc(v, walk)
		case map[string]any:
			for key, item := range v {
				if (key == "destructive" || key == "breaking") && item == true || key == "change" && item == "dropped" {
					return true
				}
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

// AuthoredSize is an authored tile's default size: full for a table of four
// columns or more, whose glance needs the width, else half where its type
// allows it.
func AuthoredSize(a AuthoredTile) Size {
	kind := prefix(a.ID)
	allowed := AllowedSizes(Tile{ID: a.ID, Type: kind})
	if kind == TypeTable {
		var data struct {
			Columns []string `json:"columns"`
		}
		if json.Unmarshal(a.Data, &data) == nil && len(data.Columns) >= 4 && slices.Contains(allowed, SizeFull) {
			return SizeFull
		}
	}
	if slices.Contains(allowed, SizeHalf) {
		return SizeHalf
	}
	return allowed[0]
}

// LibrarySize is a library tile's default size: half when its data lists
// few items (at most three in its longest top-level list, what a half
// glance shows in full), else full, where its definition allows the size.
func LibrarySize(lib LibraryTile) Size {
	var def struct {
		Sizes []Size `json:"sizes"`
	}
	_ = json.Unmarshal(lib.Definition, &def)
	allowed := def.Sizes
	if len(allowed) == 0 {
		allowed = []Size{SizeHalf}
	}
	longest := 0
	var top map[string]json.RawMessage
	if json.Unmarshal(lib.Data, &top) == nil {
		for name, raw := range top {
			var items []json.RawMessage
			if name != "related" && json.Unmarshal(raw, &items) == nil {
				longest = max(longest, len(items))
			}
		}
	}
	want := SizeHalf
	if longest > 3 {
		want = SizeFull
	}
	if slices.Contains(allowed, want) {
		return want
	}
	return allowed[0]
}
