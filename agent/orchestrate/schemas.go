package orchestrate

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/baum-ai/baum/reviewv2"
)

// The reply schemas of the levels. Every property is required and an absent
// value is an explicit null, so one schema serves providers' strict
// structured outputs; the host checks the limits the schemas cannot
// (checker), so their lengths are generous.

type obj = map[string]any

func str(max int) obj    { return obj{"type": "string", "maxLength": max} }
func text(max int) obj   { return obj{"type": "string", "minLength": 1, "maxLength": max} }
func nullable(o obj) obj { return obj{"anyOf": []any{obj{"type": "null"}, o}} }
func array(items any, min, max int) obj {
	a := obj{"type": "array", "items": items}
	if min > 0 {
		a["minItems"] = min
	}
	if max > 0 {
		a["maxItems"] = max
	}
	return a
}
func enum(values ...string) obj {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return obj{"type": "string", "enum": out}
}

// object is a closed object whose properties are all required.
func object(props obj) obj {
	required := make([]string, 0, len(props))
	for key := range props {
		required = append(required, key)
	}
	slices.Sort(required)
	return obj{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}

const hunkPattern = "^h_[0-9a-f]{12}(-[0-9]+)?$"

// citeSchema is one cite: an anchor to a hunk (with its lines or null), a
// file, a session prompt or command event, a check, a review thread, a
// comment, an attachment or the description.
func citeSchema() obj {
	return obj{"anyOf": []any{
		object(obj{"type": enum("hunk"), "id": obj{"type": "string", "pattern": hunkPattern},
			"lines": nullable(array(obj{"type": "integer", "minimum": 1}, 2, 2))}),
		object(obj{"type": enum("file"), "path": text(400)}),
		object(obj{"type": enum("prompt"), "session": text(200), "index": obj{"type": "integer", "minimum": 0}}),
		object(obj{"type": enum("session_event"), "session": text(200), "event": text(200)}),
		object(obj{"type": enum("check", "review_thread", "pr_comment", "attachment"), "id": text(200)}),
		object(obj{"type": enum("pr_body")}),
	}}
}

func cites(max int) obj { return array(citeSchema(), 0, max) }

// MapSchema is the map's reply schema.
func MapSchema(types []string) []byte {
	section := object(obj{
		"id":            obj{"type": "string", "pattern": "^sec_[a-z0-9_]{1,60}$"},
		"title":         text(120),
		"kind":          enum("logical", "mechanical"),
		"hunks":         array(obj{"type": "string", "pattern": hunkPattern}, 0, 0),
		"gist":          text(300),
		"scope":         nullable(text(400)),
		"level":         nullable(obj{"type": "integer", "minimum": 1, "maximum": 5}),
		"level_reasons": array(object(obj{"text": text(500), "cites": cites(reviewv2.MaxCites)}), 0, 3),
	})
	tile := object(obj{
		"id":    obj{"type": "string", "pattern": "^tl_[a-z0-9_]{1,60}$"},
		"type":  enum(types...),
		"owner": text(80),
		"shows": text(600),
		"hunks": array(obj{"type": "string", "pattern": hunkPattern}, 0, 0),
	})
	return encode(object(obj{
		"change_kind": enum(reviewv2.ChangeKinds...),
		"sections":    array(section, 1, 0),
		"tiles":       array(tile, 0, 12),
	}))
}

// SectionSchema is a section agent's reply schema.
func SectionSchema(types []string) []byte {
	// A code tile's focus: the new-side lines the page highlights.
	focus := nullable(array(obj{"type": "integer", "minimum": 1}, 1, reviewv2.MaxCodeFocus))
	brief := object(obj{"id": text(80), "shows": text(600), "cites": cites(reviewv2.MaxCites), "focus": focus, "drop": obj{"type": "boolean"}, "reason": nullable(text(400))})
	point := object(obj{"text": text(450), "more": nullable(text(1200)), "cites": cites(reviewv2.MaxCites)})
	// A block uses the fields of its type; the others are null.
	block := object(obj{
		"type":  enum(reviewv2.BlockProse, reviewv2.BlockPoints, reviewv2.BlockNote, reviewv2.BlockTile),
		"title": nullable(text(160)), "text": nullable(text(1200)), "items": nullable(array(point, 1, reviewv2.MaxPoints)),
		"tile": nullable(text(80)),
	})
	return encode(object(obj{
		"body": array(block, 1, reviewv2.MaxBlocks),
		"risks": array(object(obj{
			"id": obj{"type": "string", "pattern": "^rsk_[a-z0-9_]{1,60}$"}, "tier": enum(reviewv2.RiskTiers...),
			"category": obj{"type": "string", "pattern": "^[a-z0-9_]{1,40}$"}, "text": text(1200), "cites": array(citeSchema(), 1, reviewv2.MaxCites),
		}), 0, 8),
		"tiles":   array(brief, 0, 0),
		"propose": nullable(object(obj{"id": obj{"type": "string", "pattern": "^tl_[a-z0-9_]{1,60}$"}, "type": enum(types...), "shows": text(600), "cites": cites(reviewv2.MaxCites), "focus": focus})),
	}))
}

// ContextSchema is the context agent's reply schema.
func ContextSchema() []byte {
	part := object(obj{"text": str(800), "files": array(text(400), 0, 12)})
	return encode(object(obj{"area": part, "reached_by": part, "contracts": part, "related": part}))
}

// OverviewSchema is the overview's reply schema.
func OverviewSchema() []byte {
	return encode(object(obj{
		"summary": text(1500),
		"verdict": object(obj{
			"decision": enum(reviewv2.Decisions...), "text": text(300),
			"points": array(object(obj{"text": text(500), "section": nullable(text(80))}), 0, 3),
		}),
		"risk_why":    str(600),
		"screenshots": array(text(200), 0, reviewv2.MaxScreenshots),
		"videos":      array(text(200), 0, reviewv2.MaxVideos),
		"diagram":     nullable(text(80)),
		"intent": array(object(obj{
			"session": text(200), "prompt": obj{"type": "integer", "minimum": 0}, "asked": text(400), "got": text(400),
		}), 0, 3),
	}))
}

func encode(o obj) []byte {
	encoded, err := json.Marshal(o)
	if err != nil {
		panic(fmt.Sprintf("orchestrate: a reply schema does not encode: %v", err))
	}
	return encoded
}
