package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/baum-ai/baum/schema"
)

// OpenAI strict structured outputs reject the lenient contract as written;
// these are the rules the live API enforced on it.
func TestCodexOutputSchemaMeetsStrictModeRules(t *testing.T) {
	encoded, err := StrictSchema(schema.Document)
	if err != nil {
		t.Fatal(err)
	}
	var strict any
	if err := json.Unmarshal(encoded, &strict); err != nil {
		t.Fatal(err)
	}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		n, ok := node.(map[string]any)
		if !ok {
			return
		}
		for _, keyword := range []string{"$ref", "$defs", "allOf", "oneOf", "if", "then", "else", "not", "contains", "uniqueItems"} {
			if _, found := n[keyword]; found {
				t.Errorf("%s: strict mode rejects %s", path, keyword)
			}
		}
		if format, ok := n["format"].(string); ok && !strictSchemaFormats[format] {
			t.Errorf("%s: unsupported format %q", path, format)
		}
		_, hasConst := n["const"]
		if _, typed := n["type"]; !typed && (hasConst || n["enum"] != nil) {
			t.Errorf("%s: literal without type", path)
		}
		if properties, ok := n["properties"].(map[string]any); ok {
			if n["additionalProperties"] != false || len(asSlice(n["required"])) != len(properties) {
				t.Errorf("%s: object is not closed with every property required", path)
			}
			for name, property := range properties {
				walk(property, path+"/"+name)
			}
		}
		for _, key := range []string{"items", "additionalProperties"} {
			walk(n[key], path+"/"+key)
		}
		for i, variant := range asSlice(n["anyOf"]) {
			walk(variant, fmt.Sprintf("%s/anyOf/%d", path, i))
		}
	}
	walk(strict, "#")
}
