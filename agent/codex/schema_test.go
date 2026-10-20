package codex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/baum-ai/baum/review"
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

// The model schema leaves an attention reference's spelling to normalization
// but not its kind: a section's own ID or a hunk ID cannot be generated.
func TestCodexOutputSchemaLimitsAttentionReferencesToItemKinds(t *testing.T) {
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	model, err := validator.ModelSchema(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := StrictSchema(model)
	if err != nil {
		t.Fatal(err)
	}
	var strict map[string]any
	if err := json.Unmarshal(encoded, &strict); err != nil {
		t.Fatal(err)
	}
	properties := strict["properties"].(map[string]any)
	section := properties["sections"].(map[string]any)["items"].(map[string]any)
	refs := attentionRefs(t, section)
	pattern, ok := refs["pattern"].(string)
	if !ok {
		t.Fatalf("section attention refs have no pattern: %v", refs)
	}
	// OpenAI strict mode supports patterns but not flags or lookarounds.
	for _, unsupported := range []string{"(?", "\\b", "\\d", "\\w"} {
		if strings.Contains(pattern, unsupported) {
			t.Errorf("pattern %q uses %s", pattern, unsupported)
		}
	}
	expression := regexp.MustCompile(pattern)
	for _, ref := range []string{"ev_x", "rsk_retry_herd", "RSK_x", "Dec-x", "EV Retry Herd", "dec_api_header"} {
		if !expression.MatchString(ref) {
			t.Errorf("pattern %q rejects %q", pattern, ref)
		}
	}
	for _, ref := range []string{"sec_sync", "h_0123456789ab", "retry_herd", "evidence_x", "ev", "ev_", "SEC_ev_x"} {
		if expression.MatchString(ref) {
			t.Errorf("pattern %q accepts %q", pattern, ref)
		}
	}
	// Summary attention names sections, so it stays unconstrained.
	summary := properties["summary"].(map[string]any)
	if pattern, ok := attentionRefs(t, summary)["pattern"]; ok {
		t.Errorf("summary attention refs have pattern %v", pattern)
	}
}

func attentionRefs(t *testing.T, owner map[string]any) map[string]any {
	t.Helper()
	attention := owner["properties"].(map[string]any)["attention"].(map[string]any)
	for _, variant := range asSlice(attention["anyOf"]) {
		object, _ := variant.(map[string]any)
		if properties, ok := object["properties"].(map[string]any); ok {
			return properties["refs"].(map[string]any)["items"].(map[string]any)
		}
	}
	t.Fatalf("attention has no object variant: %v", attention)
	return nil
}
