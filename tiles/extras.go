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
// just after the changes group. Each takes half the width where its type
// allows it. With no changes group, both go after the leading tiles. A
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
		size := SizeHalf
		if len(def.Sizes) > 0 && !slices.Contains(def.Sizes, SizeHalf) {
			size = def.Sizes[0]
		}
		entry := Entry{Tile: id, Size: size}
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
		size := SizeHalf
		if kind := prefix(a.ID); kind != "" {
			if allowed := AllowedSizes(Tile{ID: a.ID, Type: kind}); !slices.Contains(allowed, SizeHalf) {
				size = allowed[0]
			}
		}
		after = append(after, Entry{Tile: a.ID, Size: size})
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
// see first: a member "destructive" or "breaking" that is true, at any
// depth.
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
				if (key == "destructive" || key == "breaking") && item == true {
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
