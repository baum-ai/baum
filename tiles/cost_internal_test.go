package tiles

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// costBound is the most one custom tile's data may take to check against
// the worst schema the dialect allows. The race detector slows the
// validator several times over, so it gets more.
func costBound() time.Duration {
	if raceEnabled {
		return 500 * time.Millisecond
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
