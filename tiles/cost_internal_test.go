package tiles

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// costBound is the most one custom tile's data may take to check against
// the worst schema the dialect allows. The race detector slows the
// validator about twenty times over, most of it in regexp's backtracker, so
// it gets more. Builds without it (Windows in CI) keep the tight bound.
func costBound() time.Duration {
	if raceEnabled {
		return time.Second
	}
	return 50 * time.Millisecond
}

// hostileCase is a schema the dialect allows, chosen to make validation as
// costly as it can, and the costliest data it can be given.
type hostileCase struct {
	name   string
	schema string
	data   string
}

func hostileCases() []hostileCase {
	closedObject := func(properties string, required ...string) string {
		quoted := make([]string, len(required))
		for i, name := range required {
			quoted[i] = `"` + name + `"`
		}
		return `{"type":"object","additionalProperties":false,"required":[` + strings.Join(quoted, ",") + `],"properties":{` + properties + `}}`
	}
	array := func(items string, maxItems int) string {
		return fmt.Sprintf(`{"type":"array","maxItems":%d,"items":%s}`, maxItems, items)
	}
	fill := func(item string, sep string, limit int) string {
		var b strings.Builder
		for n := 0; n < limit && b.Len()+len(item)+len(sep)+64 < MaxDataBytes; n++ {
			if b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteString(item)
		}
		return b.String()
	}
	var cases []hostileCase

	// Patterns: every string tried against 8 x 8 options of large patterns
	// that a long string almost matches.
	var outer []string
	for i := range 8 {
		var inner []string
		for j := range 8 {
			inner = append(inner, fmt.Sprintf(`{"type":"string","maxLength":20000,"pattern":"^(a|aa|aaa|b)*[a-z]{0,30}(x|y){1,40}%d%d$"}`, i, j))
		}
		outer = append(outer, `{"oneOf":[`+strings.Join(inner, ",")+`]}`)
	}
	longString := `"` + strings.Repeat("a", 20000) + `"`
	cases = append(cases, hostileCase{
		name:   "patterns in 64 options",
		schema: closedObject(`"s":`+array(`{"anyOf":[`+strings.Join(outer, ",")+`]}`, 500), "s"),
		data:   `{"s":[` + longString + "," + longString + "," + longString + `]}`,
	})
	cases = append(cases, hostileCase{
		name:   "short strings against patterns in 64 options",
		schema: closedObject(`"s":`+array(`{"anyOf":[`+strings.Join(outer, ",")+`]}`, 500), "s"),
		// Each string matches only the last option, so every item tries all
		// 64.
		data: `{"s":[` + fill(`"`+strings.Repeat("a", 100)+`xxx77"`, ",", 500) + `]}`,
	})

	// Catastrophic regexes for a backtracking engine; RE2 is linear.
	cases = append(cases, hostileCase{
		name:   "catastrophic regexes",
		schema: closedObject(`"s":`+array(`{"allOf":[{"type":"string","maxLength":20000,"pattern":"^(a+)+$"},{"type":"string","maxLength":20000,"pattern":"^(a|aa)*b"},{"type":"string","maxLength":20000,"pattern":"(.*a){20}x"},{"type":"string","maxLength":20000,"pattern":"^(\\w+\\s?)*$"}]}`, 500), "s"),
		data:   `{"s":[` + longString + "," + longString + "," + longString + `]}`,
	})

	// Combinators: 8 x 8 options of objects, all but the last failing late.
	var objectOptions []string
	for i := range 8 {
		var inner []string
		for j := range 8 {
			inner = append(inner, closedObject(`"a":{"type":"integer"},"b":{"type":"integer"},"c":{"type":"integer"},"k":{"const":`+fmt.Sprint(i*8+j)+`}`, "a", "b", "c", "k"))
		}
		objectOptions = append(objectOptions, `{"oneOf":[`+strings.Join(inner, ",")+`]}`)
	}
	cases = append(cases, hostileCase{
		name:   "objects in 64 options",
		schema: closedObject(`"o":`+array(`{"anyOf":[`+strings.Join(objectOptions, ",")+`]}`, 500), "o"),
		data:   `{"o":[` + strings.Repeat(`{"a":1,"b":2,"c":3,"k":63},`, 499) + `{"a":1,"b":2,"c":3,"k":63}]}`,
	})

	// Depth: arrays nested twelve deep, full of numbers.
	deep := `{"type":"integer"}`
	for range 10 {
		deep = array(deep, 500)
	}
	cases = append(cases, hostileCase{
		name:   "arrays nested twelve deep",
		schema: closedObject(`"d":`+deep, "d"),
		data:   `{"d":[[[[[[[[[[` + fill("0", ",", 500) + `]]]]]]]]]]}`,
	})

	// Citations: 500 items of 8 citations each, every one tried against
	// every anchor kind, and objects that look like citations but are not.
	cases = append(cases, hostileCase{
		name:   "citations",
		schema: closedObject(`"c":`+array(array(`{"$ref":"#/$defs/citation"}`, 8), 500), "c"),
		data:   `{"c":[` + fill(`[`+fill(`{"type":"attachment","url":"https://example.com/a"}`, ",", 7)+`,{"type":"tile","id":"t"}]`, ",", 500) + `]}`,
	})
	cases = append(cases, hostileCase{
		name:   "objects that look like citations",
		schema: closedObject(`"c":`+array(closedObject(`"x":`+array(closedObject(`"type":{"type":"string","maxLength":20},"id":{"type":"string","maxLength":20},"z":{"type":"integer"}`, "type"), 500)), 500), "c"),
		data:   `{"c":[` + fill(`{"x":[`+fill(`{"type":"hunk","id":"h_1","z":1}`, ",", 500)+`]}`, ",", 500) + `]}`,
	})

	// Enums: 64 long values, compared for every item.
	var values []string
	for i := range 64 {
		values = append(values, fmt.Sprintf(`"%s%02d"`, strings.Repeat("v", 400), i))
	}
	cases = append(cases, hostileCase{
		name:   "64 long enum values",
		schema: closedObject(`"e":`+array(`{"enum":[`+strings.Join(values, ",")+`]}`, 500), "e"),
		data:   `{"e":[` + strings.Join(strings.Split(strings.Repeat(values[63]+"\x00", 150), "\x00")[:150], ",") + `]}`,
	})

	// Wide objects: 500 items with every property of a wide object.
	var wide []string
	for i := range 40 {
		wide = append(wide, fmt.Sprintf(`"p%02d":{"type":["string","null"],"maxLength":20}`, i))
	}
	var wideItem []string
	for i := range 40 {
		wideItem = append(wideItem, fmt.Sprintf(`"p%02d":null`, i))
	}
	cases = append(cases, hostileCase{
		name:   "wide objects",
		schema: closedObject(`"w":`+array(closedObject(strings.Join(wide, ",")), 500), "w"),
		data:   `{"w":[` + fill("{"+strings.Join(wideItem, ",")+"}", ",", 500) + `]}`,
	})
	return cases
}

// The worst schemas the dialect allows, on the worst data that fits the
// size limit, validate within costBound, or stop at the budget and report
// that the data takes too much work.
func TestDataValidationCost(t *testing.T) {
	for _, tc := range hostileCases() {
		t.Run(tc.name, func(t *testing.T) {
			if problems := CheckDataSchema(json.RawMessage(tc.schema)); problems != nil {
				t.Fatalf("the dialect refuses the schema: %v", problems)
			}
			if size := encodedSize([]byte(tc.data), MaxDataBytes); size > MaxDataBytes {
				t.Fatalf("the data is %d bytes, more than allowed", size)
			}
			compiled, _ := compileDataSchema(json.RawMessage(tc.schema))
			// The fastest of a few runs measures the work, not what else the
			// machine was doing.
			fastest := time.Duration(math.MaxInt64)
			var problems []string
			var steps int
			for range 5 {
				v := &validation{}
				start := time.Now()
				_, problems = checkDataWith(v, compiled, json.RawMessage(tc.data))
				fastest = min(fastest, time.Since(start))
				steps = v.steps
			}
			t.Logf("%v for %d steps; %s", fastest, steps, truncatedProblems(problems))
			if fastest > costBound() {
				t.Errorf("took %v, more than %v", fastest, costBound())
			}
		})
	}
}

func truncatedProblems(problems []string) string {
	text := strings.Join(problems, "; ")
	if len(text) > 160 {
		return text[:160] + "…"
	}
	return text
}

// fastest returns the fastest of a few runs of f.
func fastest(f func()) time.Duration {
	best := time.Duration(math.MaxInt64)
	for range 3 {
		start := time.Now()
		f()
		best = min(best, time.Since(start))
	}
	return best
}

// A definition just under the size limit is refused for what it holds before
// it is digested: a data schema of many tiny members, or a long fill.
func TestDefinitionCheckCost(t *testing.T) {
	head := `{"name":"x","title":"t","use_when":"u","fill":"f","kit":1,"sizes":["full"],"source":{"format":"html","html":""},`
	wide := func(member string) []byte {
		var b strings.Builder
		b.WriteString(head + member)
		for i := 0; b.Len() < MaxDefinitionBytes-200; i++ {
			fmt.Fprintf(&b, `,"%x":0`, i)
		}
		b.WriteString(`},"digest":"sha256:` + strings.Repeat("0", 64) + `"}`)
		return []byte(b.String())
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"a data schema of many members", wide(`"data_schema":{"type":"object"`), "the data schema is larger than 32 KiB"},
		{"an extra member of many members", wide(`"data_schema":{"type":"object"},"extra":{"a":0`), "the definition is larger than 128 KiB without its HTML and data schema"},
	} {
		if len(tc.raw) > MaxDefinitionBytes {
			t.Fatalf("%s: %d bytes", tc.name, len(tc.raw))
		}
		var problems []string
		took := fastest(func() { problems = NewValidator().checkDefinition(tc.raw).problems })
		t.Logf("%s: %v", tc.name, took)
		if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
			t.Errorf("%s: problems %v", tc.name, problems)
		}
		if took > costBound() {
			t.Errorf("%s: took %v, more than %v", tc.name, took, costBound())
		}
	}

	// The largest definition that passes: a component of 262144 escaped
	// characters.
	def := map[string]any{"name": "x", "title": "t", "use_when": "u", "fill": strings.Repeat("f", 8000), "kit": 1, "sizes": []any{"full"},
		"source":      map[string]any{"format": "html", "html": strings.Repeat("<\x01", 262144/2)},
		"data_schema": map[string]any{"type": "object", "additionalProperties": false}}
	encoded, _ := json.Marshal(def)
	digest, err := DefinitionDigest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	def["digest"] = digest
	encoded, _ = json.Marshal(def)
	var problems []string
	took := fastest(func() { problems = NewValidator().checkDefinition(encoded).problems })
	t.Logf("the largest valid definition, %d bytes: %v", len(encoded), took)
	if problems != nil {
		t.Errorf("problems %v", problems)
	}
	if took > 2*costBound() {
		t.Errorf("took %v, more than %v", took, 2*costBound())
	}
}

// heavyDocument returns the base tile document with n custom tiles of the
// costliest data, each a distinct definition so nothing is shared.
func heavyDocument(t *testing.T, n int) *Document {
	t.Helper()
	raw, err := os.ReadFile("testdata/base.tiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var heavy hostileCase
	for _, tc := range hostileCases() {
		if tc.name == "objects in 64 options" {
			heavy = tc
		}
	}
	for i := range n {
		def := TypeDefinition{Name: fmt.Sprintf("heavy_%d", i), Title: "Heavy", UseWhen: "Never.", Fill: "Nothing.",
			DataSchema: json.RawMessage(heavy.schema), Kit: 1, Sizes: []Size{SizeThird}, Source: TypeSource{Format: "html", HTML: "<p></p>"}}
		encoded, _ := json.Marshal(def)
		def.Digest, _ = DefinitionDigest(encoded)
		encoded, _ = json.Marshal(def)
		id := fmt.Sprintf("heavy_%d", i)
		doc.Tiles = append(doc.Tiles, Tile{ID: id, Type: TypeCustom, Title: "Heavy", Data: json.RawMessage(heavy.data), State: StateReady, Definition: encoded})
	}
	return &doc
}

// A document's custom tiles share one budget, and their verdicts are cached,
// so a document of many of the costliest tiles takes bounded time to check,
// and nothing to check again.
func TestDocumentCheckCost(t *testing.T) {
	doc := heavyDocument(t, maxCustomTiles+2)
	v := NewValidator()
	// Compile the schemas first: the built-in tiles' schemas once per
	// validator, the definitions' in their own cache.
	if _, err := v.data(); err != nil {
		t.Fatal(err)
	}
	for i := range doc.Tiles {
		if doc.Tiles[i].Type == TypeCustom {
			v.definition(doc.Tiles[i].Definition)
		}
	}
	start := time.Now()
	if err := v.CheckTiles(doc); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	states := map[string]int{}
	for _, tile := range doc.Tiles {
		if tile.Type == TypeCustom {
			states[strings.Join(tile.Problems, "")]++
		}
	}
	t.Logf("first check %v: %v", first, states)
	if states[documentTooCostly] == 0 || states[fmt.Sprintf("the review has more than %d custom tiles; this one is past the limit", maxCustomTiles)] != 2 {
		t.Errorf("states %v", states)
	}
	if first > 5*costBound() {
		t.Errorf("the first check took %v, more than %v", first, 5*costBound())
	}

	// A document whose verdicts are cached costs nothing to check again.
	doc = heavyDocument(t, 8)
	if err := v.CheckTiles(doc); err != nil {
		t.Fatal(err)
	}
	refs := newTileRefs(doc)
	for i := range doc.Tiles {
		if doc.Tiles[i].Type == TypeCustom {
			v.tileProblems(nil, refs, &doc.Tiles[i])
		}
	}
	if refs.spent != 0 {
		t.Errorf("checking cached tiles again spent %d steps", refs.spent)
	}
	again := fastest(func() {
		if err := v.CheckTiles(heavyDocument(t, 8)); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("checking 8 cached tiles again (with decoding the document): %v", again)
}

// Tiles past the document's byte limits are unavailable; those before are
// checked.
func TestDocumentByteLimits(t *testing.T) {
	doc := heavyDocument(t, 0)
	data := `{"s":"` + strings.Repeat("d", 60<<10) + `"}`
	def := TypeDefinition{Name: "big", Title: "Big", UseWhen: "Never.", Fill: "Nothing.",
		DataSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"s":{"type":"string","maxLength":20000}}}`),
		Kit:        1, Sizes: []Size{SizeThird}, Source: TypeSource{Format: "html", HTML: "<p></p>"}}
	encoded, _ := json.Marshal(def)
	def.Digest, _ = DefinitionDigest(encoded)
	encoded, _ = json.Marshal(def)
	for i := range 20 {
		id := fmt.Sprintf("big_%d", i)
		doc.Tiles = append(doc.Tiles, Tile{ID: id, Type: TypeCustom, Title: "Big", Data: json.RawMessage(data), State: StateReady, Definition: encoded})
	}
	if err := NewValidator().CheckTiles(doc); err != nil {
		t.Fatal(err)
	}
	past := 0
	for _, tile := range doc.Tiles {
		if tile.Type != TypeCustom {
			continue
		}
		problems := strings.Join(tile.Problems, "")
		switch {
		case strings.Contains(problems, "data is larger than 1 MiB together"):
			past++
		case !strings.Contains(problems, "is 61440 characters long"):
			t.Errorf("tile %s: %v", tile.ID, tile.Problems)
		}
	}
	// 17 tiles of 61450 bytes fit 1 MiB.
	if past != 3 {
		t.Errorf("%d tiles past the data limit, want 3", past)
	}
}

// EdgeSamples has one budget for the whole call.
func TestEdgeSamplesCost(t *testing.T) {
	var outer []string
	for i := range 8 {
		var inner []string
		for j := range 8 {
			inner = append(inner, fmt.Sprintf(`{"type":"string","maxLength":20000,"pattern":"(?:a*[\\x01]?){40}%d%d"}`, i, j))
		}
		outer = append(outer, `{"oneOf":[`+strings.Join(inner, ",")+`]}`)
	}
	schemas := map[string]string{
		"unmatchable patterns in 64 options": `{"type":"object","additionalProperties":false,"properties":{"s":{"type":"array","maxItems":500,"items":{"anyOf":[` + strings.Join(outer, ",") + `]}}}}`,
	}
	for _, tc := range hostileCases() {
		schemas[tc.name] = tc.schema
	}
	for name, schema := range schemas {
		if problems := CheckDataSchema(json.RawMessage(schema)); problems != nil {
			t.Fatalf("%s: the dialect refuses the schema: %v", name, problems)
		}
		var warnings []string
		start := time.Now()
		_, warnings, _ = EdgeSamples(json.RawMessage(schema), nil)
		took := time.Since(start)
		t.Logf("%s: %v, warnings %.120v", name, took, warnings)
		if took > 10*costBound() {
			t.Errorf("%s: took %v, more than %v", name, took, 10*costBound())
		}
	}
}
