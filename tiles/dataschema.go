package tiles

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/schema"
)

// A custom tile's data schema is written in a restricted dialect of JSON
// Schema draft 2020-12, so validating untrusted data against an untrusted
// schema has a bounded cost and the schema bounds what a model fills in.
// CheckDataSchema checks the raw schema against the dialect before anything
// compiles it.

// The dialect's limits.
const (
	maxSchemaObjects    = 512
	maxSchemaDepth      = 12
	maxCombinatorDepth  = 2
	maxAnyOf            = 8
	maxAllOf            = 4
	maxArrayItems       = 500
	maxStringLength     = 20000
	maxEnumValues       = 64
	maxPatternLength    = 256
	maxDescription      = 1000
	maxSchemaTitle      = 200
	maxSchemaProblems   = 20
	schemaDraft202012   = "https://json-schema.org/draft/2020-12/schema"
	reservedDefsPointer = "#/$defs/"
)

// reservedDefinitions are the $defs every data schema may refer to and none
// may define: citation (a review anchor or a reference to an item of the
// document), markdown (text the kit renders) and tier (a risk tier), and the
// definitions they refer to.
var reservedDefinitions = []string{"anchor", "attachmentId", "citation", "hunkId", "itemRef", "markdown", "sectionId", "tier", "tileId"}

// dataKeywords are the keywords a data schema may use.
var dataKeywords = []string{
	"$comment", "$defs", "$ref", "$schema", "additionalProperties", "allOf", "anyOf", "const",
	"default", "description", "enum", "examples", "exclusiveMaximum", "exclusiveMinimum", "items",
	"maxItems", "maxLength", "maximum", "minItems", "minLength", "minimum", "oneOf", "pattern",
	"properties", "required", "title", "type",
}

// keywordHints say what to do instead of a keyword the dialect refuses.
var keywordHints = map[string]string{
	"format":                "describe the format in description, or check it with pattern",
	"uniqueItems":           "say in description that items are unique; the dialect cannot check it cheaply",
	"if":                    "use oneOf with one branch per case",
	"then":                  "use oneOf with one branch per case",
	"else":                  "use oneOf with one branch per case",
	"not":                   "describe the allowed values with enum, const, pattern or oneOf",
	"patternProperties":     "list every property under properties",
	"propertyNames":         "list every property under properties",
	"additionalItems":       "use items",
	"prefixItems":           "use an array of objects with named properties",
	"contains":              "use items",
	"minContains":           "use items",
	"maxContains":           "use items",
	"dependentRequired":     "use oneOf with one branch per case",
	"dependentSchemas":      "use oneOf with one branch per case",
	"dependencies":          "use oneOf with one branch per case",
	"unevaluatedProperties": `use "additionalProperties": false`,
	"unevaluatedItems":      "use items",
	"$id":                   "refer to definitions with #/$defs/<name>",
	"$anchor":               "refer to definitions with #/$defs/<name>",
	"$dynamicRef":           "refer to definitions with #/$defs/<name>",
	"$dynamicAnchor":        "refer to definitions with #/$defs/<name>",
	"$vocabulary":           "remove it",
	"definitions":           "use $defs",
	"nullable":              `use a type array such as ["string", "null"]`,
	"multipleOf":            "say it in description",
	"minProperties":         "use required",
	"maxProperties":         "list every property under properties",
	"contentEncoding":       "carry the text itself",
	"contentMediaType":      "carry the text itself",
	"contentSchema":         "carry the text itself",
}

var defName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// reservedSchemas are the reserved definitions, compiled once from the
// merged tile schema, and the same definitions decoded, for the library
// compile.
type reservedSchemas struct {
	nodes map[string]*schemaNode
	raw   map[string]any
	// allDefs are all of the merged tile schema's $defs, decoded.
	allDefs map[string]any
	// citations maps each citation type to the option of $defs/citation
	// for it. The options of the anchor and item reference oneOfs each
	// have a type const of their own, so an object is a citation exactly
	// when it passes the option its type names.
	citations map[string]*schemaNode
}

var reserved = sync.OnceValues(func() (*reservedSchemas, error) {
	merged, err := schema.MergedTiles()
	if err != nil {
		return nil, err
	}
	value, err := decodeCanonical(merged)
	if err != nil {
		return nil, err
	}
	defs, _ := value.(map[string]any)["$defs"].(map[string]any)
	raw := map[string]any{}
	for _, name := range reservedDefinitions {
		def, ok := defs[name]
		if !ok {
			return nil, fmt.Errorf("tiles: the tile schema has no $defs/%s", name)
		}
		raw[name] = def
	}
	compiler := &schemaCompiler{defs: raw, compiled: map[string]*schemaNode{}, pending: map[string]bool{}}
	nodes := map[string]*schemaNode{}
	for _, name := range reservedDefinitions {
		node, err := compiler.def(name)
		if err != nil {
			return nil, fmt.Errorf("tiles: compiling $defs/%s: %w", name, err)
		}
		nodes[name] = node
	}
	citations := map[string]*schemaNode{}
	for _, union := range []*schemaNode{nodes["anchor"], nodes["itemRef"]} {
		for _, option := range union.oneOf {
			var kind string
			if typeNode := option.properties["type"]; typeNode != nil {
				kind, _ = typeNode.constant.(string)
			}
			if kind == "" || citations[kind] != nil || citationKinds[kind] == "" {
				return nil, fmt.Errorf("tiles: an option of %s has no type const of its own", union.at)
			}
			citations[kind] = option
		}
	}
	if len(citations) != len(citationKinds) {
		return nil, fmt.Errorf("tiles: $defs/citation has %d types, not the %d the package names", len(citations), len(citationKinds))
	}
	return &reservedSchemas{nodes: nodes, raw: raw, allDefs: defs, citations: citations}, nil
})

// compiledSchema is a checked data schema, ready to validate data.
type compiledSchema struct {
	root *schemaNode
}

// CheckDataSchema checks a custom tile's data schema against the dialect
// and returns its problems, each naming the place in the schema, what is
// wrong and how to fix it. It returns nil for a schema in the dialect.
//
// The dialect is JSON Schema draft 2020-12 with only these keywords: type;
// properties, required and additionalProperties, which must be false on
// every object; items, minItems and maxItems, which every array needs (at
// most 500); enum (at most 64 strings, numbers, booleans or nulls) and
// const (one of those); minLength and maxLength, which every string needs
// (at most 20000) unless it has enum, const or pattern; pattern (at most 256
// characters, Go RE2 syntax, compiling to at most 400 instructions);
// minimum, maximum, exclusiveMinimum and exclusiveMaximum; anyOf and oneOf
// (at most 8 options) and allOf (at most 4), nested at most 2 deep; $ref to
// #/$defs/<name> and $defs at the top level; and the annotations title,
// description, $comment, examples and default. The top level is an object,
// the encoded schema is at most 32 KiB, it has at most 512 schema objects,
// nested at most 12 deep counting through $ref, and no $ref may lead back to
// itself. The reserved definitions (citation, markdown, tier and those they
// refer to) are provided and may not be defined.
func CheckDataSchema(raw json.RawMessage) []string {
	_, problems := compileDataSchema(raw)
	return problems
}

func compileDataSchema(raw json.RawMessage) (*compiledSchema, []string) {
	if size := encodedSize(raw, maxDataSchemaBytes); size > maxDataSchemaBytes {
		return nil, []string{fmt.Sprintf("the schema is larger than %d KiB encoded without whitespace; make it smaller", maxDataSchemaBytes>>10)}
	}
	value, err := decodeCanonical(raw)
	if err != nil {
		return nil, []string{strings.TrimPrefix(err.Error(), "tiles: ")}
	}
	shared, err := reserved()
	if err != nil {
		return nil, []string{err.Error()}
	}
	c := &dialectChecker{defs: map[string]*dialectNode{}}
	c.check(value)
	if len(c.problems) > 0 {
		return nil, c.report()
	}
	root := value.(map[string]any)
	defs, _ := root["$defs"].(map[string]any)
	compiler := &schemaCompiler{defs: defs, shared: shared.nodes, compiled: map[string]*schemaNode{}, pending: map[string]bool{}}
	node, err := compiler.node(root, "#")
	if err != nil {
		return nil, []string{err.Error()}
	}
	if _, err := resolveWithLibrary(root, shared.raw); err != nil {
		return nil, []string{"the schema does not compile: " + conciseError(err)}
	}
	return &compiledSchema{root: node}, nil
}

// resolveWithLibrary resolves the schema with the reserved definitions
// merged in, with the JSON Schema library the rest of baum uses, so a schema
// that passes the dialect is a schema any draft 2020-12 tool accepts.
func resolveWithLibrary(root map[string]any, reservedDefs map[string]any) (*jsonschema.Resolved, error) {
	merged := make(map[string]any, len(root))
	for key, value := range root {
		merged[key] = value
	}
	defs := map[string]any{}
	if own, ok := root["$defs"].(map[string]any); ok {
		for name, def := range own {
			defs[name] = def
		}
	}
	for name, def := range reservedDefs {
		defs[name] = def
	}
	merged["$defs"] = defs
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(encoded, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}

// dialectNode is a schema object as the dialect checker sees it: its
// children, its $ref and whether it combines options.
type dialectNode struct {
	at         string
	children   []*dialectNode
	ref        string
	combinator bool
}

type dialectChecker struct {
	problems []string
	objects  int
	defs     map[string]*dialectNode
	root     *dialectNode
}

func (c *dialectChecker) add(at, format string, args ...any) {
	c.problems = append(c.problems, at+": "+fmt.Sprintf(format, args...))
}

func (c *dialectChecker) report() []string {
	if len(c.problems) <= maxSchemaProblems {
		return c.problems
	}
	return append(c.problems[:maxSchemaProblems:maxSchemaProblems], fmt.Sprintf("and %d more problems", len(c.problems)-maxSchemaProblems))
}

func (c *dialectChecker) check(value any) {
	root, ok := value.(map[string]any)
	if !ok {
		c.add("#", `the schema is not an object; start from {"type": "object", "properties": {...}, "additionalProperties": false}`)
		return
	}
	if root["type"] != "object" {
		c.add("#", `the top level must have "type": "object"`)
	}
	if s, ok := root["$schema"]; ok && s != schemaDraft202012 {
		c.add("#/$schema", "only %s is supported; remove $schema or use that", schemaDraft202012)
	}
	if raw, ok := root["$defs"]; ok {
		defs, ok := raw.(map[string]any)
		if !ok {
			c.add("#/$defs", "is not an object of definitions")
		}
		names := sortedKeys(defs)
		for _, name := range names {
			at := "#/$defs/" + pointerToken(name)
			switch {
			case slices.Contains(reservedDefinitions, name):
				c.add(at, `%s is provided to every data schema and cannot be redefined; remove this definition and refer to it with {"$ref": "#/$defs/%s"}`, name, name)
				continue
			case !defName.MatchString(name):
				c.add(at, "a definition name must start with a letter and hold only letters, digits and underscores (at most 64)")
				continue
			}
			c.defs[name] = c.node(defs[name], at, false)
		}
	}
	c.root = c.node(root, "#", true)
	if c.objects > maxSchemaObjects {
		c.add("#", "the schema has %d schema objects, more than %d; simplify it or share repeated shapes through $defs", c.objects, maxSchemaObjects)
	}
	c.checkRefs()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// node checks one schema object and the objects inside it.
func (c *dialectChecker) node(raw any, at string, top bool) *dialectNode {
	n := &dialectNode{at: at}
	c.objects++
	object, ok := raw.(map[string]any)
	if !ok {
		c.add(at, "is not a schema object; write a JSON object such as {\"type\": \"string\", \"maxLength\": 200}")
		return n
	}
	keys := sortedKeys(object)
	for _, key := range keys {
		switch {
		case (key == "$defs" || key == "$schema") && !top:
			c.add(at+"/"+key, "%s may appear only at the top level of the schema; move it there", key)
		case !slices.Contains(dataKeywords, key):
			hint := keywordHints[key]
			if hint == "" {
				hint = "the dialect allows only " + strings.Join(dataKeywords, ", ")
			}
			c.add(at+"/"+pointerToken(key), "%q is not allowed in a tile data schema; %s", key, hint)
		}
	}

	types := c.types(object, at)
	has := func(key string) bool { _, ok := object[key]; return ok }
	if ref, ok := object["$ref"]; ok {
		name, ok := ref.(string)
		target, isDef := strings.CutPrefix(name, reservedDefsPointer)
		if !ok || !isDef || strings.Contains(target, "/") || target == "" {
			c.add(at+"/$ref", `a $ref must be "#/$defs/<name>"; define the shape under $defs at the top level`)
		} else {
			n.ref = target
		}
		for _, key := range keys {
			switch key {
			case "$ref", "title", "description", "$comment", "examples", "default":
			default:
				if slices.Contains(dataKeywords, key) {
					c.add(at+"/"+pointerToken(key), `a schema with $ref may carry only title, description, $comment, examples and default; move %q into the definition, or combine with allOf`, key)
				}
			}
		}
		c.annotations(object, at)
		return n
	}
	if types == 0 && !has("enum") && !has("const") && !has("anyOf") && !has("oneOf") && !has("allOf") {
		c.add(at, `say what the value is: add "type" (or enum, const, $ref, anyOf, oneOf or allOf)`)
	}
	for _, key := range []string{"properties", "required", "additionalProperties"} {
		if has(key) && types != 0 && types&typeObject == 0 {
			c.add(at+"/"+key, `%s applies only to objects; add "object" to the type or remove %s`, key, key)
		}
	}
	for _, key := range []string{"items", "minItems", "maxItems"} {
		if has(key) && types != 0 && types&typeArray == 0 {
			c.add(at+"/"+key, `%s applies only to arrays; add "array" to the type or remove %s`, key, key)
		}
	}
	for _, key := range []string{"minLength", "maxLength", "pattern"} {
		if has(key) && types != 0 && types&typeString == 0 {
			c.add(at+"/"+key, `%s applies only to strings; add "string" to the type or remove %s`, key, key)
		}
	}
	for _, key := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if !has(key) {
			continue
		}
		if types != 0 && types&(typeNumber|typeInteger) == 0 {
			c.add(at+"/"+key, `%s applies only to numbers; add "number" or "integer" to the type or remove %s`, key, key)
		}
		if f, ok := number(object[key]); !ok || math.IsInf(f, 0) {
			c.add(at+"/"+key, "must be a number")
		}
	}

	// Objects.
	var properties map[string]any
	if raw, ok := object["properties"]; ok {
		properties, ok = raw.(map[string]any)
		if !ok {
			c.add(at+"/properties", "must be an object mapping each property name to its schema")
		}
	}
	if types&typeObject != 0 || properties != nil {
		if object["additionalProperties"] != false {
			c.add(at, `an object needs "additionalProperties": false; list every property under properties`)
		}
	}
	if raw, ok := object["additionalProperties"]; ok && raw != false {
		c.add(at+"/additionalProperties", `must be false; list every property under properties`)
	}
	for _, name := range sortedKeys(properties) {
		n.children = append(n.children, c.node(properties[name], at+"/properties/"+pointerToken(name), false))
	}
	if raw, ok := object["required"]; ok {
		list, ok := raw.([]any)
		if !ok {
			c.add(at+"/required", "must be an array of property names")
		}
		seen := map[string]bool{}
		for _, item := range list {
			name, ok := item.(string)
			switch {
			case !ok:
				c.add(at+"/required", "must hold only property names (strings)")
			case seen[name]:
				c.add(at+"/required", "names %q twice", name)
			case properties[name] == nil:
				c.add(at+"/required", "names %q, which properties does not define; add it to properties or remove it from required", name)
			}
			seen[name] = true
		}
	}

	// Arrays.
	if types&typeArray != 0 {
		if !has("items") {
			c.add(at, `an array needs "items", the schema of each item`)
		}
		if !has("maxItems") {
			c.add(at, `an array needs "maxItems" (at most %d); add the most items the tile can show`, maxArrayItems)
		}
	}
	if raw, ok := object["items"]; ok {
		if _, isObject := raw.(map[string]any); !isObject {
			c.add(at+"/items", "must be one schema object, the schema of every item")
		} else {
			n.children = append(n.children, c.node(raw, at+"/items", false))
		}
	}
	minItems := c.count(object, "minItems", at, maxArrayItems)
	maxItems := c.count(object, "maxItems", at, maxArrayItems)
	if minItems > maxItems && maxItems >= 0 {
		c.add(at+"/minItems", "is greater than maxItems")
	}

	// Strings.
	if types&typeString != 0 && !has("maxLength") && !has("enum") && !has("const") && !has("pattern") {
		c.add(at, `a string needs "maxLength" (at most %d), unless it has enum, const or pattern; add the longest text the tile can show`, maxStringLength)
	}
	minLength := c.count(object, "minLength", at, maxStringLength)
	maxLength := c.count(object, "maxLength", at, maxStringLength)
	if minLength > maxLength && maxLength >= 0 {
		c.add(at+"/minLength", "is greater than maxLength")
	}
	if raw, ok := object["pattern"]; ok {
		pattern, ok := raw.(string)
		switch {
		case !ok:
			c.add(at+"/pattern", "must be a string")
		case utf8.RuneCountInString(pattern) > maxPatternLength:
			c.add(at+"/pattern", "is longer than %d characters; simplify it", maxPatternLength)
		default:
			if _, _, err := compilePattern(pattern); err != nil {
				c.add(at+"/pattern", "does not compile as a Go RE2 regular expression (no lookaround or backreferences; repetition counts make it larger): %v", err)
			}
		}
	}

	// Values.
	if raw, ok := object["enum"]; ok {
		list, ok := raw.([]any)
		switch {
		case !ok || len(list) == 0:
			c.add(at+"/enum", "must be a non-empty array of values")
		case len(list) > maxEnumValues:
			c.add(at+"/enum", "has %d values, more than %d; use a string with maxLength and describe the values", len(list), maxEnumValues)
		}
		for _, item := range list {
			if _, ok := scalarValue(item); !ok {
				c.add(at+"/enum", "may hold only strings, numbers, booleans and null")
				break
			}
		}
	}
	if raw, ok := object["const"]; ok {
		if _, ok := scalarValue(raw); !ok {
			c.add(at+"/const", "must be a string, number, boolean or null")
		}
	}

	// Combinators.
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		n.combinator = true
		limit := maxAnyOf
		if key == "allOf" {
			limit = maxAllOf
		}
		list, ok := raw.([]any)
		switch {
		case !ok || len(list) == 0:
			c.add(at+"/"+key, "must be a non-empty array of schema objects")
		case len(list) > limit:
			c.add(at+"/"+key, "has %d options, more than %d; merge options or use enum", len(list), limit)
		}
		for i, item := range list {
			n.children = append(n.children, c.node(item, fmt.Sprintf("%s/%s/%d", at, key, i), false))
		}
	}
	c.annotations(object, at)
	return n
}

// types checks the type keyword and returns its bits, zero when absent or
// wrong.
func (c *dialectChecker) types(object map[string]any, at string) uint8 {
	raw, ok := object["type"]
	if !ok {
		return 0
	}
	var names []any
	switch v := raw.(type) {
	case string:
		names = []any{v}
	case []any:
		names = v
	}
	var bits uint8
	for _, name := range names {
		s, _ := name.(string)
		bit, ok := typeBits[s]
		if !ok || bits&bit != 0 {
			c.add(at+"/type", "must be one of object, array, string, number, integer, boolean and null, or an array of distinct ones")
			return 0
		}
		bits |= bit
	}
	if bits == 0 {
		c.add(at+"/type", "must be one of object, array, string, number, integer, boolean and null, or an array of distinct ones")
	}
	return bits
}

// count checks a non-negative integer keyword with a maximum and returns
// it, or -1.
func (c *dialectChecker) count(object map[string]any, key, at string, limit int) int {
	raw, ok := object[key]
	if !ok {
		return -1
	}
	f, ok := number(raw)
	switch {
	case !ok || f < 0 || f != math.Trunc(f):
		c.add(at+"/"+key, "must be a non-negative integer")
		return -1
	case f > float64(limit) && (key == "maxItems" || key == "maxLength" || key == "minItems" || key == "minLength"):
		c.add(at+"/"+key, "is %s, more than %d; a tile shows at most that many", ecmaNumber(f), limit)
		return -1
	}
	return int(f)
}

func (c *dialectChecker) annotations(object map[string]any, at string) {
	for key, limit := range map[string]int{"title": maxSchemaTitle, "description": maxDescription, "$comment": maxDescription} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		s, ok := raw.(string)
		switch {
		case !ok:
			c.add(at+"/"+key, "must be a string")
		case utf8.RuneCountInString(s) > limit:
			c.add(at+"/"+key, "is longer than %d characters; shorten it", limit)
		}
	}
	if raw, ok := object["examples"]; ok {
		if _, ok := raw.([]any); !ok {
			c.add(at+"/examples", "must be an array")
		}
	}
}

// checkRefs checks that every $ref names a definition, that no $ref leads
// back to itself, and the nesting depth and the combinator nesting counted
// through $refs.
func (c *dialectChecker) checkRefs() {
	if c.root == nil {
		return
	}
	type measure struct{ depth, combinators int }
	memo := map[*dialectNode]measure{}
	visiting := map[string]bool{}
	reported := map[string]bool{}
	var walk func(n *dialectNode, chain []string) measure
	walk = func(n *dialectNode, chain []string) measure {
		if m, ok := memo[n]; ok {
			return m
		}
		m := measure{depth: 1}
		if n.combinator {
			m.combinators = 1
		}
		var below measure
		if n.ref != "" && !slices.Contains(reservedDefinitions, n.ref) {
			def := c.defs[n.ref]
			switch {
			case def == nil:
				if !reported[n.at] {
					reported[n.at] = true
					c.add(n.at+"/$ref", "names #/$defs/%s, which is not defined; define it under $defs at the top level", n.ref)
				}
			case visiting[n.ref]:
				if !reported[n.ref] {
					reported[n.ref] = true
					cycle := append(slices.Clone(chain), n.ref)
					c.add(n.at+"/$ref", "the $ref leads back to itself (%s); tile data schemas cannot be recursive, so write out each level the tile shows", strings.Join(cycle, " → "))
				}
				return m
			default:
				visiting[n.ref] = true
				below = walk(def, append(chain, n.ref))
				delete(visiting, n.ref)
			}
		}
		for _, child := range n.children {
			cm := walk(child, chain)
			below.depth = max(below.depth, cm.depth)
			below.combinators = max(below.combinators, cm.combinators)
		}
		m.depth += below.depth
		m.combinators += below.combinators
		memo[n] = m
		return m
	}
	m := walk(c.root, nil)
	for name, def := range c.defs {
		if _, ok := memo[def]; !ok {
			visiting[name] = true
			walk(def, []string{name})
			delete(visiting, name)
		}
	}
	if m.depth > maxSchemaDepth {
		c.add("#", "the schema nests %d schema objects deep counting through $ref, more than %d; flatten it", m.depth, maxSchemaDepth)
	}
	if m.combinators > maxCombinatorDepth {
		c.add("#", `anyOf, oneOf and allOf nest %d deep counting through properties, items and $ref, more than %d; use a type array such as ["string", "null"] for a nullable value, or merge options`, m.combinators, maxCombinatorDepth)
	}
}
