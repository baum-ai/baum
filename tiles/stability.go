package tiles

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// StabilizeIDs keeps the IDs of a pull request's tiles from one version to
// the next. It returns next with:
//
//   - a promotion given the ID of the previous version's promotion of the
//     same change and kind, when exactly one promotion of each version has
//     that change and kind and no tile of next has the ID;
//   - a promoted or authored tile whose ID the previous version gave another
//     subject (a library tile, an authored tile, or a promotion of another
//     change or one that became authored) renamed with the first free
//     numeric suffix (_2, _3, …),
//
// and the layout's entries and the notes renamed with them. Library tiles
// keep their IDs, which are their types' names. A host runs it on a
// composition before NormalizeComposition, with the previous version's
// stored composition; with no previous composition next is returned as it
// is.
func StabilizeIDs(previous, next *Composition) Composition {
	if next == nil {
		return Composition{Schema: CompositionName}
	}
	out := cloneComposition(next)
	if previous == nil {
		return out
	}
	type subject struct{ kind, change string }
	before := map[string]subject{}
	byKey := map[subject][]string{}
	for _, p := range previous.Promotions {
		s := subject{prefix(p.ID), p.Change}
		before[p.ID] = s
		byKey[s] = append(byKey[s], p.ID)
	}
	for _, a := range previous.Authored {
		before[a.ID] = subject{"authored", ""}
	}
	for _, lib := range previous.Library {
		before[definitionName(lib.Definition)] = subject{"library", ""}
	}
	used := map[string]bool{}
	for _, lib := range out.Library {
		used[definitionName(lib.Definition)] = true
	}
	for _, p := range out.Promotions {
		used[p.ID] = true
	}
	for _, a := range out.Authored {
		used[a.ID] = true
	}
	count := map[subject]int{}
	for _, p := range out.Promotions {
		count[subject{prefix(p.ID), p.Change}]++
	}
	renames := map[string]string{}
	rename := func(id, next string) {
		used[next] = true
		if _, ok := renames[id]; !ok {
			renames[id] = next
		}
	}
	free := func(id string) string {
		for n := 2; ; n++ {
			candidate := id + "_" + strconv.Itoa(n)
			if _, ok := before[candidate]; !ok && !used[candidate] {
				return candidate
			}
		}
	}
	// Authored tiles first, so a promotion can take back an ID an authored
	// tile of another subject held.
	for i, a := range out.Authored {
		if was, ok := before[a.ID]; ok && was.kind != "authored" {
			next := free(a.ID)
			delete(used, a.ID)
			rename(a.ID, next)
			out.Authored[i].ID = next
		}
	}
	for i, p := range out.Promotions {
		s := subject{prefix(p.ID), p.Change}
		if ids := byKey[s]; len(ids) == 1 && count[s] == 1 && ids[0] != p.ID && !used[ids[0]] {
			delete(used, p.ID)
			rename(p.ID, ids[0])
			out.Promotions[i].ID = ids[0]
			continue
		}
		if was, ok := before[p.ID]; ok && was != s {
			next := free(p.ID)
			delete(used, p.ID)
			rename(p.ID, next)
			out.Promotions[i].ID = next
		}
	}
	if len(renames) == 0 {
		return out
	}
	for i, entry := range out.Layout.Entries {
		if next, ok := renames[entry.Tile]; ok {
			out.Layout.Entries[i].Tile = next
		}
	}
	if out.Notes != nil {
		if out.Notes.Why != nil {
			why := map[string]string{}
			for _, id := range slices.Sorted(maps.Keys(out.Notes.Why)) {
				key := id
				if next, ok := renames[id]; ok {
					key = next
				}
				why[key] = out.Notes.Why[id]
			}
			out.Notes.Why = why
		}
		for i, move := range out.Notes.Moves {
			if next, ok := renames[move.Tile]; ok {
				out.Notes.Moves[i].Tile = next
			}
		}
	}
	return out
}

// prefix returns the type an ID of a promoted or authored tile starts with.
func prefix(id string) string {
	kind, _, _ := strings.Cut(id, "_")
	return kind
}

// definitionName returns the name a library tile's definition gives, or "".
func definitionName(definition json.RawMessage) string {
	var def struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(definition, &def)
	return def.Name
}

// cloneComposition copies a composition's lists and notes.
func cloneComposition(c *Composition) Composition {
	out := *c
	out.Layout.Entries = slices.Clone(c.Layout.Entries)
	out.Library = slices.Clone(c.Library)
	out.Promotions = slices.Clone(c.Promotions)
	out.Authored = slices.Clone(c.Authored)
	if c.Notes != nil {
		notes := *c.Notes
		notes.Why = maps.Clone(c.Notes.Why)
		notes.Moves = slices.Clone(c.Notes.Moves)
		out.Notes = &notes
	}
	return out
}
