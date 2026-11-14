package tiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A schemaNode is a compiled JSON Schema object of the subset a tile data
// schema may use (CheckDataSchema), plus the few keywords the trusted
// schemas this package compiles itself use. The package validates custom
// tile data with it rather than with the general JSON Schema library: every
// check counts its work against a budget, so the cost of validating an
// untrusted schema against untrusted data has a hard bound, and a failure
// names the data's JSON path without quoting the data.
type schemaNode struct {
	// at is the node's JSON pointer in its schema, for messages.
	at string
	// types is a set of jsonType bits; zero allows every type.
	types      uint8
	enum       []any
	hasConst   bool
	constant   any
	properties map[string]*schemaNode
	// propertyNames are the properties' names, sorted.
	propertyNames []string
	required      []string
	closed        bool
	items         *schemaNode
	minItems      int
	maxItems      int
	unique        bool
	minLength     int
	maxLength     int
	pattern       *regexp.Regexp
	// patternCost is the size of the pattern's compiled program.
	patternCost  int
	minimum      *float64
	maximum      *float64
	exclusiveMin *float64
	exclusiveMax *float64
	allOf        []*schemaNode
	anyOf        []*schemaNode
	oneOf        []*schemaNode
	ref          *schemaNode
	// refName is the $defs name a $ref names.
	refName string
}

// The JSON types, as bits of schemaNode.types.
const (
	typeNull uint8 = 1 << iota
	typeBoolean
	typeObject
	typeArray
	typeNumber
	typeInteger
	typeString
)

var typeBits = map[string]uint8{
	"null": typeNull, "boolean": typeBoolean, "object": typeObject, "array": typeArray,
	"number": typeNumber, "integer": typeInteger, "string": typeString,
}

var typeOrder = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

func typeNames(bits uint8) string {
	var names []string
	for _, name := range typeOrder {
		if bits&typeBits[name] != 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, " or ")
}

// maxPatternProgram bounds the compiled size of a pattern. Go's regexp runs
// in time linear in the text times this size.
const maxPatternProgram = 400

// compilePattern compiles a pattern as Go's regexp does (RE2 syntax,
// unanchored) and returns its program size.
func compilePattern(pattern string) (*regexp.Regexp, int, error) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, 0, err
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, 0, err
	}
	if len(program.Inst) > maxPatternProgram {
		return nil, len(program.Inst), fmt.Errorf("it compiles to %d instructions, more than %d", len(program.Inst), maxPatternProgram)
	}
	compiled, err := regexp.Compile(pattern)
	return compiled, len(program.Inst), err
}

// schemaCompiler compiles a decoded schema (json.Number numbers) whose
// $refs name "#/$defs/<name>" in defs or, failing that, in shared.
type schemaCompiler struct {
	defs     map[string]any
	shared   map[string]*schemaNode
	compiled map[string]*schemaNode
	pending  map[string]bool
}

func (c *schemaCompiler) def(name string) (*schemaNode, error) {
	if node := c.compiled[name]; node != nil {
		return node, nil
	}
	raw, ok := c.defs[name]
	if !ok {
		if node := c.shared[name]; node != nil {
			return node, nil
		}
		return nil, fmt.Errorf("$defs/%s is not defined", name)
	}
	if c.pending[name] {
		return nil, fmt.Errorf("$defs/%s refers to itself", name)
	}
	c.pending[name] = true
	defer delete(c.pending, name)
	node, err := c.node(raw, "#/$defs/"+name)
	if err != nil {
		return nil, err
	}
	c.compiled[name] = node
	return node, nil
}

// node compiles one schema object. It refuses a keyword it does not know,
// except annotations, so a trusted schema the package compiles cannot
// silently lose a rule.
func (c *schemaCompiler) node(raw any, at string) (*schemaNode, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a schema object", at)
	}
	n := &schemaNode{at: at, minItems: -1, maxItems: -1, minLength: -1, maxLength: -1}
	var err error
	integer := func(key string) int {
		value, ok := object[key]
		if !ok {
			return -1
		}
		f, ok := number(value)
		if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
			err = cmpErr(err, fmt.Errorf("%s/%s is not a non-negative integer", at, key))
			return -1
		}
		return int(f)
	}
	float := func(key string) *float64 {
		value, ok := object[key]
		if !ok {
			return nil
		}
		f, ok := number(value)
		if !ok {
			err = cmpErr(err, fmt.Errorf("%s/%s is not a number", at, key))
			return nil
		}
		return &f
	}
	branches := func(key string) []*schemaNode {
		value, ok := object[key]
		if !ok {
			return nil
		}
		list, ok := value.([]any)
		if !ok || len(list) == 0 {
			err = cmpErr(err, fmt.Errorf("%s/%s is not a non-empty array", at, key))
			return nil
		}
		nodes := make([]*schemaNode, 0, len(list))
		for i, item := range list {
			child, childErr := c.node(item, at+"/"+key+"/"+strconv.Itoa(i))
			if childErr != nil {
				err = cmpErr(err, childErr)
				continue
			}
			nodes = append(nodes, child)
		}
		return nodes
	}
	for key, value := range object {
		switch key {
		case "title", "description", "$comment", "examples", "default", "format", "$schema", "$defs":
			// Annotations, and $defs, which $refs reach by name.
		case "type":
			n.types, err = cmpTypes(err, value, at)
		case "enum":
			list, ok := value.([]any)
			if !ok {
				err = cmpErr(err, fmt.Errorf("%s/enum is not an array", at))
				continue
			}
			for _, item := range list {
				scalar, ok := scalarValue(item)
				if !ok {
					err = cmpErr(err, fmt.Errorf("%s/enum holds a value that is not a string, number, boolean or null", at))
				}
				n.enum = append(n.enum, scalar)
			}
		case "const":
			scalar, ok := scalarValue(value)
			if !ok {
				err = cmpErr(err, fmt.Errorf("%s/const is not a string, number, boolean or null", at))
			}
			n.hasConst, n.constant = true, scalar
		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				err = cmpErr(err, fmt.Errorf("%s/properties is not an object", at))
				continue
			}
			n.properties = make(map[string]*schemaNode, len(props))
			for name, prop := range props {
				child, childErr := c.node(prop, at+"/properties/"+pointerToken(name))
				if childErr != nil {
					err = cmpErr(err, childErr)
					continue
				}
				n.properties[name] = child
			}
		case "required":
			list, _ := value.([]any)
			for _, item := range list {
				name, ok := item.(string)
				if !ok {
					err = cmpErr(err, fmt.Errorf("%s/required holds a value that is not a string", at))
					continue
				}
				n.required = append(n.required, name)
			}
		case "additionalProperties":
			if value != false {
				err = cmpErr(err, fmt.Errorf("%s/additionalProperties is not false", at))
			}
			n.closed = true
		case "items":
			n.items, err = c.child(err, value, at+"/items")
		case "minItems":
			n.minItems = integer(key)
		case "maxItems":
			n.maxItems = integer(key)
		case "uniqueItems":
			n.unique = value == true
		case "minLength":
			n.minLength = integer(key)
		case "maxLength":
			n.maxLength = integer(key)
		case "pattern":
			source, _ := value.(string)
			compiled, size, patternErr := compilePattern(source)
			if patternErr != nil {
				err = cmpErr(err, fmt.Errorf("%s/pattern: %v", at, patternErr))
				continue
			}
			n.pattern, n.patternCost = compiled, size
		case "minimum":
			n.minimum = float(key)
		case "maximum":
			n.maximum = float(key)
		case "exclusiveMinimum":
			n.exclusiveMin = float(key)
		case "exclusiveMaximum":
			n.exclusiveMax = float(key)
		case "allOf":
			n.allOf = branches(key)
		case "anyOf":
			n.anyOf = branches(key)
		case "oneOf":
			n.oneOf = branches(key)
		case "$ref":
			ref, _ := value.(string)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			if !ok || strings.Contains(name, "/") {
				err = cmpErr(err, fmt.Errorf("%s/$ref is not #/$defs/<name>", at))
				continue
			}
			n.refName = name
		default:
			err = cmpErr(err, fmt.Errorf("%s/%s is not a supported keyword", at, key))
		}
	}
	if err != nil {
		return nil, err
	}
	slices.Sort(n.required)
	for name := range n.properties {
		n.propertyNames = append(n.propertyNames, name)
	}
	slices.Sort(n.propertyNames)
	if n.refName != "" {
		target, refErr := c.def(n.refName)
		if refErr != nil {
			return nil, fmt.Errorf("%s/$ref: %w", at, refErr)
		}
		n.ref = target
	}
	return n, nil
}

func (c *schemaCompiler) child(err error, value any, at string) (*schemaNode, error) {
	child, childErr := c.node(value, at)
	return child, cmpErr(err, childErr)
}

func cmpErr(err, next error) error {
	if err != nil {
		return err
	}
	return next
}

func cmpTypes(err error, value any, at string) (uint8, error) {
	var names []any
	switch v := value.(type) {
	case string:
		names = []any{v}
	case []any:
		names = v
	}
	var bits uint8
	for _, name := range names {
		s, _ := name.(string)
		bit, ok := typeBits[s]
		if !ok {
			return 0, cmpErr(err, fmt.Errorf("%s/type names no JSON type", at))
		}
		bits |= bit
	}
	if bits == 0 {
		return 0, cmpErr(err, fmt.Errorf("%s/type names no JSON type", at))
	}
	return bits, err
}

// number returns a decoded JSON number as a float64.
func number(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}

// scalarValue returns a scalar with its number as a float64.
func scalarValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil, bool, string:
		return v, true
	case json.Number, float64:
		f, ok := number(v)
		return f, ok
	}
	return nil, false
}

// pointerToken escapes a JSON pointer token.
func pointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// decodeData decodes JSON data for validation: numbers become float64, and
// a number a double cannot hold is an error.
func decodeData(raw []byte) (any, error) {
	value, err := decodeCanonical(raw)
	if err != nil {
		return nil, err
	}
	return floatNumbers(value)
}

func floatNumbers(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return nil, errBadNumber
		}
		return f, nil
	case []any:
		for i := range v {
			item, err := floatNumbers(v[i])
			if err != nil {
				return nil, err
			}
			v[i] = item
		}
	case map[string]any:
		for key, item := range v {
			converted, err := floatNumbers(item)
			if err != nil {
				return nil, err
			}
			v[key] = converted
		}
	}
	return value, nil
}

// encodedSize returns the size of JSON text without its insignificant
// whitespace, stopping once it passes limit.
func encodedSize(raw []byte, limit int) int {
	size, inString, escaped := 0, false, false
	for _, c := range raw {
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		}
		size++
		if size > limit {
			return size
		}
	}
	return size
}

// The validation budget, in steps. A step is about one schema keyword
// applied to one value; a pattern match costs its text's length times its
// program size over patternStepScale. TestDataValidationCost measures the
// time the budget takes on the worst schemas the dialect allows.
const (
	validationBudget = 1 << 18
	patternStepScale = 16
)

var errBudget = errors.New("budget")

// dataPath is the JSON path of a value, built only when it is reported.
type dataPath struct {
	parent *dataPath
	name   string
	index  int
}

func (p *dataPath) String() string {
	if p == nil {
		return "the top level"
	}
	var tokens []string
	for at := p; at != nil; at = at.parent {
		if at.name != "" {
			tokens = append(tokens, safeToken(at.name))
		} else {
			tokens = append(tokens, strconv.Itoa(at.index))
		}
	}
	slices.Reverse(tokens)
	return "/" + strings.Join(tokens, "/")
}

func (p *dataPath) depth() int {
	n := 0
	for at := p; at != nil; at = at.parent {
		n++
	}
	return n
}

// dataError is a value that fails its schema. Its message never quotes the
// value. It is formatted only when it is reported: most failures are of
// options an anyOf or oneOf tries and drops.
type dataError struct {
	at     *dataPath
	format string
	args   []any
	// inOption is set on a failure that already says which option it is in.
	inOption bool
}

func (e *dataError) message() string {
	return fmt.Sprintf(e.format, e.args...)
}

func (e *dataError) Error() string {
	return "at " + e.at.String() + ": " + e.message()
}

// maxProblemLength bounds one problem, in characters.
const maxProblemLength = 400

// boundedProblem bounds a problem's length.
func boundedProblem(text string) string {
	return truncate(text, maxProblemLength)
}

// lazyMessage formats a failure inside another only when that is reported.
type lazyMessage struct{ e *dataError }

func (m lazyMessage) String() string { return m.e.message() }

func failure(at *dataPath, format string, args ...any) error {
	return &dataError{at: at, format: format, args: args}
}

// validation validates one value against a compiled schema within a budget:
// limit steps, or validationBudget when limit is zero.
type validation struct {
	steps int
	limit int
}

func (v *validation) spend(steps int) error {
	v.steps += steps
	limit := v.limit
	if limit == 0 {
		limit = validationBudget
	}
	if v.steps > limit {
		return errBudget
	}
	return nil
}

// validate returns nil, a *dataError, or errBudget.
func (v *validation) validate(n *schemaNode, value any, at *dataPath) error {
	if err := v.spend(1); err != nil {
		return err
	}
	if n.ref != nil {
		if err := v.validate(n.ref, value, at); err != nil {
			return err
		}
	}
	kind := typeOf(value)
	if n.types != 0 && n.types&kind == 0 && !(kind&typeInteger != 0 && n.types&typeNumber != 0) {
		return failure(at, "is %s; the schema allows %s", article(typeNames(primaryType(kind))), typeNames(n.types))
	}
	if n.enum != nil {
		if err := v.spend(len(n.enum)); err != nil {
			return err
		}
		if !slices.ContainsFunc(n.enum, func(e any) bool { return scalarEqual(e, value) }) {
			return failure(at, "is not one of the %d values of its enum", len(n.enum))
		}
	}
	if n.hasConst && !scalarEqual(n.constant, value) {
		return failure(at, "is not its const value")
	}
	if f, ok := value.(float64); ok {
		switch {
		case n.minimum != nil && f < *n.minimum:
			return failure(at, "is less than its minimum, %s", ecmaNumber(*n.minimum))
		case n.maximum != nil && f > *n.maximum:
			return failure(at, "is greater than its maximum, %s", ecmaNumber(*n.maximum))
		case n.exclusiveMin != nil && f <= *n.exclusiveMin:
			return failure(at, "is not greater than its exclusiveMinimum, %s", ecmaNumber(*n.exclusiveMin))
		case n.exclusiveMax != nil && f >= *n.exclusiveMax:
			return failure(at, "is not less than its exclusiveMaximum, %s", ecmaNumber(*n.exclusiveMax))
		}
	}
	if s, ok := value.(string); ok && (n.minLength >= 0 || n.maxLength >= 0 || n.pattern != nil) {
		if err := v.spend(len(s)/64 + 1); err != nil {
			return err
		}
		length := utf8.RuneCountInString(s)
		switch {
		case n.minLength >= 0 && length < n.minLength:
			return failure(at, "is %d characters long; its minLength is %d", length, n.minLength)
		case n.maxLength >= 0 && length > n.maxLength:
			return failure(at, "is %d characters long; its maxLength is %d", length, n.maxLength)
		}
		if n.pattern != nil {
			if err := v.spend((len(s) + 1) * n.patternCost / patternStepScale); err != nil {
				return err
			}
			if !n.pattern.MatchString(s) {
				return failure(at, "does not match its pattern")
			}
		}
	}
	for _, branch := range n.allOf {
		if err := v.validate(branch, value, at); err != nil {
			return err
		}
	}
	if n.anyOf != nil {
		var closest error
		matched := false
		for _, branch := range n.anyOf {
			err := v.validate(branch, value, at)
			if err == errBudget {
				return err
			}
			if err == nil {
				matched = true
				break
			}
			closest = deeper(closest, err)
		}
		if !matched {
			return v.noBranch("anyOf", len(n.anyOf), closest, at)
		}
	}
	if n.oneOf != nil {
		var closest error
		var matched []int
		for i, branch := range n.oneOf {
			err := v.validate(branch, value, at)
			if err == errBudget {
				return err
			}
			if err == nil {
				matched = append(matched, i)
				continue
			}
			closest = deeper(closest, err)
		}
		switch {
		case len(matched) == 0:
			return v.noBranch("oneOf", len(n.oneOf), closest, at)
		case len(matched) > 1:
			return failure(at, "matches options %d and %d of its oneOf; it must match exactly one", matched[0]+1, matched[1]+1)
		}
	}
	switch v2 := value.(type) {
	case []any:
		if err := v.spend(1); err != nil {
			return err
		}
		switch {
		case n.minItems >= 0 && len(v2) < n.minItems:
			return failure(at, "has %d items; its minItems is %d", len(v2), n.minItems)
		case n.maxItems >= 0 && len(v2) > n.maxItems:
			return failure(at, "has %d items; its maxItems is %d", len(v2), n.maxItems)
		}
		if n.items != nil {
			for i, item := range v2 {
				if err := v.validate(n.items, item, &dataPath{parent: at, index: i}); err != nil {
					return err
				}
			}
		}
		if n.unique {
			if err := v.spend(len(v2)); err != nil {
				return err
			}
			seen := map[string]int{}
			for i, item := range v2 {
				key, _ := json.Marshal(item)
				if j, ok := seen[string(key)]; ok {
					return failure(at, "has equal items %d and %d; its items must be unique", j, i)
				}
				seen[string(key)] = i
			}
		}
	case map[string]any:
		if err := v.spend(len(v2) + len(n.required)); err != nil {
			return err
		}
		for _, name := range n.required {
			if _, ok := v2[name]; !ok {
				return failure(at, "lacks %s", quotedName("the required property", name))
			}
		}
		if n.closed {
			var extra []string
			for name := range v2 {
				if _, ok := n.properties[name]; !ok {
					extra = append(extra, name)
				}
			}
			if len(extra) > 0 {
				slices.Sort(extra)
				return failure(at, "has %s, which its schema does not define (additionalProperties is false)", propertyName(extra[0]))
			}
		}
		for _, name := range n.propertyNames {
			if value, ok := v2[name]; ok {
				if err := v.validate(n.properties[name], value, &dataPath{parent: at, name: name}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// noBranch reports a value that matches no option of an anyOf or oneOf.
// When an option failed on a part of the value, that failure is the most
// useful one to show.
func (v *validation) noBranch(keyword string, options int, closest error, at *dataPath) error {
	inner, _ := closest.(*dataError)
	if inner != nil && inner.at.depth() > at.depth() {
		if inner.inOption {
			return inner
		}
		return &dataError{at: inner.at, format: "%s (in the closest option of the %s at %s)", args: []any{lazyMessage{inner}, keyword, at}, inOption: true}
	}
	if inner != nil {
		return failure(at, "matches none of the %d options of its %s; the closest %s", options, keyword, lazyMessage{inner})
	}
	return failure(at, "matches none of the %d options of its %s", options, keyword)
}

// deeper returns the failure further into the value, the first on a tie.
func deeper(a, b error) error {
	ea, _ := a.(*dataError)
	if ea == nil {
		return b
	}
	if eb, _ := b.(*dataError); eb != nil && eb.at.depth() > ea.at.depth() {
		return b
	}
	return a
}

// Messages are shown to people and read by the agents that write tiles, and
// definitions may come from other people, so a name from a definition or
// from data appears in one only when it is a plain identifier of at most 64
// characters.

// propertyName names a property in a message.
func propertyName(name string) string {
	return quotedName("the property", name)
}

// quotedName names something in a message: "<what> "name"" for a plain
// name, otherwise what it is without its name ("the property" becomes "a
// property").
func quotedName(what, name string) string {
	if isPlainName(name) {
		return fmt.Sprintf("%s %q", what, name)
	}
	return "a " + strings.TrimPrefix(what, "the ")
}

// safeToken is a JSON pointer token for a name in a message: the name when
// it is plain, otherwise "…".
func safeToken(name string) string {
	if isPlainName(name) {
		return name
	}
	return "…"
}

func isPlainName(name string) bool {
	return len(name) <= 64 && plainName.MatchString(name)
}

var plainName = regexp.MustCompile(`^\$?[A-Za-z0-9_.-]+$`)

func typeOf(value any) uint8 {
	switch v := value.(type) {
	case nil:
		return typeNull
	case bool:
		return typeBoolean
	case map[string]any:
		return typeObject
	case []any:
		return typeArray
	case float64:
		if v == math.Trunc(v) && !math.IsInf(v, 0) {
			return typeNumber | typeInteger
		}
		return typeNumber
	case string:
		return typeString
	}
	return 0
}

func primaryType(bits uint8) uint8 {
	if bits&typeInteger != 0 {
		return typeInteger
	}
	return bits
}

func article(name string) string {
	switch name {
	case "null":
		return "null"
	case "object", "array", "integer":
		return "an " + name
	}
	return "a " + name
}

func scalarEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	}
	return false
}
