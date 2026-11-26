package agentcontext

import (
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/tiles/view"
)

// BuiltinType is a tile type the review page draws with its own components
// that add_tile takes: the native diagrams, the code excerpt and images.
// Their data is the agent's and always given; nothing fills them.
type BuiltinType struct {
	Name, Title, UseWhen string
	// Schema is the data add_tile takes for the type.
	Schema json.RawMessage
	// Note is how to write the data.
	Note string
}

// The data add_tile takes for the code and images types.
const (
	codeAddSchema   = `{"type":"object","additionalProperties":false,"required":["hunk"],"properties":{"hunk":{"type":"string","description":"A hunk's id, as the diff tool gives it."},"lines":{"type":"array","items":{"type":"integer","minimum":1},"minItems":2,"maxItems":2,"description":"The first and last line on the hunk's new side; without it, the whole hunk. At most 80 lines."},"focus":{"type":"array","items":{"type":"integer","minimum":1},"minItems":1,"maxItems":3,"description":"The one to three new-side lines that carry the point, within the excerpt; the page highlights them and dims the rest."}}}`
	imagesAddSchema = `{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"description":"The image file's absolute path on this computer: PNG, JPEG, GIF or WebP."},"caption":{"type":"string","maxLength":200,"description":"What the image shows, one plain line."}}}}}}`
)

const diagramNote = "Write the diagram as fill_schema gives it, from this pull request's diff and checkout: every field, null where it has nothing; source lists the ids of the hunks it rests on, as the diff tool gives them. Plain text, within each maxLength: the host refuses a text over its limit or a source that is not a hunk of the version shown, never cutting it."

// BuiltinTypes are the built-in types add_tile takes, in the order it
// lists them. internal/uiserver adds them (NativeAddTypes, the same names).
var BuiltinTypes = []BuiltinType{
	{Name: view.TypeFlow, Title: "Flow", UseWhen: "how a request, a job or a person moves through the parts, step by step, with lanes for who or what each path follows", Schema: view.DiagramSchema(view.TypeFlow), Note: diagramNote},
	{Name: view.TypeColumns, Title: "Columns", UseWhen: "two or three lists side by side the reader compares: allowed and refused, kept and removed; or, with rows, before and after compared item by item", Schema: view.DiagramSchema(view.TypeColumns), Note: diagramNote},
	{Name: view.TypeSequence, Title: "Sequence", UseWhen: "messages between two to four parties in order: a protocol, a handshake, a request across processes", Schema: view.DiagramSchema(view.TypeSequence), Note: diagramNote},
	{Name: view.TypeTable, Title: "Table", UseWhen: "values a reader compares: limits, codes, states, who may do what", Schema: view.DiagramSchema(view.TypeTable), Note: diagramNote},
	{Name: "code", Title: "Code", UseWhen: "the few lines of one hunk the reader must see; the host reads them from the clone", Schema: json.RawMessage(codeAddSchema),
		Note: "Name a hunk of the version shown and, optionally, the first and last line of its new side and the one to three lines of it that matter (focus). The host reads the code at the version's head: never write the code yourself."},
	{Name: "images", Title: "Images", UseWhen: "screenshots or other images of what the change looks like: UI states, charts, recordings' frames", Schema: json.RawMessage(imagesAddSchema),
		Note: "Save each image to a file first (a screenshot you took, or an image downloaded from the pull request's description), then pass its absolute path. To screenshot the repository's app, use the way its agent instructions (CLAUDE.md, AGENTS.md, their guides) describe, if they do, rather than writing your own capture. The host reads, downscales and stores it with this pull request's added tiles; never pass image bytes. Pass title for the tile's heading."},
}

// builtinType returns the built-in type named.
func builtinType(name string) (BuiltinType, bool) {
	for _, t := range BuiltinTypes {
		if t.Name == name {
			return t, true
		}
	}
	return BuiltinType{}, false
}

// builtinNames lists the built-in types, each with its title.
func builtinNames() string {
	names := make([]string, 0, len(BuiltinTypes))
	for _, t := range BuiltinTypes {
		names = append(names, t.Name+" ("+t.Title+")")
	}
	return strings.Join(names, ", ")
}

// builtinTypeRead is tile_type's answer for a built-in type.
func builtinTypeRead(t BuiltinType) (string, error) {
	return compactJSON(map[string]any{"type": t.Name, "title": t.Title, "built_in": true, "use_when": t.UseWhen,
		"fill_schema": t.Schema, "fill_instructions": t.Note,
		"note": "Pass the data to add_tile with this type; it shows at once. A built-in type has no definition or source to read or change."})
}
