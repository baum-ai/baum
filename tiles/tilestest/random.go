package tilestest

import (
	"encoding/json"
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
