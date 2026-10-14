// Package codex runs the Codex CLI as an agent.Backend and derives the strict
// structured-output schema it passes to OpenAI.
package codex

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StrictSchema derives the schema Codex passes to OpenAI's strict
// structured outputs from the lenient authored contract. Strict mode requires
// every property and a closed object, and rejects conditionals, oneOf and
// most string formats and uniqueItems; it also fails on chained $refs, so
// they are inlined.
// Optional properties become nullable, and normalization removes the nulls.
// Dropped constraints stay with the validator and the repair loop. schema is a
// JSON Schema whose $refs point into its own $defs; a host that calls a
// provider's strict structured outputs itself, such as a stack review, uses
// it too.
func StrictSchema(schema []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, err
	}
	defs, _ := root["$defs"].(map[string]any)
	inlined, err := inlineSchemaRefs(root, defs, nil)
	if err != nil {
		return nil, err
	}
	return json.Marshal(strictSchema(inlined))
}

func inlineSchemaRefs(node any, defs map[string]any, stack []string) (any, error) {
	switch n := node.(type) {
	case []any:
		out := make([]any, len(n))
		for i, item := range n {
			inlined, err := inlineSchemaRefs(item, defs, stack)
			if err != nil {
				return nil, err
			}
			out[i] = inlined
		}
		return out, nil
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/$defs/")
			for _, seen := range stack {
				if seen == name {
					return nil, fmt.Errorf("recursive schema reference %s", ref)
				}
			}
			def, ok := defs[name]
			if !ok {
				return nil, fmt.Errorf("unknown schema reference %s", ref)
			}
			inlined, err := inlineSchemaRefs(def, defs, append(stack, name))
			if err != nil {
				return nil, err
			}
			out := map[string]any{}
			for k, v := range inlined.(map[string]any) {
				out[k] = v
			}
			for k, v := range n {
				if k != "$ref" {
					out[k] = v
				}
			}
			return out, nil
		}
		out := map[string]any{}
		for k, v := range n {
			switch k {
			case "$defs":
			case "enum", "const", "required":
				out[k] = v
			default:
				inlined, err := inlineSchemaRefs(v, defs, stack)
				if err != nil {
					return nil, err
				}
				out[k] = inlined
			}
		}
		return out, nil
	}
	return node, nil
}

var strictSchemaFormats = map[string]bool{"date-time": true, "date": true, "time": true, "duration": true,
	"email": true, "hostname": true, "ipv4": true, "ipv6": true, "uuid": true}

func strictSchema(node any) any {
	n, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := map[string]any{}
	for k, v := range n {
		switch k {
		case "allOf", "if", "then", "else", "not", "contains", "uniqueItems", "$comment":
		case "format":
			if format, _ := v.(string); strictSchemaFormats[format] {
				out[k] = v
			}
		case "properties":
			properties := map[string]any{}
			for name, property := range v.(map[string]any) {
				properties[name] = strictSchema(property)
			}
			out[k] = properties
		case "anyOf", "oneOf", "prefixItems":
			var variants []any
			for _, variant := range v.([]any) {
				variants = append(variants, strictSchema(variant))
			}
			if k == "oneOf" {
				k = "anyOf"
			}
			out[k] = variants
		case "items", "additionalProperties":
			out[k] = strictSchema(v)
		default:
			out[k] = v
		}
	}
	if _, typed := out["type"]; !typed {
		if t := literalSchemaType(out); t != nil {
			out["type"] = t
		}
	}
	if properties, ok := out["properties"].(map[string]any); ok {
		required := map[string]bool{}
		for _, name := range asSlice(out["required"]) {
			required[name.(string)] = true
		}
		names := make([]any, 0, len(properties))
		for name, property := range properties {
			if !required[name] && !schemaAllowsNull(property) {
				properties[name] = map[string]any{"anyOf": []any{property, map[string]any{"type": "null"}}}
			}
			names = append(names, name)
		}
		out["required"] = names
		out["additionalProperties"] = false
	}
	return out
}

// literalSchemaType names the type of a const or enum schema, which strict
// mode requires alongside the literal.
func literalSchemaType(schema map[string]any) any {
	values := asSlice(schema["enum"])
	if c, ok := schema["const"]; ok {
		values = []any{c}
	}
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var types []any
	for _, value := range values {
		t := "null"
		switch v := value.(type) {
		case bool:
			t = "boolean"
		case string:
			t = "string"
		case float64:
			t = "number"
			if v == float64(int64(v)) {
				t = "integer"
			}
		}
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	if len(types) == 1 {
		return types[0]
	}
	return types
}

func schemaAllowsNull(node any) bool {
	n, _ := node.(map[string]any)
	switch t := n["type"].(type) {
	case string:
		if t == "null" {
			return true
		}
	case []any:
		for _, name := range t {
			if name == "null" {
				return true
			}
		}
	}
	for _, variant := range asSlice(n["anyOf"]) {
		if schemaAllowsNull(variant) {
			return true
		}
	}
	return false
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
