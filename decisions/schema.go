package decisions

import (
	"bytes"
	"encoding/json"
)

// orderedObject is a JSON object whose keys marshal in insertion order. A
// map[string]any marshals its keys alphabetically, but OpenAI strict
// structured outputs generate fields in schema property order, so the order
// of "properties" decides what the model writes first: the summary before the
// labels derived from it, the turns before the files chosen from them.
type orderedObject []orderedField

type orderedField struct {
	Key   string
	Value any
}

// MarshalJSON implements json.Marshaler.
func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, field := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// ExtractSchema returns the JSON Schema of the model's reply,
// {"decisions": [...]}. It is valid under OpenAI strict structured outputs, so
// optional values have an empty form: "" for no rationale, rejected
// alternative or evidence, and decided_turn 0 for none.
// Each "properties" value is an orderedObject, so the fields marshal in the
// order the model should write them.
func ExtractSchema() map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	decision := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []any{
			"summary", "title", "who", "category", "trigger", "rationale", "rejected_alternative", "superseded",
			"turns", "decided_turn", "evidence", "files",
		},
		"properties": orderedObject{
			{"summary", map[string]any{"type": "string"}},
			{"title", map[string]any{"type": "string", "minLength": 1, "maxLength": 80, "description": "A concise action-led title, usually 3–8 words, naming the choice. For example: Exclude binary files. Keep the explanation in summary."}},
			{"who", map[string]any{
				"type": "string",
				"enum": []any{string(WhoHuman), string(WhoAgent), string(WhoApproved)},
			}},
			{"category", map[string]any{
				"type": "string",
				"enum": []any{
					string(CategoryArchitecture), string(CategoryDataModel), string(CategoryInterface),
					string(CategoryBehavior), string(CategoryImplementation), string(CategoryDependency),
					string(CategoryTesting), string(CategoryScope), string(CategoryProcess),
				},
			}},
			{"trigger", map[string]any{
				"type": "string",
				"enum": []any{
					string(TriggerRequest), string(TriggerReview), string(TriggerFailure),
					string(TriggerFinding), string(TriggerInitiative),
				},
			}},
			{"rationale", map[string]any{"type": "string"}},
			{"rejected_alternative", map[string]any{"type": "string"}},
			{"superseded", map[string]any{"type": "boolean"}},
			{"turns", map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}},
			{"decided_turn", map[string]any{"type": "integer"}},
			{"evidence", map[string]any{"type": "string"}},
			{"files", stringArray},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"decisions"},
		"properties": orderedObject{
			{"decisions", map[string]any{"type": "array", "items": decision}},
		},
	}
}
