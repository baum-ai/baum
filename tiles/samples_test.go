package tiles_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// sampleSchemas are data schemas of varied shapes, each in the dialect.
var sampleSchemas = map[string]string{
	"routes": string(routesDefinition().DataSchema),
	"scalars": closed(`"title":{"type":"string","minLength":3,"maxLength":40},`+
		`"note":{"type":["string","null"],"maxLength":5000},`+
		`"slug":{"type":"string","maxLength":31,"pattern":"^[a-z][a-z0-9-]{2,30}$"},`+
		`"hunk":{"$ref":"#/$defs/hunkId"},`+
		`"count":{"type":"integer","minimum":2,"maximum":9},`+
		`"open":{"type":"integer","exclusiveMinimum":0,"exclusiveMaximum":10},`+
		`"ratio":{"type":"number","exclusiveMinimum":0.5,"maximum":0.75},`+
		`"negative":{"type":"number","maximum":-3},`+
		`"flag":{"type":"boolean"},"nothing":{"type":"null"},`+
		`"kind":{"enum":["a","much longer value",3,null]},"version":{"const":"v1"},`+
		`"tier":{"$ref":"#/$defs/tier"},"text":{"$ref":"#/$defs/markdown"},`+
		`"both":{"allOf":[{"type":"integer"},{"type":"number","minimum":1}]}`,
		`"required":["title","slug","count","kind","version"]`),
	"options": closed(`"shape":{"oneOf":[`+
		closed(`"kind":{"const":"circle"},"radius":{"type":"number","minimum":0}`, `"required":["kind","radius"]`)+`,`+
		closed(`"kind":{"const":"box"},"sides":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"integer"}}`, `"required":["kind","sides"]`)+`]},`+
		`"maybe":{"anyOf":[{"type":"null"},`+closed(`"cite":{"$ref":"#/$defs/citation"}`, `"required":["cite"]`)+`]}`,
		`"required":["shape","maybe"]`),
	"nested arrays": closed(`"grid":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"array","minItems":2,"maxItems":500,"items":{"type":"array","maxItems":500,"items":{"type":"string","maxLength":20000}}}}`, `"required":["grid"]`),
	"definitions": `{"type":"object","additionalProperties":false,"required":["tables"],"properties":{
		"tables":{"type":"array","maxItems":30,"items":{"$ref":"#/$defs/table"}}},
		"$defs":{
			"table":{"type":"object","additionalProperties":false,"required":["name","columns","cites"],"properties":{
				"name":{"type":"string","maxLength":120},
				"columns":{"type":"array","maxItems":60,"items":{"$ref":"#/$defs/column"}},
				"cites":{"type":"array","minItems":1,"maxItems":8,"items":{"$ref":"#/$defs/citation"}}}},
			"column":{"type":"object","additionalProperties":false,"required":["name","change"],"properties":{
				"name":{"type":"string","maxLength":120},
				"change":{"enum":["added","removed","altered","renamed"]},
				"before":{"type":["string","null"],"maxLength":160},
				"note":{"type":["string","null"],"maxLength":200}}}}}`,
	"items and citations": closed(`"items":{"type":"array","maxItems":500,"items":{"$ref":"#/$defs/itemRef"}},"anchors":{"type":"array","minItems":1,"maxItems":3,"items":{"$ref":"#/$defs/anchor"}}`, `"required":["anchors"]`),
}

func TestEdgeSamples(t *testing.T) {
	pool := []json.RawMessage{
		json.RawMessage(`{"type":"hunk","id":"h_000000000003","lines":[1,2]}`),
		json.RawMessage(`{"type":"risk","id":"rsk_retry_storm"}`),
		json.RawMessage(`{"type":"change","id":"sec_retry_policy"}`),
	}
	for name, schema := range sampleSchemas {
		t.Run(name, func(t *testing.T) {
			if problems := tiles.CheckDataSchema(json.RawMessage(schema)); problems != nil {
				t.Fatalf("the schema is not in the dialect: %v", problems)
			}
			def := routesDefinition()
			def.DataSchema = json.RawMessage(schema)
			sealed := tilestest.SealDefinition(def)
			for _, citations := range [][]json.RawMessage{nil, pool} {
				samples, warnings, err := tiles.EdgeSamples(json.RawMessage(schema), citations)
				if err != nil {
					t.Fatal(err)
				}
				if len(warnings) > 0 {
					t.Errorf("warnings: %v", warnings)
				}
				if len(samples) != 2 || samples[0].Name != tiles.SampleMinimal || samples[1].Name != tiles.SampleMaximal {
					t.Fatalf("samples = %v", samples)
				}
				for _, sample := range samples {
					if problems := validator.CheckData(sealed, sample.Data); problems != nil {
						t.Errorf("%s does not pass its schema: %v\n%s", sample.Name, problems, sample.Data)
					}
				}
				if len(samples[1].Data) <= len(samples[0].Data) {
					t.Errorf("the maximal sample (%d bytes) is not larger than the minimal (%d)", len(samples[1].Data), len(samples[0].Data))
				}
				again, _, _ := tiles.EdgeSamples(json.RawMessage(schema), citations)
				for i := range samples {
					if string(again[i].Data) != string(samples[i].Data) {
						t.Errorf("%s is not deterministic", samples[i].Name)
					}
				}
			}
		})
	}
}

func TestEdgeSamplesContent(t *testing.T) {
	samples, _, err := tiles.EdgeSamples(json.RawMessage(sampleSchemas["scalars"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	var minimal, maximal map[string]any
	if err := json.Unmarshal(samples[0].Data, &minimal); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(samples[1].Data, &maximal); err != nil {
		t.Fatal(err)
	}
	// Minimal: required properties only, the shortest strings, the first
	// enum value and the smallest numbers.
	if len(minimal) != 5 || minimal["title"] != "xxx" || minimal["kind"] != "a" || minimal["count"] != 2.0 || minimal["version"] != "v1" {
		t.Errorf("minimal = %v", minimal)
	}
	// Maximal: every property, the longest enum value, the largest numbers
	// and long text with an unbroken token, right-to-left text, combining
	// characters and CJK.
	if len(maximal) != 15 || maximal["kind"] != "much longer value" || maximal["count"] != 9.0 || maximal["open"] != 9.0 || maximal["negative"] != -3.0 || maximal["flag"] != true {
		t.Errorf("maximal = %v", maximal)
	}
	note, _ := maximal["note"].(string)
	for _, part := range []string{strings.Repeat("unbroken", 15), "اليسار", "é", "漢字"} {
		if !strings.Contains(note, part) {
			t.Errorf("the maximal note lacks %q", part)
		}
	}
	if n := len([]rune(note)); n != 2000 {
		t.Errorf("the maximal note is %d characters, want 2000", n)
	}
	if maximal["hunk"] != "h_000000000000" {
		t.Errorf("a hunk ID without citations = %v", maximal["hunk"])
	}

	// Citations come from the pool in turn.
	samples, _, err = tiles.EdgeSamples(json.RawMessage(sampleSchemas["routes"]), []json.RawMessage{
		json.RawMessage(`{"type":"hunk","id":"h_000000000003"}`),
		json.RawMessage(`{"type":"tile","id":"overview"}`),
		json.RawMessage(`{"not":"a citation"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"id":"h_000000000003","type":"hunk"}`, `{"id":"overview","type":"tile"}`} {
		if !strings.Contains(string(samples[1].Data), want) {
			t.Errorf("the maximal sample does not cite %s", want)
		}
	}
	if strings.Contains(string(samples[1].Data), "h_000000000000") {
		t.Error("the maximal sample has an unknown hunk though the pool has citations")
	}
}

// The maximal sample of a large schema shrinks until it fits.
func TestEdgeSamplesFitTheDataLimit(t *testing.T) {
	samples, warnings, err := tiles.EdgeSamples(json.RawMessage(sampleSchemas["nested arrays"]), nil)
	if err != nil || len(warnings) > 0 {
		t.Fatal(err, warnings)
	}
	if size := len(samples[1].Data); size > tiles.MaxDataBytes || size < tiles.MaxDataBytes/8 {
		t.Errorf("the maximal sample is %d bytes", size)
	}
}

func TestEdgeSamplesWarnAndFail(t *testing.T) {
	// No string matches both the pattern and maxLength: both samples are
	// dropped, with warnings.
	samples, warnings, err := tiles.EdgeSamples(json.RawMessage(closed(`"a":{"type":"string","pattern":"^abc$","maxLength":2}`, `"required":["a"]`)), []json.RawMessage{json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 || len(warnings) != 3 || !strings.Contains(warnings[0], "citation 1 is not a citation") || !strings.Contains(warnings[1], "the minimal sample was dropped: the data does not match its schema: at /a:") {
		t.Errorf("samples %v, warnings %v", samples, warnings)
	}
	if _, _, err := tiles.EdgeSamples(json.RawMessage(`{"type":"object"}`), nil); err == nil {
		t.Error("a schema outside the dialect gave samples")
	}
}
