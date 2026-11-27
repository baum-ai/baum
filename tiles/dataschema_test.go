package tiles_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/tiles"
)

// closed wraps properties in a closed top-level object schema.
func closed(properties string, extra ...string) string {
	fields := append([]string{`"type":"object"`, `"additionalProperties":false`, `"properties":{` + properties + `}`}, extra...)
	return "{" + strings.Join(fields, ",") + "}"
}

func TestCheckDataSchemaAccepts(t *testing.T) {
	for name, schema := range map[string]string{
		"an empty object": `{"type":"object","additionalProperties":false}`,
		"the reserved definitions": closed(`"cite":{"$ref":"#/$defs/citation"},"text":{"$ref":"#/$defs/markdown"},"tier":{"$ref":"#/$defs/tier"},` +
			`"hunk":{"$ref":"#/$defs/hunkId"},"item":{"$ref":"#/$defs/itemRef"},"anchor":{"$ref":"#/$defs/anchor"}`),
		"every allowed keyword": `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$comment": "A table of findings.", "title": "Findings", "description": "What the tile shows.",
			"type": "object", "additionalProperties": false, "required": ["rows"],
			"examples": [{"rows": []}], "default": {"rows": []},
			"properties": {
				"rows": {"type": "array", "minItems": 0, "maxItems": 500, "items": {"$ref": "#/$defs/row"}},
				"kind": {"enum": ["a", "b", 1, true, null]},
				"version": {"const": 2},
				"name": {"type": "string", "minLength": 1, "maxLength": 20000},
				"slug": {"type": "string", "maxLength": 40, "pattern": "^[a-z0-9-]{1,40}$"},
				"score": {"type": "number", "minimum": 0, "maximum": 1, "exclusiveMinimum": -1, "exclusiveMaximum": 2},
				"count": {"type": ["integer", "null"]},
				"either": {"anyOf": [{"type": "string", "maxLength": 10}, {"oneOf": [{"type": "integer"}, {"type": "null"}]}]},
				"both": {"allOf": [{"type": "integer"}, {"type": "number", "minimum": 0}, {"type": "integer"}, {"type": "number"}]},
				"flag": {"type": "boolean", "description": "Annotated.", "title": "Flag"}
			},
			"$defs": {
				"row": {"type": "object", "additionalProperties": false, "required": ["text", "cites"], "properties": {
					"text": {"type": "string", "maxLength": 300},
					"cites": {"type": "array", "maxItems": 8, "items": {"$ref": "#/$defs/citation"}}
				}}
			}
		}`,
		"patterns both engines read alike": closed(`"a":{"type":"string","maxLength":40,"pattern":"^(?:ab|c)(?<tail>[a-z\\]\\[]*)\\x41é$"},` +
			`"b":{"type":"string","maxLength":40,"pattern":"^[]a]*[^]b]?\\(?\\d+\\s\\w\\b$"}`),
		"twelve levels through $ref": `{"type":"object","additionalProperties":false,"properties":{"a":{"$ref":"#/$defs/l1"}},"$defs":{
			"l1":{"type":"object","additionalProperties":false,"properties":{"b":{"$ref":"#/$defs/l2"}}},
			"l2":{"type":"object","additionalProperties":false,"properties":{"c":{"$ref":"#/$defs/l3"}}},
			"l3":{"type":"object","additionalProperties":false,"properties":{"d":{"$ref":"#/$defs/l4"}}},
			"l4":{"type":"object","additionalProperties":false,"properties":{"e":{"$ref":"#/$defs/l5"}}},
			"l5":{"type":"object","additionalProperties":false,"properties":{"f":{"type":"string","maxLength":1}}}}}`,
	} {
		if problems := tiles.CheckDataSchema(json.RawMessage(schema)); problems != nil {
			t.Errorf("%s: %v", name, problems)
		}
	}
}

// Every rule of the dialect, and the message that tells an author how to
// fix it.
func TestCheckDataSchemaRules(t *testing.T) {
	str := `{"type":"string","maxLength":10}`
	for _, tc := range []struct {
		name, schema, want string
	}{
		// Structure.
		{"not an object", `[]`, "the schema is not an object"},
		{"not JSON", `{"type":`, "not valid JSON"},
		{"a top level that is not an object", `{"type":"string","maxLength":1}`, `# : the top level must have "type": "object"`},
		{"a nullable top level", `{"type":["object","null"],"additionalProperties":false}`, `the top level must have "type": "object"`},
		{"larger than 32 KiB", closed(`"a":{"type":"string","maxLength":1,"description":"` + strings.Repeat("x", 33<<10) + `"}`), "larger than 32 KiB"},
		{"513 schema objects", closed(manyProperties(512)), "513 schema objects, more than 512"},
		{"13 levels deep", nested(12), "nests 13 schema objects deep counting through $ref, more than 12"},
		{"a boolean schema", closed(`"a":true`), "#/properties/a: is not a schema object"},
		{"another draft", closed(``, `"$schema":"http://json-schema.org/draft-07/schema#"`), "#/$schema: only https://json-schema.org/draft/2020-12/schema is supported"},
		{"a nested $schema", closed(`"a":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","maxLength":1}`), "#/properties/a/$schema: $schema may appear only at the top level"},
		{"nested $defs", closed(`"a":{"type":"string","maxLength":1,"$defs":{}}`), "#/properties/a/$defs: $defs may appear only at the top level"},
		{"a value without a type", closed(`"a":{"description":"Anything."}`), `#/properties/a: say what the value is: add "type"`},
		{"an unknown type", closed(`"a":{"type":"date"}`), "#/properties/a/type: must be one of object"},
		{"a repeated type", closed(`"a":{"type":["string","string"],"maxLength":1}`), "#/properties/a/type: must be one of"},

		// Objects.
		{"an open object", `{"type":"object","properties":{}}`, `# : an object needs "additionalProperties": false`},
		{"a nested open object", closed(`"a":{"type":"object","properties":{"b":` + str + `}}`), `#/properties/a: an object needs "additionalProperties": false`},
		{"additionalProperties true", closed(`"a":{"type":"object","properties":{},"additionalProperties":true}`), "#/properties/a/additionalProperties: must be false"},
		{"additionalProperties a schema", closed(`"a":{"type":"object","properties":{},"additionalProperties":{"type":"string"}}`), "#/properties/a/additionalProperties: must be false"},
		{"properties on a string", closed(`"a":{"type":"string","maxLength":1,"properties":{}}`), "#/properties/a/properties: properties applies only to objects"},
		{"required without the property", closed(`"a":`+str, `"required":["b"]`), `#/required: names the property "b", which properties does not define`},
		{"required twice", closed(`"a":`+str, `"required":["a","a"]`), `#/required: names the property "a" twice`},
		{"required not names", closed(`"a":`+str, `"required":[1]`), "#/required: must hold only property names"},

		// Arrays.
		{"an array without maxItems", closed(`"a":{"type":"array","items":` + str + `}`), `#/properties/a: an array needs "maxItems" (at most 500)`},
		{"an array without items", closed(`"a":{"type":"array","maxItems":3}`), `#/properties/a: an array needs "items"`},
		{"maxItems over 500", closed(`"a":{"type":"array","maxItems":501,"items":` + str + `}`), "#/properties/a/maxItems: is 501, more than 500"},
		{"minItems over maxItems", closed(`"a":{"type":"array","minItems":4,"maxItems":3,"items":` + str + `}`), "#/properties/a/minItems: is greater than maxItems"},
		{"a negative maxItems", closed(`"a":{"type":"array","maxItems":-1,"items":` + str + `}`), "#/properties/a/maxItems: must be a non-negative integer"},
		{"a fractional maxItems", closed(`"a":{"type":"array","maxItems":2.5,"items":` + str + `}`), "#/properties/a/maxItems: must be a non-negative integer"},
		{"tuple items", closed(`"a":{"type":"array","maxItems":2,"items":[` + str + `]}`), "#/properties/a/items: must be one schema object"},
		{"maxItems on a string", closed(`"a":{"type":"string","maxLength":1,"maxItems":1}`), "#/properties/a/maxItems: maxItems applies only to arrays"},

		// Strings.
		{"a string without maxLength", closed(`"a":{"type":"string"}`), `#/properties/a: a string needs "maxLength" (at most 20000)`},
		{"a nullable string without maxLength", closed(`"a":{"type":["string","null"]}`), `a string needs "maxLength"`},
		{"maxLength over 20000", closed(`"a":{"type":"string","maxLength":20001}`), "#/properties/a/maxLength: is 20001, more than 20000"},
		{"minLength over maxLength", closed(`"a":{"type":"string","minLength":5,"maxLength":4}`), "#/properties/a/minLength: is greater than maxLength"},
		{"a pattern over 256 characters", closed(`"a":{"type":"string","pattern":"` + strings.Repeat("a", 257) + `"}`), "#/properties/a/pattern: is longer than 256 characters"},
		{"a pattern with lookahead", closed(`"a":{"type":"string","maxLength":9,"pattern":"^(?=a)"}`), "#/properties/a/pattern: does not compile (invalid or unsupported Perl syntax); use the syntax Go and JavaScript share"},
		{"a pattern with a backreference", closed(`"a":{"type":"string","maxLength":9,"pattern":"(a)\\1"}`), "#/properties/a/pattern: does not compile (invalid escape sequence)"},
		{"a pattern too large to run", closed(`"a":{"type":"string","pattern":"^(a{50}){10}$"}`), "more than 400"},
		{"a pattern without maxLength", closed(`"a":{"type":"string","pattern":"^[a-z]{1,9}$"}`), `#/properties/a: a string needs "maxLength" (at most 20000), unless it has enum or const`},
		{"a flag group", closed(`"a":{"type":"string","maxLength":9,"pattern":"(?i)^abc$"}`), "#/properties/a/pattern: sets flags with (?…); write the cases out, as in [Aa]; use the syntax Go and JavaScript share"},
		{"a flag group with a pattern", closed(`"a":{"type":"string","maxLength":9,"pattern":"(?s:a.b)"}`), "sets flags with (?…)"},
		{"a Go named group", closed(`"a":{"type":"string","maxLength":9,"pattern":"(?P<n>a)"}`), "names a group with (?P<name>…); write (?<name>…)"},
		{"an end of text", closed(`"a":{"type":"string","maxLength":9,"pattern":"^a\\z"}`), `uses \z, which JavaScript reads otherwise or not at all`},
		{"a start of text", closed(`"a":{"type":"string","maxLength":9,"pattern":"\\Aa"}`), `uses \A`},
		{"a quoted span", closed(`"a":{"type":"string","maxLength":9,"pattern":"\\Qa.b\\E"}`), `uses \Q`},
		{"a Unicode class", closed(`"a":{"type":"string","maxLength":9,"pattern":"^\\p{L}+$"}`), `uses \p`},
		{"a braced hex escape", closed(`"a":{"type":"string","maxLength":9,"pattern":"\\x{41}"}`), `uses \x{…}; write \xHH or the character itself`},
		{"a POSIX class", closed(`"a":{"type":"string","maxLength":9,"pattern":"^[[:alpha:]]+$"}`), "uses a POSIX class such as [:alpha:]"},
		{"a pattern on a number", closed(`"a":{"type":"number","pattern":"1"}`), "#/properties/a/pattern: pattern applies only to strings"},

		// Numbers.
		{"a minimum on a string", closed(`"a":{"type":"string","maxLength":1,"minimum":1}`), "#/properties/a/minimum: minimum applies only to numbers"},
		{"a minimum that is not a number", closed(`"a":{"type":"number","minimum":"1"}`), "#/properties/a/minimum: must be a number"},

		// Values.
		{"65 enum values", closed(`"a":{"enum":[` + numbers(65) + `]}`), "#/properties/a/enum: has 65 values, more than 64"},
		{"an empty enum", closed(`"a":{"enum":[]}`), "#/properties/a/enum: must be a non-empty array"},
		{"an object in an enum", closed(`"a":{"enum":[{"x":1}]}`), "#/properties/a/enum: may hold only strings, numbers, booleans and null"},
		{"an object const", closed(`"a":{"const":[1]}`), "#/properties/a/const: must be a string, number, boolean or null"},

		// Combinators.
		{"nine anyOf options", closed(`"a":{"anyOf":[` + options(9) + `]}`), "#/properties/a/anyOf: has 9 options, more than 8"},
		{"nine oneOf options", closed(`"a":{"oneOf":[` + options(9) + `]}`), "#/properties/a/oneOf: has 9 options, more than 8"},
		{"five allOf options", closed(`"a":{"allOf":[` + options(5) + `]}`), "#/properties/a/allOf: has 5 options, more than 4"},
		{"an empty anyOf", closed(`"a":{"anyOf":[]}`), "#/properties/a/anyOf: must be a non-empty array"},
		{"combinators three deep", closed(`"a":{"anyOf":[{"oneOf":[{"allOf":[` + str + `]}]}]}`), "anyOf, oneOf and allOf nest 3 deep"},
		{"combinators three deep through properties", closed(`"a":{"anyOf":[{"type":"object","additionalProperties":false,"properties":{"b":{"anyOf":[{"type":"object","additionalProperties":false,"properties":{"c":{"oneOf":[` + str + `]}}}]}}}]}`), "nest 3 deep counting through properties"},
		{"combinators three deep through $ref", closed(`"a":{"anyOf":[{"$ref":"#/$defs/x"}]}`, `"$defs":{"x":{"oneOf":[{"$ref":"#/$defs/y"}]},"y":{"allOf":[`+str+`]}}`), "nest 3 deep"},

		// References.
		{"a remote $ref", closed(`"a":{"$ref":"https://example.com/schema.json"}`), `#/properties/a/$ref: a $ref must be "#/$defs/<name>"`},
		{"a pointer $ref", closed(`"a":{"$ref":"#/properties/b"}`), `a $ref must be "#/$defs/<name>"`},
		{"a deep $defs pointer", closed(`"a":{"$ref":"#/$defs/x/properties/y"}`, `"$defs":{"x":`+str+`}`), `a $ref must be "#/$defs/<name>"`},
		{"an undefined $ref", closed(`"a":{"$ref":"#/$defs/missing"}`), `#/properties/a/$ref: names the definition "missing", which is not defined`},
		{"a recursive $ref", closed(`"a":{"$ref":"#/$defs/node"}`, `"$defs":{"node":{"type":"object","additionalProperties":false,"properties":{"child":{"$ref":"#/$defs/node"}}}}`), "the $ref leads back to itself (node → node)"},
		{"a cycle through two definitions", closed(`"a":{"$ref":"#/$defs/x"}`, `"$defs":{"x":{"type":"array","maxItems":2,"items":{"$ref":"#/$defs/y"}},"y":{"type":"array","maxItems":2,"items":{"$ref":"#/$defs/x"}}}`), "leads back to itself (x → y → x)"},
		{"a cycle no property reaches", closed(``, `"$defs":{"x":{"type":"array","maxItems":2,"items":{"$ref":"#/$defs/x"}}}`), "leads back to itself"},
		{"a $ref with a sibling assertion", closed(`"a":{"$ref":"#/$defs/markdown","maxLength":3}`), `#/properties/a/maxLength: a schema with $ref may carry only title, description`},
		{"a redefined reserved definition", closed(``, `"$defs":{"citation":`+str+`}`), `#/$defs/citation: citation is provided to every data schema and cannot be redefined`},
		{"a redefined dependency", closed(``, `"$defs":{"hunkId":`+str+`}`), "#/$defs/hunkId: hunkId is provided"},
		{"a bad definition name", closed(``, `"$defs":{"my-def":`+str+`}`), "#/$defs/my-def: a definition name must start with a letter"},
		{"$defs that are not an object", closed(``, `"$defs":[]`), "#/$defs: is not an object of definitions"},

		// Refused keywords, each with what to do instead.
		{"format", closed(`"a":{"type":"string","maxLength":9,"format":"date"}`), `#/properties/a/format: the keyword "format" is not allowed in a tile data schema; describe the format in description`},
		{"uniqueItems", closed(`"a":{"type":"array","maxItems":3,"items":` + str + `,"uniqueItems":true}`), `the keyword "uniqueItems" is not allowed`},
		{"if", closed(`"a":{"type":"string","maxLength":1,"if":{"type":"string"}}`), `the keyword "if" is not allowed in a tile data schema; use oneOf`},
		{"not", closed(`"a":{"type":"string","maxLength":1,"not":{"const":"x"}}`), `the keyword "not" is not allowed`},
		{"patternProperties", closed(``, `"patternProperties":{}`), `the keyword "patternProperties" is not allowed`},
		{"propertyNames", closed(``, `"propertyNames":{}`), `the keyword "propertyNames" is not allowed`},
		{"prefixItems", closed(`"a":{"type":"array","maxItems":1,"items":` + str + `,"prefixItems":[]}`), `the keyword "prefixItems" is not allowed`},
		{"contains", closed(`"a":{"type":"array","maxItems":1,"items":` + str + `,"contains":{}}`), `the keyword "contains" is not allowed`},
		{"dependentRequired", closed(``, `"dependentRequired":{}`), `the keyword "dependentRequired" is not allowed`},
		{"dependentSchemas", closed(``, `"dependentSchemas":{}`), `the keyword "dependentSchemas" is not allowed`},
		{"unevaluatedProperties", closed(``, `"unevaluatedProperties":false`), `the keyword "unevaluatedProperties" is not allowed`},
		{"$id", closed(``, `"$id":"https://example.com/s"`), `the keyword "$id" is not allowed`},
		{"$anchor", closed(`"a":{"type":"string","maxLength":1,"$anchor":"x"}`), `the keyword "$anchor" is not allowed`},
		{"$dynamicRef", closed(`"a":{"$dynamicRef":"#x"}`), `the keyword "$dynamicRef" is not allowed`},
		{"contentMediaType", closed(`"a":{"type":"string","maxLength":1,"contentMediaType":"text/html"}`), `the keyword "contentMediaType" is not allowed`},
		{"nullable", closed(`"a":{"type":"string","maxLength":1,"nullable":true}`), `use a type array such as ["string", "null"]`},
		{"an unknown keyword", closed(`"a":{"type":"string","maxLength":1,"widget":"x"}`), `the keyword "widget" is not allowed in a tile data schema; the dialect allows only $comment, $defs`},

		// Annotations.
		{"a long description", closed(`"a":{"type":"string","maxLength":1,"description":"` + strings.Repeat("d", 1001) + `"}`), "#/properties/a/description: is longer than 1000 characters"},
		{"examples that are not an array", closed(`"a":{"type":"string","maxLength":1,"examples":"x"}`), "#/properties/a/examples: must be an array"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := tiles.CheckDataSchema(json.RawMessage(tc.schema))
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, strings.ReplaceAll(tc.want, "# : ", "#: ")) {
				t.Errorf("problems:\n%s\nwant one saying %q", joined, tc.want)
			}
		})
	}
}

// A schema with many problems reports the first twenty and counts the rest.
func TestCheckDataSchemaBoundsItsReport(t *testing.T) {
	var properties []string
	for i := range 30 {
		properties = append(properties, fmt.Sprintf(`"p%d":{"type":"string"}`, i))
	}
	problems := tiles.CheckDataSchema(json.RawMessage(closed(strings.Join(properties, ","))))
	if len(problems) != 21 || problems[20] != "and 10 more problems" {
		t.Errorf("problems = %d, last %q", len(problems), problems[len(problems)-1])
	}
}

func manyProperties(n int) string {
	var properties []string
	for i := range n {
		properties = append(properties, fmt.Sprintf(`"p%d":{"type":"boolean"}`, i))
	}
	return strings.Join(properties, ",")
}

// nested returns a schema whose objects nest levels+1 deep.
func nested(levels int) string {
	schema := `{"type":"boolean"}`
	for range levels - 1 {
		schema = `{"type":"object","additionalProperties":false,"properties":{"x":` + schema + `}}`
	}
	return closed(`"x":` + schema)
}

func numbers(n int) string {
	var values []string
	for i := range n {
		values = append(values, fmt.Sprint(i))
	}
	return strings.Join(values, ",")
}

func options(n int) string {
	var values []string
	for i := range n {
		values = append(values, fmt.Sprintf(`{"const":%d}`, i))
	}
	return strings.Join(values, ",")
}

// A standalone data schema carries the reserved definitions it uses, and
// those they use, so a JSON Schema implementation resolves it on its own.
func TestStandaloneDataSchema(t *testing.T) {
	raw := `{"type":"object","additionalProperties":false,"required":["cites"],"properties":{
		"cites":{"type":"array","maxItems":3,"items":{"$ref":"#/$defs/citation"}},
		"note":{"$ref":"#/$defs/own"}},
		"$defs":{"own":{"type":"string","maxLength":10}}}`
	out, err := tiles.StandaloneDataSchema(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"own", "citation", "anchor", "itemRef", "hunkId"} {
		if schema.Defs[name] == nil {
			t.Errorf("the standalone schema lacks $defs/%s", name)
		}
	}
	if schema.Defs["markdown"] != nil {
		t.Error("the standalone schema carries a definition it does not use")
	}
	var resolved jsonschema.Schema
	if err := json.Unmarshal(out, &resolved); err != nil {
		t.Fatal(err)
	}
	r, err := resolved.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(map[string]any{"cites": []any{map[string]any{"type": "hunk", "id": "h_000000000001"}}}); err != nil {
		t.Error(err)
	}
	if _, err := tiles.StandaloneDataSchema(json.RawMessage(`{"type":"array"}`)); err == nil {
		t.Error("a schema outside the dialect has a standalone form")
	}
}
