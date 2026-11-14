package tiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Sample is named example data for a custom tile type.
type Sample struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// The names of the samples EdgeSamples generates.
const (
	SampleMinimal = "minimal"
	SampleMaximal = "maximal"
)

// EdgeSamples generates two samples of data from a data schema, to check a
// component against the edges of what its schema allows:
//
//   - minimal: required properties only, null where a value may be null,
//     the fewest items, the shortest strings, the first enum value and the
//     smallest numbers;
//   - maximal: every property, arrays at maxItems (at most 50), strings at
//     maxLength (at most 2000) of long words, a 120-character unbroken
//     token, right-to-left text, combining characters and CJK, the longest
//     enum value and the largest numbers. When that is larger than
//     MaxDataBytes or has too many citations, it is generated again with
//     fewer items and shorter strings.
//
// Citations are filled from citations in turn (the review's own, so they
// resolve), or with a hunk citation that resolves in no review. Generation
// is deterministic. A sample that does not pass its schema is dropped, with
// a warning saying why. The whole call, generation and validation, has one
// budget of sampleBudget steps; when it runs out, the samples not yet made
// are dropped, with a warning. EdgeSamples fails only for a schema that is
// not in the dialect (CheckDataSchema).
func EdgeSamples(dataSchema json.RawMessage, citations []json.RawMessage) ([]Sample, []string, error) {
	compiled, problems := compileDataSchema(dataSchema)
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("tiles: the data schema is not valid: %s", strings.Join(problems, "; "))
	}
	shared, err := reserved()
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	var pool []any
	work := &sampleWork{}
	for i, raw := range citations {
		if work.spend(len(raw)/16+1) != nil {
			return nil, append(warnings, outOfWork), nil
		}
		value, err := decodeData(raw)
		if err == nil {
			err = work.validate(shared.nodes["citation"], value)
		}
		if err == errSampleBudget {
			return nil, append(warnings, outOfWork), nil
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("citation %d is not a citation and was not used", i+1))
			continue
		}
		pool = append(pool, value)
	}
	var samples []Sample
	for _, maximal := range []bool{false, true} {
		name := SampleMinimal
		limits := []sampleLimits{{}}
		if maximal {
			name = SampleMaximal
			limits = maximalLimits
		}
		var last string
		for _, limit := range limits {
			g := &generator{maximal: maximal, limits: limit, shared: shared, pool: pool, work: work}
			value, err := g.value(compiled.root)
			if err == errSampleBudget {
				return samples, append(warnings, outOfWork), nil
			}
			if err != nil {
				last = err.Error()
				continue
			}
			data, err := json.Marshal(value)
			if err != nil {
				last = err.Error()
				continue
			}
			problems, err := work.checkData(compiled, data)
			if err != nil {
				return samples, append(warnings, outOfWork), nil
			}
			if len(problems) > 0 {
				last = problems[0]
				continue
			}
			samples = append(samples, Sample{Name: name, Data: data})
			last = ""
			break
		}
		if last != "" {
			warnings = append(warnings, fmt.Sprintf("the %s sample was dropped: %s", name, last))
		}
	}
	return samples, warnings, nil
}

// sampleLimits bound a maximal sample's arrays and strings.
type sampleLimits struct {
	items, length int
}

var maximalLimits = []sampleLimits{{50, 2000}, {30, 1000}, {20, 500}, {12, 300}, {8, 200}, {5, 120}, {3, 60}, {1, 20}}

var errTooLarge = errors.New("the schema describes more data than a sample may hold")

// generator generates one sample. It counts what it generates, about as
// encoded, and stops once that passes MaxDataBytes: a schema can describe
// far more than data may hold.
type generator struct {
	maximal bool
	limits  sampleLimits
	shared  *reservedSchemas
	pool    []any
	next    int
	size    int
	work    *sampleWork
}

// sampleBudget bounds the steps of one EdgeSamples call: generating a value
// is a step, a string costs its length over 16, and validation counts as it
// does for a tile.
const sampleBudget = 1 << 21

const outOfWork = "generating samples took too much work; the samples not yet made were dropped"

var errSampleBudget = errors.New("the sample budget ran out")

// sampleWork is the work an EdgeSamples call has done.
type sampleWork struct {
	steps int
}

func (w *sampleWork) spend(steps int) error {
	if w.steps += steps; w.steps > sampleBudget {
		return errSampleBudget
	}
	return nil
}

// validation returns a validation with a tile's budget, or what is left of
// the call's when that is less.
func (w *sampleWork) validation() (*validation, error) {
	remaining := sampleBudget - w.steps
	if remaining <= 0 {
		return nil, errSampleBudget
	}
	return &validation{limit: min(validationBudget, remaining)}, nil
}

// validate validates a value and returns nil, a failure, or errSampleBudget
// when the call's budget ran out first.
func (w *sampleWork) validate(n *schemaNode, value any) error {
	v, err := w.validation()
	if err != nil {
		return err
	}
	err = v.validate(n, value, nil)
	w.steps += v.steps
	if err == errBudget && v.limit < validationBudget {
		return errSampleBudget
	}
	return err
}

// checkData checks a sample as a tile's data is checked.
func (w *sampleWork) checkData(compiled *compiledSchema, data []byte) ([]string, error) {
	v, err := w.validation()
	if err != nil {
		return nil, err
	}
	_, problems := checkDataWith(v, compiled, data)
	w.steps += v.steps
	if v.steps > v.limit && v.limit < validationBudget {
		return nil, errSampleBudget
	}
	return problems, nil
}

func (g *generator) grow(n int) error {
	if g.size += n; g.size > MaxDataBytes {
		return errTooLarge
	}
	return nil
}

func (g *generator) value(n *schemaNode) (any, error) {
	if err := g.work.spend(1); err != nil {
		return nil, err
	}
	if err := g.grow(2); err != nil {
		return nil, err
	}
	switch n {
	case g.shared.nodes["citation"], g.shared.nodes["anchor"], g.shared.nodes["itemRef"]:
		return g.cite(n)
	case g.shared.nodes["hunkId"]:
		return g.hunkID(), nil
	}
	if n.ref != nil {
		return g.value(n.ref)
	}
	if n.hasConst {
		return n.constant, nil
	}
	if len(n.enum) > 0 {
		if !g.maximal {
			return n.enum[0], nil
		}
		longest, size := n.enum[0], -1
		for _, value := range n.enum {
			encoded, _ := json.Marshal(value)
			if len(encoded) > size {
				longest, size = value, len(encoded)
			}
		}
		return longest, nil
	}
	if options := slices.Concat(n.anyOf, n.oneOf); len(options) > 0 {
		return g.option(n, options)
	}
	if n.types == 0 && len(n.allOf) > 0 {
		return g.value(n.allOf[0])
	}
	return g.typed(n, g.pickType(n.types))
}

// option generates a value from each option in turn and returns the first
// that passes the whole node, or, for a maximal sample, the largest that
// does.
func (g *generator) option(n *schemaNode, options []*schemaNode) (any, error) {
	var best any
	size := -1
	for _, option := range options {
		value, err := g.value(option)
		if err != nil {
			return nil, err
		}
		switch err := g.work.validate(n, value); err {
		case nil:
		case errSampleBudget:
			return nil, err
		default:
			continue
		}
		if !g.maximal {
			return value, nil
		}
		if encoded, _ := json.Marshal(value); len(encoded) > size {
			best, size = value, len(encoded)
		}
	}
	if size < 0 {
		return g.value(options[0])
	}
	return best, nil
}

func (g *generator) pickType(types uint8) uint8 {
	order := []uint8{typeNull, typeBoolean, typeInteger, typeNumber, typeString, typeArray, typeObject}
	if g.maximal {
		slices.Reverse(order)
	}
	for _, t := range order {
		if types&t != 0 {
			return t
		}
	}
	return typeNull
}

func (g *generator) typed(n *schemaNode, t uint8) (any, error) {
	switch t {
	case typeBoolean:
		return g.maximal, nil
	case typeInteger:
		return g.number(n, true), nil
	case typeNumber:
		return g.number(n, false), nil
	case typeString:
		text := g.text(n)
		if err := g.work.spend(len(text)/16 + 1); err != nil {
			return nil, err
		}
		return text, g.grow(len(text))
	case typeArray:
		count := max(n.minItems, 0)
		if g.maximal {
			count = g.limits.items
			if n.maxItems >= 0 {
				count = min(count, n.maxItems)
			}
			count = max(count, n.minItems)
		}
		items := make([]any, 0, count)
		for range count {
			if n.items == nil {
				items = append(items, nil)
				continue
			}
			item, err := g.value(n.items)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case typeObject:
		object := map[string]any{}
		names := make([]string, 0, len(n.properties))
		for name := range n.properties {
			if g.maximal || slices.Contains(n.required, name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if err := g.grow(len(name) + 3); err != nil {
				return nil, err
			}
			value, err := g.value(n.properties[name])
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		return object, nil
	}
	return nil, nil
}

func (g *generator) number(n *schemaNode, integer bool) float64 {
	lo, hi := math.Inf(-1), math.Inf(1)
	if n.minimum != nil {
		lo = *n.minimum
	}
	if n.maximum != nil {
		hi = *n.maximum
	}
	step := 1e-3
	if integer {
		step = 1
		lo, hi = math.Ceil(lo), math.Floor(hi)
	}
	if n.exclusiveMin != nil {
		bound := *n.exclusiveMin + step
		if integer {
			bound = math.Floor(*n.exclusiveMin) + 1
		}
		lo = max(lo, bound)
	}
	if n.exclusiveMax != nil {
		bound := *n.exclusiveMax - step
		if integer {
			bound = math.Ceil(*n.exclusiveMax) - 1
		}
		hi = min(hi, bound)
	}
	value := 0.0
	if g.maximal {
		value = 999999
		if !integer {
			value = 99999.75
		}
	}
	if !math.IsInf(hi, 1) && (g.maximal || value > hi) {
		value = hi
	}
	if !math.IsInf(lo, -1) && (!g.maximal || value < lo) {
		value = lo
	}
	return value
}

// The parts of a maximal string, in order: long words, an unbroken token of
// 120 characters, right-to-left text, combining characters and CJK.
var maximalText = "Internationalisation considerations notwithstanding, configuration reconciliation proceeds " +
	strings.Repeat("unbroken", 15) + " " +
	"نص من اليمين إلى اليسار " +
	"café naïve " +
	"漢字とかなのテキスト 한국어 "

func (g *generator) text(n *schemaNode) string {
	if n.pattern != nil {
		if s, ok := patternText(n.pattern.String(), g.maximal); ok {
			return s
		}
	}
	if !g.maximal {
		return strings.Repeat("x", max(n.minLength, 0))
	}
	length := g.limits.length
	if n.maxLength >= 0 {
		length = min(length, n.maxLength)
	}
	length = max(length, n.minLength)
	var b strings.Builder
	for count := 0; count < length; {
		for _, r := range maximalText {
			if count == length {
				break
			}
			b.WriteRune(r)
			count++
		}
	}
	return b.String()
}

// patternText returns a string the pattern matches: the fewest repetitions
// and the first alternative, or, for a maximal sample, a few more
// repetitions.
func patternText(pattern string, maximal bool) (string, bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	ok := writePattern(re, maximal, &b)
	return b.String(), ok && utf8.RuneCountInString(b.String()) <= maxStringLength
}

func writePattern(re *syntax.Regexp, maximal bool, b *strings.Builder) bool {
	if b.Len() > 4*maxStringLength {
		return false
	}
	repeat := func(sub *syntax.Regexp, times int) bool {
		for range times {
			if !writePattern(sub, maximal, b) {
				return false
			}
		}
		return true
	}
	extra := 0
	if maximal {
		extra = 3
	}
	switch re.Op {
	case syntax.OpNoMatch:
		return false
	case syntax.OpLiteral:
		b.WriteString(string(re.Rune))
	case syntax.OpCharClass:
		r, ok := classRune(re.Rune)
		if !ok {
			return false
		}
		b.WriteRune(r)
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		b.WriteByte('a')
	case syntax.OpCapture:
		return writePattern(re.Sub[0], maximal, b)
	case syntax.OpStar:
		return repeat(re.Sub[0], extra)
	case syntax.OpPlus:
		return repeat(re.Sub[0], 1+extra)
	case syntax.OpQuest:
		return repeat(re.Sub[0], min(extra, 1))
	case syntax.OpRepeat:
		times := re.Min
		if maximal {
			times = re.Min + extra
			if re.Max >= 0 {
				times = re.Max
			}
		}
		return repeat(re.Sub[0], times)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !writePattern(sub, maximal, b) {
				return false
			}
		}
	case syntax.OpAlternate:
		return writePattern(re.Sub[0], maximal, b)
	}
	return true
}

// classRune picks a printable rune of a character class, preferring a
// letter or digit.
func classRune(ranges []rune) (rune, bool) {
	for _, want := range []rune{'a', 'x', 'A', '0', '_', '-', ' '} {
		for i := 0; i+1 < len(ranges); i += 2 {
			if ranges[i] <= want && want <= ranges[i+1] {
				return want, true
			}
		}
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		r := max(ranges[i], 0x21)
		if r <= ranges[i+1] && r != 0x7f {
			return r, true
		}
	}
	return 0, false
}

// cite returns the next citation of the pool that passes the node, or a
// hunk citation no review has.
func (g *generator) cite(n *schemaNode) (any, error) {
	for range g.pool {
		value := g.pool[g.next%len(g.pool)]
		g.next++
		switch err := g.work.validate(n, value); err {
		case nil:
			return value, nil
		case errSampleBudget:
			return nil, err
		}
	}
	citation := map[string]any{"type": "hunk", "id": unknownHunk}
	if g.maximal {
		citation["lines"] = []any{1.0, 3.0}
	}
	switch err := g.work.validate(n, citation); err {
	case nil:
		return citation, nil
	case errSampleBudget:
		return nil, err
	}
	return map[string]any{"type": "change", "id": "sec_sample"}, nil
}

// unknownHunk is a hunk ID no review has.
const unknownHunk = "h_000000000000"

func (g *generator) hunkID() string {
	for i := range g.pool {
		if c, ok := g.pool[(g.next+i)%len(g.pool)].(map[string]any); ok && c["type"] == "hunk" {
			g.next += i + 1
			return c["id"].(string)
		}
	}
	return unknownHunk
}
