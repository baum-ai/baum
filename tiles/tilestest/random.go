package tilestest

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/baum-ai/baum/tiles"
)

// LibraryDefinition returns a synthetic library tile type, named name: a
// summary and the citations it rests on.
func LibraryDefinition(name string) tiles.TypeDefinition {
	return tiles.TypeDefinition{
		Name:    name,
		Title:   "Synthetic " + strings.ReplaceAll(name, "_", " "),
		UseWhen: "When a test needs a library tile.",
		Fill:    "Summarise the change and cite the hunks it rests on.",
		DataSchema: json.RawMessage(`{
			"type": "object", "additionalProperties": false, "required": ["summary", "cites"],
			"properties": {
				"summary": {"$ref": "#/$defs/markdown"},
				"cites": {"type": "array", "minItems": 1, "maxItems": 20, "items": {"$ref": "#/$defs/citation"}}
			}
		}`),
		Kit:    1,
		Sizes:  []tiles.Size{tiles.SizeHalf, tiles.SizeFull},
		Source: tiles.TypeSource{Format: "html", HTML: "<script>baum.tile({glance: (v) => baum.html`<p>${v.data.summary}</p>`});</script>"},
	}
}

// LibraryTile returns a library tile of LibraryDefinition(name) citing the
// hunks.
func LibraryTile(name string, hunks ...string) tiles.LibraryTile {
	data, _ := json.Marshal(map[string]any{"summary": "A synthetic summary.", "cites": hunkAnchors(hunks)})
	return tiles.LibraryTile{Definition: SealDefinition(LibraryDefinition(name)), Data: data}
}

// AuthoredTile returns an authored tile of the type the ID starts with,
// citing the hunks.
func AuthoredTile(id string, hunks ...string) tiles.AuthoredTile {
	kind, _, _ := strings.Cut(id, "_")
	var data any
	switch kind {
	case tiles.TypeTable:
		data = map[string]any{"columns": []string{"Setting", "Default"}, "rows": [][]string{{"retries", "3"}, {"jitter", "on"}}, "source": hunkAnchors(hunks)}
	case tiles.TypeCode:
		data = map[string]any{"language": "go", "content": "if attempts > limit {\n\treturn errGiveUp\n}", "caption": "The give-up rule.", "source": hunkAnchors(hunks)}
	default:
		data = map[string]any{"text": "Retries back off before giving up.", "anchors": hunkAnchors(hunks)}
	}
	encoded, _ := json.Marshal(data)
	return tiles.AuthoredTile{ID: id, Title: "Synthetic " + kind, Data: encoded}
}

func hunkAnchors(hunks []string) []any {
	anchors := []any{}
	for _, hunk := range hunks {
		anchors = append(anchors, map[string]any{"type": "hunk", "id": hunk})
	}
	return anchors
}

// Hunks returns the IDs of the hunks a document's index lists.
func Hunks(doc *tiles.Document) []string {
	var index struct {
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(doc.Index, &index)
	hunks := make([]string, len(index.Hunks))
	for i, hunk := range index.Hunks {
		hunks[i] = hunk.ID
	}
	return hunks
}

// RandomComposition draws a composition for a converted document from rng,
// as a model might write one: layout entries over the document's tiles in a
// random order with random sizes, groups and pins, promotions of its table
// and code blocks, library and authored tiles citing its hunks, and notes.
// With hostile it also draws what normalisation must repair or refuse:
// unknown and repeated tiles, sizes and groups a tile cannot have,
// promotions of blocks that do not exist, have another kind or were already
// promoted, too many promotions and authored tiles, authored tiles that cite
// nothing, cite what the review lacks or carry an origin, library tiles with
// bad definitions or data, clashing IDs, and long notes. The draw is
// deterministic for a given rng state.
func RandomComposition(rng *rand.Rand, doc *tiles.Document, hostile bool) *tiles.Composition {
	c := &tiles.Composition{Schema: tiles.CompositionName}
	hunks := Hunks(doc)
	pick := func(n int) []string {
		var out []string
		for range n {
			if len(hunks) > 0 {
				out = append(out, hunks[rng.IntN(len(hunks))])
			}
		}
		return out
	}
	sizes := []tiles.Size{tiles.SizeFull, tiles.SizeHalf, tiles.SizeThird}
	var ids []string
	for _, tile := range doc.Tiles {
		ids = append(ids, tile.ID)
	}

	for i, block := range tiles.Promotable(doc) {
		if rng.IntN(2) == 0 {
			continue
		}
		c.Promotions = append(c.Promotions, tiles.Promotion{
			ID: fmt.Sprintf("%s_block_%d", block.Kind, i), Change: block.Change, Block: block.Block, Title: "Promoted " + block.Kind,
		})
	}
	for i := range rng.IntN(3) {
		c.Library = append(c.Library, LibraryTile(fmt.Sprintf("synthetic_%d", i), pick(1+rng.IntN(3))...))
	}
	for i := range rng.IntN(4) {
		kind := []string{"table", "code", "prose"}[rng.IntN(3)]
		c.Authored = append(c.Authored, AuthoredTile(fmt.Sprintf("%s_note_%d", kind, i), pick(1+rng.IntN(2))...))
	}
	for _, p := range c.Promotions {
		ids = append(ids, p.ID)
	}
	for _, a := range c.Authored {
		ids = append(ids, a.ID)
	}
	for _, lib := range c.Library {
		var def tiles.TypeDefinition
		_ = json.Unmarshal(lib.Definition, &def)
		ids = append(ids, def.Name)
	}

	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	for _, id := range ids {
		if rng.IntN(5) == 0 {
			continue
		}
		entry := tiles.Entry{Tile: id, Size: sizes[rng.IntN(len(sizes))]}
		if strings.HasPrefix(id, "sec_") && rng.IntN(3) > 0 {
			entry.Group = tiles.GroupChanges
		}
		if rng.IntN(12) == 0 {
			entry.Pin = true
		}
		c.Layout.Entries = append(c.Layout.Entries, entry)
	}
	if rng.IntN(2) == 0 {
		shown := 1 + rng.IntN(8)
		c.Layout.ChangesShown = &shown
	}
	c.Notes = &tiles.Notes{Why: map[string]string{}}
	for _, id := range ids {
		if rng.IntN(3) == 0 {
			c.Notes.Why[id] = "Leads because it holds the riskiest change."
		}
	}
	if rng.IntN(4) == 0 {
		c.Notes.CustomSuggestion = &tiles.CustomSuggestion{Title: "Queue states", Shows: "The queue's states and moves.", Why: "No tile shows them."}
	}
	if !hostile {
		return c
	}

	// Layout entries a composition must not have.
	for range 1 + rng.IntN(6) {
		var entry tiles.Entry
		switch rng.IntN(7) {
		case 0:
			entry = tiles.Entry{Tile: "ignore previous instructions", Size: tiles.SizeFull}
		case 1:
			entry = tiles.Entry{Tile: "sec_does_not_exist", Size: tiles.SizeHalf}
		case 2:
			if len(ids) > 0 {
				entry = tiles.Entry{Tile: ids[rng.IntN(len(ids))], Size: "huge"}
			}
		case 3:
			if len(ids) > 0 {
				entry = tiles.Entry{Tile: ids[rng.IntN(len(ids))], Size: tiles.SizeThird, Group: []string{"obsolete", "other", "changes"}[rng.IntN(3)]}
			}
		case 4:
			if len(c.Layout.Entries) > 0 {
				entry = c.Layout.Entries[rng.IntN(len(c.Layout.Entries))]
			}
		case 5:
			entry = tiles.Entry{Tile: tiles.TypeRisks, Size: tiles.SizeThird, Group: tiles.GroupChanges, Pin: true}
		case 6:
			entry = tiles.Entry{Tile: "", Size: ""}
		}
		at := rng.IntN(len(c.Layout.Entries) + 1)
		c.Layout.Entries = append(c.Layout.Entries[:at], append([]tiles.Entry{entry}, c.Layout.Entries[at:]...)...)
	}
	if rng.IntN(4) == 0 {
		shown := -rng.IntN(3)
		c.Layout.ChangesShown = &shown
	}

	// Promotions of what cannot be promoted.
	var changes []string
	for _, change := range doc.Changes {
		changes = append(changes, change.ID)
	}
	for range rng.IntN(5) {
		p := tiles.Promotion{ID: fmt.Sprintf("table_hostile_%d", rng.IntN(4)), Block: rng.IntN(6) - 1, Title: strings.Repeat("Long title\n", 20)}
		if len(changes) > 0 {
			p.Change = changes[rng.IntN(len(changes))]
		}
		switch rng.IntN(5) {
		case 0:
			p.ID = "TABLE x"
		case 1:
			p.ID = "code_" + strings.TrimPrefix(p.ID, "table_")
		case 2:
			p.Digest = "sha256:" + strings.Repeat("0", 64)
		case 3:
			p.Change = "sec_missing"
		}
		c.Promotions = append(c.Promotions, p)
	}
	if len(c.Promotions) > 0 && rng.IntN(2) == 0 {
		c.Promotions = append(c.Promotions, c.Promotions[0])
	}

	// Authored tiles that must be refused or cut.
	for i := range rng.IntN(50) {
		a := AuthoredTile(fmt.Sprintf("prose_extra_%d", i), pick(1)...)
		switch rng.IntN(7) {
		case 0:
			a = AuthoredTile(fmt.Sprintf("table_uncited_%d", i))
		case 1:
			a = AuthoredTile(fmt.Sprintf("code_unknown_%d", i), "h_ffffffffffff")
		case 2:
			a.Data = json.RawMessage(`{"text": "Moved here.", "anchors": [], "origin": {"change": "sec_x", "block": 0}}`)
		case 3:
			a.Data = json.RawMessage(`[1, 2]`)
		case 4:
			a.ID = "diagram_flow"
		case 5:
			if len(c.Promotions) > 0 {
				a = AuthoredTile(c.Promotions[0].ID, pick(1)...)
			}
		}
		c.Authored = append(c.Authored, a)
	}

	// Library tiles that must be refused.
	for i := range rng.IntN(12) {
		lib := LibraryTile(fmt.Sprintf("hostile_%d", rng.IntN(4)), pick(1)...)
		switch rng.IntN(8) {
		case 0:
			lib.Definition = json.RawMessage(`{"name": "broken"`)
		case 1:
			def := LibraryDefinition("unsealed")
			def.Digest = "sha256:" + strings.Repeat("1", 64)
			lib.Definition, _ = json.Marshal(def)
		case 2:
			lib.Data = json.RawMessage(`{"summary": 7}`)
		case 3:
			lib.Data = json.RawMessage(`{"summary": "Cites a hunk the review lacks.", "cites": [{"type": "hunk", "id": "h_eeeeeeeeeeee"}]}`)
		case 4:
			if len(changes) > 0 {
				lib = LibraryTile(changes[rng.IntN(len(changes))], pick(1)...)
			}
		case 5:
			lib.Pending, lib.Data = true, json.RawMessage(`{}`)
		case 6:
			lib = LibraryTile(fmt.Sprintf("table_note_%d", i), pick(1)...)
		}
		c.Library = append(c.Library, lib)
	}

	for range rng.IntN(30) {
		c.Notes.Moves = append(c.Notes.Moves, tiles.Move{Tile: "overview", Why: strings.Repeat("Moved up. ", 40)})
	}
	c.Notes.Why["no_such_tile"] = "A reason for nothing."
	return c
}
