package tiles

import (
	"encoding/json"
	"slices"
)

// PlaceExtras gives the library and authored tiles of a composition that its
// layout leaves out a place, before it is normalised (which would append
// them last): a library tile whose data has a destructive or breaking item
// (Leads) right after the overview and risks tiles that lead the layout; any
// other library tile just after the later of the risks and evidence tiles,
// so it never stands above them; an authored tile after the later of those
// and the changes group, after the changes it supports. With neither risks
// nor evidence placed, a library tile goes after the changes group; with no
// changes group either, both go after the leading tiles. A library tile
// takes its LibrarySize, an authored tile its AuthoredSize. A composer that
// places a tile itself keeps its place (the guard rails may still move it).
func PlaceExtras(c *Composition, doc *Document) {
	placed := map[string]bool{}
	for _, entry := range c.Layout.Entries {
		placed[entry.Tile] = true
	}
	var lead, library, authored []Entry
	for _, lib := range c.Library {
		var def struct {
			Name string `json:"name"`
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
			library = append(library, entry)
		}
	}
	for _, a := range c.Authored {
		if placed[a.ID] {
			continue
		}
		placed[a.ID] = true
		authored = append(authored, Entry{Tile: a.ID, Size: AuthoredSize(a)})
	}
	if len(lead)+len(library)+len(authored) == 0 {
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
	index := func(id string) int { return slices.IndexFunc(entries, func(e Entry) bool { return e.Tile == id }) }
	// Each run goes after the entry at its index (-1: before the first).
	core := max(index(TypeRisks), index(TypeEvidence))
	libraryAt := core
	if libraryAt < 0 {
		libraryAt = lastGroup
	}
	authoredAt := max(core, lastGroup)
	for _, at := range []*int{&libraryAt, &authoredAt} {
		if *at < 0 {
			*at = leadEnd - 1
		}
	}
	after := map[int][]Entry{}
	after[leadEnd-1] = append(after[leadEnd-1], lead...)
	after[libraryAt] = append(after[libraryAt], library...)
	after[authoredAt] = append(after[authoredAt], authored...)
	out := slices.Clone(after[-1])
	for i, entry := range entries {
		out = append(out, entry)
		out = append(out, after[i]...)
	}
	c.Layout.Entries = out
}

// Leads reports whether a library tile's data has an item a reviewer must
// see first, as the built-in types draw it: a member "destructive" or
// "breaking" that is true, at any depth, or a dropped table, an item of its
// top-level "tables" whose "change" is "dropped" (as schema_change draws
// one). A "change" elsewhere, as in another tile's own vocabulary, does not
// lead.
func Leads(data json.RawMessage) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	if top, ok := value.(map[string]any); ok {
		if tables, ok := top["tables"].([]any); ok {
			for _, table := range tables {
				if item, ok := table.(map[string]any); ok && item["change"] == "dropped" {
					return true
				}
			}
		}
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

// LibrarySize is a library tile's default size, by what its glance lists
// (LibraryItems): half for three items or fewer, which a half glance shows in
// full, else full; when its definition does not allow that size, the first
// size it allows. One or two items never take the full row.
func LibrarySize(lib LibraryTile) Size {
	var def struct {
		Sizes []Size `json:"sizes"`
	}
	_ = json.Unmarshal(lib.Definition, &def)
	allowed := def.Sizes
	if len(allowed) == 0 {
		allowed = []Size{SizeHalf}
	}
	want := SizeHalf
	if LibraryItems(lib) > 3 {
		want = SizeFull
	}
	if slices.Contains(allowed, want) {
		return want
	}
	if want == SizeHalf && slices.Contains(allowed, SizeThird) {
		return SizeThird
	}
	return allowed[0]
}

// LibraryGlanceLists names, for a built-in library type, the top-level
// arrays of its data whose items its glance lists as rows, in the order it
// looks for them: the glance lists the first that has items. Nil for any
// other type.
func LibraryGlanceLists(name string) []string {
	switch name {
	case "schema_change":
		// Tables, else the migrations.
		return []string{"tables", "migrations"}
	case "api_change":
		return []string{"endpoints"}
	}
	return nil
}

// LibraryItems counts the rows a library tile's glance lists: the items of
// the first of its type's glance lists (LibraryGlanceLists) that has any,
// and for a type without them the longest top-level array of its data but
// the host's related items.
func LibraryItems(lib LibraryTile) int {
	var def struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(lib.Definition, &def)
	var top map[string]json.RawMessage
	if json.Unmarshal(lib.Data, &top) != nil {
		return 0
	}
	count := func(name string) int {
		var items []json.RawMessage
		if json.Unmarshal(top[name], &items) != nil {
			return 0
		}
		return len(items)
	}
	if lists := LibraryGlanceLists(def.Name); lists != nil {
		for _, name := range lists {
			if n := count(name); n > 0 {
				return n
			}
		}
		return 0
	}
	longest := 0
	for name := range top {
		if name != "related" {
			longest = max(longest, count(name))
		}
	}
	return longest
}

// SupportedChange returns the current change a library or authored tile's
// data supports: the one whose hunks the data cites most (a citation of type
// hunk, at any depth), the first in review order on a tie, or "" when it
// cites none of a current change's hunks.
func SupportedChange(doc *Document, data json.RawMessage) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	cited := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			if v["type"] == "hunk" {
				if id, ok := v["id"].(string); ok {
					cited[id] = true
					return
				}
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	best, most := "", 0
	for _, change := range doc.Changes {
		if !currentChange(change) {
			continue
		}
		n := 0
		for _, hunk := range change.Hunks {
			if cited[hunk] {
				n++
			}
		}
		if n > most {
			best, most = change.ID, n
		}
	}
	return best
}
