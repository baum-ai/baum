package tiles

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/schema"
)

// The converter decodes parts of a review into structs and rejects a field
// they do not have, so every property the review schema defines for those
// objects needs a field: a property the schema adds must fail this test, not
// send every new review to the fallback page.
func TestReviewStructsCoverTheReviewSchema(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(schema.Document, &root); err != nil {
		t.Fatal(err)
	}
	at := func(value any, path ...string) map[string]any {
		for _, key := range path {
			value = value.(map[string]any)[key]
		}
		return value.(map[string]any)
	}
	block := func(kind string) map[string]any {
		for _, raw := range at(root, "$defs", "block")["oneOf"].([]any) {
			branch := raw.(map[string]any)
			if at(branch, "properties", "kind")["const"] == kind {
				return branch
			}
		}
		t.Fatalf("no %s block", kind)
		return nil
	}
	for _, tc := range []struct {
		name   string
		object map[string]any
		value  any
	}{
		{"document", root, reviewDocument{}},
		{"summary", at(root, "properties", "summary"), reviewSummary{}},
		{"section", at(root, "$defs", "section"), reviewSection{}},
		{"risk", at(root, "$defs", "section", "properties", "risks", "items"), reviewRisk{}},
		{"evidence", at(root, "$defs", "section", "properties", "evidence", "items"), reviewEvidence{}},
		{"intent block", block("intent"), intentBlock{}},
		{"decisions block", block("decisions"), decisionsBlock{}},
	} {
		tags := jsonTags(reflect.TypeOf(tc.value))
		var properties []string
		for name := range at(tc.object, "properties") {
			properties = append(properties, name)
			if !slices.Contains(tags, name) {
				t.Errorf("%s: the schema's property %q has no field", tc.name, name)
			}
		}
		for _, tag := range tags {
			if !slices.Contains(properties, tag) {
				t.Errorf("%s: field %q is not a property of the schema", tc.name, tag)
			}
		}
	}
}

func jsonTags(typ reflect.Type) []string {
	var tags []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			tags = append(tags, name)
		}
	}
	return tags
}

func TestCheckKeyCase(t *testing.T) {
	for _, tc := range []struct {
		document string
		ok       bool
	}{
		{`{"schema":"x","summary":{"text":"A \"Quoted\" Text"},"index":{"Hunks":[{"ID":1}]}}`, true},
		{`{"sections":[{"id":"sec_a","risks":[{"id":"rsk_a"}],"blocks":[{"kind":"intent"}],"derived":{"Role":1}}]}`, true},
		{`{"Summary":{}}`, false},
		{`{"summary":{"Text":""}}`, false},
		{`{"sections":[{},{"Title":""}]}`, false},
		{`{"sections":[{"risks":[{"te\u0078t":""}]}]}`, false},
		{`{"sections":[{"evidence":[{"claim":"","Kind":""}]}]}`, false},
		{`{"sections":[{"blocks":[{"KIND":"intent"}]}]}`, false},
	} {
		if err := checkKeyCase([]byte(tc.document)); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok %v", tc.document, err, tc.ok)
		}
	}
}
