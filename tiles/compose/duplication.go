package compose

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
)

// Unit is one text of a tile document that states a point: a sentence of a
// tile's prose, a table row, a risk or an evidence claim, with the tile it
// is shown in.
type Unit struct {
	Tile string `json:"tile"`
	Text string `json:"text"`
}

// Pair is two units of different tiles that state the same point by the
// review validator's rule (review.NearDuplicate).
type Pair struct {
	A, B Unit
}

// skipKeys are data members that hold no prose: identifiers, citations,
// code and paths.
var skipKeys = map[string]bool{
	"id": true, "type": true, "kind": true, "path": true, "paths": true, "anchors": true, "source": true,
	"cites": true, "content": true, "patch": true, "hunks": true, "lines": true, "origin": true, "related": true,
	"refs": true, "change": true, "state": true, "tier": true, "status": true, "language": true,
}

var sentenceEnd = regexp.MustCompile(`[.!?]\s+`)

// sentences splits prose into its sentences.
func sentences(text string) []string {
	var out []string
	for _, s := range sentenceEnd.Split(text, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TileUnits returns the units of one tile: each prose sentence of its data
// and each row of a table, members holding identifiers, citations or code
// left out.
func TileUnits(tile tiles.Tile) []Unit {
	var value any
	if json.Unmarshal(tile.Data, &value) != nil {
		return nil
	}
	var out []Unit
	add := func(text string) {
		for _, s := range sentences(text) {
			out = append(out, Unit{Tile: tile.ID, Text: s})
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			add(v)
		case []any:
			// A row of strings is one unit, as a reader reads it.
			if row := stringRow(v); row != "" {
				out = append(out, Unit{Tile: tile.ID, Text: row})
				return
			}
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, key := range sortedKeys(v) {
				if !skipKeys[key] {
					walk(v[key])
				}
			}
		}
	}
	walk(value)
	return out
}

func stringRow(items []any) string {
	if len(items) < 2 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return ""
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// DocumentUnits returns the units of a tile document: each tile's
// (TileUnits), and each risk's text and evidence item's claim, shown in the
// risks and evidence tiles.
func DocumentUnits(doc *tiles.Document) []Unit {
	var out []Unit
	for _, tile := range doc.Tiles {
		out = append(out, TileUnits(tile)...)
	}
	for _, risk := range doc.Risks {
		for _, s := range sentences(risk.Text) {
			out = append(out, Unit{Tile: tiles.TypeRisks, Text: s})
		}
	}
	for _, item := range doc.Evidence {
		for _, s := range sentences(item.Claim) {
			out = append(out, Unit{Tile: tiles.TypeEvidence, Text: s})
		}
	}
	return out
}

// Duplicates returns the pairs of units of different tiles that state the
// same point, where one of the two is in a tile of only (every pair when only
// is empty).
func Duplicates(units []Unit, only map[string]bool) []Pair {
	var out []Pair
	for i := range units {
		for j := i + 1; j < len(units); j++ {
			a, b := units[i], units[j]
			if a.Tile == b.Tile || len(only) > 0 && !only[a.Tile] && !only[b.Tile] {
				continue
			}
			if review.NearDuplicate(a.Text, b.Text) {
				out = append(out, Pair{A: a, B: b})
			}
		}
	}
	return out
}

// DuplicatesTile reports whether an authored tile restates what the
// document's tiles already show: half or more of its units (TileUnits, those
// long enough to compare) state a point another tile states, or it is a
// table or code block a change already holds (its cells or lines found there
// at 80% or more).
func DuplicatesTile(doc *tiles.Document, extra []tiles.Tile, tile tiles.AuthoredTile) bool {
	kind, _, _ := strings.Cut(tile.ID, "_")
	own := TileUnits(tiles.Tile{ID: tile.ID, Type: kind, Data: tile.Data})
	others := DocumentUnits(doc)
	for _, t := range extra {
		if t.ID != tile.ID {
			others = append(others, TileUnits(t)...)
		}
	}
	compared, repeated := 0, 0
	for _, u := range own {
		if review.TextContainment(u.Text, u.Text) == 0 {
			continue // too short to compare
		}
		compared++
		for _, o := range others {
			if o.Tile != tile.ID && review.NearDuplicate(u.Text, o.Text) {
				repeated++
				break
			}
		}
	}
	if compared > 0 && repeated*2 >= compared {
		return true
	}
	for _, block := range tiles.Promotable(doc) {
		if blockRepeats(kind, tile.Data, block.Raw) {
			return true
		}
	}
	return false
}

// blockRepeats reports whether a table or code tile's content is a block's:
// 80% or more of its cells or non-blank lines are in the block.
func blockRepeats(kind string, data, block json.RawMessage) bool {
	var a, b struct {
		Kind    string     `json:"kind"`
		Rows    [][]string `json:"rows"`
		Content string     `json:"content"`
	}
	if json.Unmarshal(data, &a) != nil || json.Unmarshal(block, &b) != nil || b.Kind != kind {
		return false
	}
	var mine []string
	theirs := map[string]bool{}
	switch kind {
	case tiles.TypeTable:
		for _, row := range a.Rows {
			mine = append(mine, row...)
		}
		for _, row := range b.Rows {
			for _, cell := range row {
				theirs[strings.TrimSpace(cell)] = true
			}
		}
	case tiles.TypeCode:
		mine = strings.Split(a.Content, "\n")
		for _, line := range strings.Split(b.Content, "\n") {
			theirs[strings.Join(strings.Fields(line), " ")] = true
		}
	default:
		return false
	}
	n, found := 0, 0
	for _, item := range mine {
		key := strings.Join(strings.Fields(item), " ")
		if key == "" {
			continue
		}
		n++
		if theirs[key] {
			found++
		}
	}
	return n > 0 && found*5 >= n*4
}
