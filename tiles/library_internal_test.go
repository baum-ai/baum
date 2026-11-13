package tiles

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// differentialSchemas are data schemas in the dialect whose validation the
// package's validator and the JSON Schema library must agree on.
var differentialSchemas = []string{
	`{"type":"object","additionalProperties":false,"required":["summary","routes"],"properties":{
		"summary":{"$ref":"#/$defs/markdown"},
		"routes":{"type":"array","maxItems":5,"items":{"type":"object","additionalProperties":false,"required":["method","cites"],"properties":{
			"method":{"enum":["GET","POST",1,null]},
			"path":{"type":"string","minLength":1,"maxLength":30,"pattern":"^/"},
			"tier":{"anyOf":[{"$ref":"#/$defs/tier"},{"type":"null"}]},
			"cites":{"type":"array","minItems":1,"maxItems":3,"items":{"$ref":"#/$defs/citation"}}}}}}}`,
	`{"type":"object","additionalProperties":false,"required":["n","shape"],"properties":{
		"n":{"type":"integer","minimum":-5,"exclusiveMaximum":10},
		"x":{"type":"number","exclusiveMinimum":0.5,"maximum":2.5},
		"flag":{"type":["boolean","null"]},
		"v":{"const":"v1"},
		"both":{"allOf":[{"type":"integer"},{"type":"number","minimum":1}]},
		"shape":{"oneOf":[
			{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"const":"a"},"size":{"type":"integer"}}},
			{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"enum":["a","b"]},"size":{"type":"string","maxLength":3}}}]}}}`,
	`{"type":"object","additionalProperties":false,"properties":{
		"ref":{"$ref":"#/$defs/itemRef"},"anchor":{"$ref":"#/$defs/anchor"},"hunk":{"$ref":"#/$defs/hunkId"},
		"grid":{"type":"array","maxItems":3,"items":{"type":"array","minItems":1,"maxItems":2,"items":{"type":["string","integer"],"maxLength":4}}}}}`,
}

// The package validates custom tile data with its own bounded validator;
// on schemas in the dialect it agrees with the JSON Schema library on every
// sample and on every one-step change of one.
func TestValidatorAgreesWithTheLibrary(t *testing.T) {
	shared, err := reserved()
	if err != nil {
		t.Fatal(err)
	}
	pool := []json.RawMessage{
		json.RawMessage(`{"type":"hunk","id":"h_000000000003","lines":[1,2]}`),
		json.RawMessage(`{"type":"attachment","url":"https://example.com/a.png"}`),
		json.RawMessage(`{"type":"tile","id":"overview"}`),
	}
	for i, schema := range differentialSchemas {
		compiled, problems := compileDataSchema(json.RawMessage(schema))
		if problems != nil {
			t.Fatalf("schema %d: %v", i, problems)
		}
		value, _ := decodeCanonical([]byte(schema))
		library, err := resolveWithLibrary(value.(map[string]any), shared.raw)
		if err != nil {
			t.Fatal(err)
		}
		samples, _, err := EdgeSamples(json.RawMessage(schema), pool)
		if err != nil {
			t.Fatal(err)
		}
		checked, rejected := 0, 0
		for _, sample := range samples {
			var base any
			if err := json.Unmarshal(sample.Data, &base); err != nil {
				t.Fatal(err)
			}
			for _, variant := range append([]any{base}, variants(base)...) {
				encoded, _ := json.Marshal(variant)
				var instance any
				_ = json.Unmarshal(encoded, &instance)
				ours := (&validation{}).validate(compiled.root, instance, nil)
				theirs := library.Validate(instance)
				if (ours == nil) != (theirs == nil) {
					t.Errorf("schema %d, %s: ours %v, the library's %v", i, encoded, ours, theirs)
				}
				checked++
				if ours != nil {
					rejected++
				}
			}
		}
		t.Logf("schema %d: %d instances, %d rejected", i, checked, rejected)
		if rejected == 0 || rejected == checked {
			t.Errorf("schema %d: %d of %d rejected; the variants do not exercise both outcomes", i, rejected, checked)
		}
	}
}

// variants returns copies of a value, each changed in one place.
func variants(value any) []any {
	var out []any
	var walk func(v any, replace func(any) any)
	walk = func(v any, replace func(any) any) {
		for _, changed := range changes(v) {
			out = append(out, replace(changed))
		}
		switch v := v.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(func(yield func(string) bool) {
				for key := range v {
					if !yield(key) {
						return
					}
				}
			}) {
				walk(v[key], func(child any) any {
					copied := clone(v).(map[string]any)
					copied[key] = child
					return replace(copied)
				})
			}
		case []any:
			for i := range v {
				walk(v[i], func(child any) any {
					copied := clone(v).([]any)
					copied[i] = child
					return replace(copied)
				})
			}
		}
	}
	walk(clone(value), func(v any) any { return v })
	return out
}

// changes returns one-step changes of a value.
func changes(v any) []any {
	switch v := v.(type) {
	case map[string]any:
		var out []any
		for key := range v {
			copied := clone(v).(map[string]any)
			delete(copied, key)
			out = append(out, copied)
		}
		withExtra := clone(v).(map[string]any)
		withExtra["extra"] = 1.0
		return append(out, withExtra, []any{}, "text")
	case []any:
		var out []any
		out = append(out, []any{}, map[string]any{})
		if len(v) > 0 {
			out = append(out, append(clone(v).([]any), clone(v[0])), clone(v).([]any)[1:])
		}
		return out
	case string:
		return []any{"", v + strings.Repeat("y", 40), "/" + v, "GET", "a", "b", "high", "h_000000000001", 3.0, nil}
	case float64:
		return []any{v + 1, v - 1, v + 0.5, -v, 10.0, 0.5, 1.0, -5.0, fmt.Sprint(v), true}
	case bool:
		return []any{!v, nil, 0.0}
	case nil:
		return []any{"x", false, 1.0}
	}
	return nil
}

func clone(v any) any {
	encoded, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(encoded, &out)
	return out
}

// The validator checks a citation candidate against the one option of
// $defs/citation its type names; that agrees with checking it against the
// whole oneOf.
func TestCitationDispatchAgrees(t *testing.T) {
	shared, err := reserved()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"type":"hunk","id":"h_000000000001"}`, `{"type":"hunk","id":"h_000000000001","lines":[1,2]}`,
		`{"type":"hunk","id":"h_000000000001","lines":[0,2]}`, `{"type":"hunk","id":"x"}`, `{"type":"hunk"}`,
		`{"type":"file","path":"a.go"}`, `{"type":"file","path":"a.go","extra":1}`, `{"type":"commit","sha":"abc1234"}`,
		`{"type":"commit","sha":"XYZ"}`, `{"type":"pr","number":3}`, `{"type":"pr_body"}`, `{"type":"pr_body","section":null}`,
		`{"type":"pr_comment","id":5}`, `{"type":"review_thread","id":"t"}`, `{"type":"prompt","session":"s","index":1}`,
		`{"type":"prompt","session":"s","index":0}`, `{"type":"session_event","session":"s","event":"e"}`,
		`{"type":"check","id":"c"}`, `{"type":"bot_report","id":"b"}`, `{"type":"issue","system":"linear","key":"K-1"}`,
		`{"type":"spec","page":"SPEC-1"}`, `{"type":"spec","page":"x"}`, `{"type":"external_url","url":"https://example.com"}`,
		`{"type":"attachment"}`, `{"type":"attachment","id":"att_ab12"}`, `{"type":"risk","id":"rsk_a"}`, `{"type":"risk","id":"ev_a"}`,
		`{"type":"evidence","id":"ev_a"}`, `{"type":"change","id":"sec_a"}`, `{"type":"change","id":"sec_A"}`,
		`{"type":"tile","id":"overview"}`, `{"type":"tile","id":"Overview"}`, `{"type":"tile","id":"overview","x":1}`,
	} {
		value, err := decodeData([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		whole := (&validation{}).validate(shared.nodes["citation"], value, nil) == nil
		kind := value.(map[string]any)["type"].(string)
		dispatched := (&validation{}).validate(shared.citations[kind], value, nil) == nil
		if whole != dispatched {
			t.Errorf("%s: the whole citation says %v, its option %v", raw, whole, dispatched)
		}
	}
}
