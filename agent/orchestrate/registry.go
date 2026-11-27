package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// TypeCode is the code tile: an excerpt of one hunk, {hunk, lines}, whose
// text the host fills (reviewv2.Complete). No model writes it.
const TypeCode = "code"

// TileType is one entry of the tile type registry: what the map is offered
// and how a planned tile of the type is filled.
type TileType struct {
	// Name is what a planned tile's type names; Title says it in words.
	Name, Title string
	// UseWhen is the line the map reads to choose it.
	UseWhen string
	// Dedicated marks a library type (baum's own, such as schema_change, or
	// one of the person's saved types), filled through the library's fill
	// path with its own instructions and checks; the others are inline,
	// drawn by the page's own components.
	Dedicated bool
	// Host marks a type the host fills without a call (code).
	Host bool
	// Schema is the data the type's tile agent replies with: a native type's
	// diagram schema (view.DiagramSchema), a library type's fill schema, the
	// code tile's {hunk, lines}.
	Schema json.RawMessage
	// Entry is a dedicated type's library entry.
	Entry *library.Entry
}

// Fill is the type's fill instructions: a native type's part of the tile
// prompt, a library type's own, none for the code tile.
func (t TileType) Fill(p agent.ReviewV2Prompt) string {
	switch {
	case t.Entry != nil:
		return t.Entry.Definition.Fill
	case t.Host:
		return ""
	}
	return p.Natives[t.Name]
}

// Registry is the tile types a run offers, in the order the map reads them:
// the native types, the code tile, then the dedicated ones. It lives with
// the orchestration because only the orchestration reads it, and because a
// native type's fill instructions are prompt text (agent/prompt), which the
// tiles packages do not depend on.
type Registry struct {
	types []TileType
}

// The use-when lines of the native types.
var nativeUseWhen = map[string]string{
	view.TypeFlow:     "how a request, a job or a person moves through the parts, step by step, with lanes for who or what each path follows",
	view.TypeColumns:  "two or three lists side by side the reader compares: allowed and refused, kept and removed; or, with rows, before and after compared item by item",
	view.TypeSequence: "messages between two to four parties in order: a protocol, a handshake, a request across processes",
	view.TypeTable:    "values a reader compares: limits, codes, states, who may do what",
}

var nativeTitles = map[string]string{
	view.TypeFlow: "Flow", view.TypeColumns: "Columns", view.TypeSequence: "Sequence", view.TypeTable: "Table",
}

// codeSchema is the code tile's data: one hunk and the lines to show.
const codeSchema = `{"type":"object","additionalProperties":false,"required":["hunk","lines"],"properties":{"hunk":{"type":"string"},"lines":{"anyOf":[{"type":"null"},{"type":"array","items":{"type":"integer","minimum":1},"minItems":2,"maxItems":2}]}}}`

// NewRegistry is the registry of the native types, the code tile and the
// dedicated library types given: baum's library (library.All) and the
// person's saved types the run offers. A dedicated type whose fill schema
// cannot be made is left out; one whose name a native type takes too.
func NewRegistry(dedicated []library.Entry) *Registry {
	r := &Registry{}
	for _, kind := range view.NativeTypes {
		r.types = append(r.types, TileType{Name: kind, Title: nativeTitles[kind], UseWhen: nativeUseWhen[kind], Schema: view.DiagramSchema(kind)})
	}
	r.types = append(r.types, TileType{Name: TypeCode, Title: "Code", Host: true, Schema: json.RawMessage(codeSchema),
		UseWhen: "the few lines of one hunk the reader must see to judge the section; the host shows them"})
	for i := range dedicated {
		e := dedicated[i]
		if _, taken := r.Get(e.Definition.Name); taken {
			continue
		}
		schema, err := e.FillSchema()
		if err != nil {
			continue
		}
		r.types = append(r.types, TileType{Name: e.Definition.Name, Title: oneLine(e.Definition.Title, 100), UseWhen: oneLine(e.Definition.UseWhen, 280),
			Dedicated: true, Schema: schema, Entry: &e})
	}
	return r
}

// Get returns the type named.
func (r *Registry) Get(name string) (TileType, bool) {
	for _, t := range r.types {
		if t.Name == name {
			return t, true
		}
	}
	return TileType{}, false
}

// Offer is the list of types the map reads: one line each with its name,
// whether it is inline or dedicated, and when to use it.
func (r *Registry) Offer() string {
	var b strings.Builder
	for _, t := range r.types {
		kind := "inline"
		if t.Dedicated {
			kind = "dedicated"
		}
		fmt.Fprintf(&b, "- `%s` (%s, %s): %s\n", t.Name, t.Title, kind, t.UseWhen)
	}
	return b.String()
}

func (r *Registry) names() []string {
	out := make([]string, 0, len(r.types))
	for _, t := range r.types {
		out = append(out, t.Name)
	}
	return out
}

// oneLine folds whitespace and bounds text to limit runes.
func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}
