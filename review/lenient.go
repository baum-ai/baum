package review

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// lenientContract derives the model-facing schema from the strict authored
// contract. It asks the model for less than the host stores: oneOf/anyOf
// unions become discriminated allOf/if/then chains so a wrong branch is
// reported by name, optional fields accept null, fields with a canonical empty
// value or a host-derived value are not required, prefixed identifiers
// accept any spelling, and code sources name their revision by role and an
// excerpt by its hunk headers. normalizeAuthored maps a document this schema
// accepts onto the strict contract before it is validated.
func lenientContract(strict map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(strict)
	if err != nil {
		return nil, err
	}
	var lenient map[string]any
	if err := json.Unmarshal(encoded, &lenient); err != nil {
		return nil, err
	}
	defs, _ := lenient["$defs"].(map[string]any)
	relaxTileCodeFile(defs)
	// The review time estimate is rough: normalization rounds and clamps it
	// (reviewMinutes), so a provider never rejects a reply over its range.
	// The stored contract lets it be absent, for documents from before the
	// estimate existed, but the model always gives one: required and never
	// null, so a strict provider cannot answer null and skip it.
	properties, _ := lenient["properties"].(map[string]any)
	summary, _ := properties["summary"].(map[string]any)
	summaryProperties, _ := summary["properties"].(map[string]any)
	if minutes, ok := summaryProperties["review_minutes"].(map[string]any); ok {
		delete(minutes, "minimum")
		delete(minutes, "maximum")
		minutes["type"] = "number"
		// The stored description allows a null or absent estimate for older
		// documents; the model is told to always give one.
		if description, _ := minutes["description"].(string); description != "" {
			if cut := strings.Index(description, " Null or absent"); cut >= 0 {
				minutes["description"] = description[:cut] + " Always give an estimate."
			}
		}
		if required, _ := summary["required"].([]any); !containsString(stringSlice(required), "review_minutes") {
			summary["required"] = append(required, "review_minutes")
		}
	}
	relaxSchema(lenient, defs)
	for _, def := range defs {
		if node, ok := def.(map[string]any); ok {
			relaxSchema(node, defs)
		}
	}
	if err := requireEvidenceHunkAnchors(lenient, strict); err != nil {
		return nil, err
	}
	return lenient, nil
}

// requireEvidenceHunkAnchors keeps risk and behavior evidence selection as the
// model's job. Each item must name at least one hunk, while an optional lines
// range may narrow that hunk further. The host must not guess this evidence
// from the enclosing section.
func requireEvidenceHunkAnchors(lenient, strict map[string]any) error {
	lenientDefs, _ := lenient["$defs"].(map[string]any)
	anchor, _ := lenientDefs["anchor"].(map[string]any)
	anchorProperties, _ := anchor["properties"].(map[string]any)
	lineRange, _ := anchorProperties["lines"].(map[string]any)
	if lineRange == nil {
		return fmt.Errorf("review contract has no hunk anchor line range")
	}
	// The discriminated union carries a copy of the property in its hunk branch,
	// so relax every occurrence rather than only its top-level documentation.
	walkSchema(anchor, func(node map[string]any) {
		properties, _ := node["properties"].(map[string]any)
		rangeSchema, _ := properties["lines"].(map[string]any)
		if rangeSchema == nil {
			return
		}
		rangeSchema["description"] = "Optional inclusive right/new-side line range [start, end]. Omit it unless the coordinates are known; the platform clamps overlapping ranges and otherwise shows the full hunk."
		// Let best-effort coordinates reach deterministic normalization. The
		// stored contract still requires two positive coordinates afterwards.
		walkSchema(rangeSchema, func(rangeNode map[string]any) {
			if rangeNode["type"] == "integer" || containsString(stringSlice(rangeNode["type"]), "integer") {
				delete(rangeNode, "minimum")
			}
			if rangeNode["type"] == "array" || containsString(stringSlice(rangeNode["type"]), "array") {
				delete(rangeNode, "minItems")
				delete(rangeNode, "maxItems")
			}
		})
	})
	riskItems := func(root map[string]any) (map[string]any, bool) {
		defs, _ := root["$defs"].(map[string]any)
		section, _ := defs["section"].(map[string]any)
		properties, _ := section["properties"].(map[string]any)
		risks, _ := properties["risks"].(map[string]any)
		items, ok := risks["items"].(map[string]any)
		return items, ok
	}
	behaviorSteps := func(root map[string]any, relaxed bool) (map[string]any, bool) {
		defs, _ := root["$defs"].(map[string]any)
		block, _ := defs["block"].(map[string]any)
		branches, _ := block["oneOf"].([]any)
		if relaxed {
			branches, _ = block["allOf"].([]any)
		}
		for _, raw := range branches {
			branch, _ := raw.(map[string]any)
			if relaxed {
				condition, _ := branch["if"].(map[string]any)
				properties, _ := condition["properties"].(map[string]any)
				kind, _ := properties["kind"].(map[string]any)
				if !containsString(stringSlice(kind["enum"]), "behavior") {
					continue
				}
				branch, _ = branch["then"].(map[string]any)
			}
			properties, _ := branch["properties"].(map[string]any)
			kind, _ := properties["kind"].(map[string]any)
			if !relaxed && kind["const"] != "behavior" {
				continue
			}
			steps, _ := properties["steps"].(map[string]any)
			items, ok := steps["items"].(map[string]any)
			return items, ok
		}
		return nil, false
	}
	require := func(item, strictItem map[string]any) error {
		properties, _ := strictItem["properties"].(map[string]any)
		encoded, err := json.Marshal(properties["anchors"])
		if err != nil {
			return err
		}
		var anchors map[string]any
		if err := json.Unmarshal(encoded, &anchors); err != nil {
			return err
		}
		anchors["contains"] = map[string]any{
			"type":       "object",
			"properties": map[string]any{"type": map[string]any{"const": "hunk"}},
			"required":   []any{"type"},
		}
		item["properties"].(map[string]any)["anchors"] = anchors
		item["required"] = append(removeStrings(item["required"], "anchors"), "anchors")
		return nil
	}
	risk, riskOK := riskItems(lenient)
	strictRisk, strictRiskOK := riskItems(strict)
	behavior, behaviorOK := behaviorSteps(lenient, true)
	strictBehavior, strictBehaviorOK := behaviorSteps(strict, false)
	if !riskOK || !strictRiskOK || !behaviorOK || !strictBehaviorOK {
		return fmt.Errorf("review contract has no risk or behavior items")
	}
	if err := require(risk, strictRisk); err != nil {
		return err
	}
	return require(behavior, strictBehavior)
}

func walkSchema(value any, visit func(map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		visit(value)
		for _, child := range value {
			walkSchema(child, visit)
		}
	case []any:
		for _, child := range value {
			walkSchema(child, visit)
		}
	}
}

// relaxTileCodeFile lets a source name its revision as head or merge_base and
// an excerpt by hunk headers alone; the host resolves the SHA, sets
// review_version and fills the excerpt lines from the bundle's comparison.
func relaxTileCodeFile(defs map[string]any) {
	file, _ := defs["tileCodeFile"].(map[string]any)
	if file == nil {
		return
	}
	props := file["properties"].(map[string]any)
	revision, _ := props["revision"].(map[string]any)
	props["revision"] = map[string]any{
		"anyOf":       []any{map[string]any{"type": "string", "enum": []any{tileRevisionHead, tileRevisionMergeBase}}, revision},
		"description": "head for the pull request head, merge_base for deleted files or the merge-base tree; a full SHA of either is also accepted.",
	}
	file["required"] = removeStrings(file["required"], "review_version")
	if diff, ok := props["diff"].(map[string]any); ok {
		if hunks, ok := diff["properties"].(map[string]any)["hunks"].(map[string]any); ok {
			hunk := hunks["items"].(map[string]any)
			hunk["required"] = removeStrings(hunk["required"], "lines")
			hunk["description"] = "Name the excerpt by its hunk header, @@ -old,count +new,count @@, within the supplied comparison; the platform fills in the lines. Lines given here are replaced."
		}
	}
}

const (
	tileRevisionHead      = "head"
	tileRevisionMergeBase = "merge_base"
)

func relaxSchema(node map[string]any, defs map[string]any) {
	for _, key := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[key].([]any); ok {
			if union := discriminatedUnion(branches, defs); union != nil {
				delete(node, key)
				maps.Copy(node, union)
				continue
			}
			for _, branch := range branches {
				if child, ok := branch.(map[string]any); ok {
					relaxSchema(child, defs)
				}
			}
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		required := stringSlice(node["required"])
		kept := make([]any, 0, len(required))
		for _, name := range required {
			prop, _ := props[name].(map[string]any)
			if prop != nil && defaultable(name, prop, defs) {
				continue
			}
			kept = append(kept, name)
		}
		node["required"] = kept
		for name, raw := range props {
			prop, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			relaxSchema(prop, defs)
			if !containsString(stringSlice(kept), name) {
				props[name] = nullable(prop)
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "then", "if", "else", "not"} {
		if child, ok := node[key].(map[string]any); ok {
			relaxSchema(child, defs)
		}
	}
	if all, ok := node["allOf"].([]any); ok {
		for _, branch := range all {
			if child, ok := branch.(map[string]any); ok {
				relaxSchema(child, defs)
			}
		}
	}
	if pattern, ok := node["pattern"].(string); ok && identifierPattern(pattern) != nil {
		delete(node, "pattern")
		delete(node, "maxLength")
		node["minLength"] = 1
	}
}

// identifierPattern recognizes the patterns normalizeIdentifier can satisfy
// from any spelling: prefixed IDs, prefixed references, tile-local names and
// risk categories.
var identifierPatterns = struct {
	prefixed, references, local, category *regexp.Regexp
}{
	prefixed:   regexp.MustCompile(`^\^([a-z]+)_\[a-z0-9_\]\+\$$`),
	references: regexp.MustCompile(`^\^\(([a-z|]+)\)_\[a-z0-9_\]\+\$$`),
	local:      regexp.MustCompile(`^\^\[a-z\]\[a-z0-9_\]\*\$$`),
	category:   regexp.MustCompile(`^\^\[a-z0-9_\]\+\$$`),
}

type identifierRule struct {
	prefix   string
	prefixes []string
	letter   bool
	fallback string
}

func identifierPattern(pattern string) *identifierRule {
	switch {
	case identifierPatterns.prefixed.MatchString(pattern):
		return &identifierRule{prefix: identifierPatterns.prefixed.FindStringSubmatch(pattern)[1]}
	case identifierPatterns.references.MatchString(pattern):
		return &identifierRule{prefixes: strings.Split(identifierPatterns.references.FindStringSubmatch(pattern)[1], "|")}
	case identifierPatterns.local.MatchString(pattern):
		return &identifierRule{letter: true}
	case identifierPatterns.category.MatchString(pattern):
		return &identifierRule{fallback: "uncategorized"}
	}
	return nil
}

var identifierSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// identifierToken respells a value as normalization does before it applies a
// rule: lowercase, one underscore for each run of other characters, none at
// either end.
func identifierToken(value string) string {
	return strings.Trim(identifierSeparators.ReplaceAllString(strings.ToLower(value), "_"), "_")
}

// normalizeIdentifier maps any spelling onto the contract's identifier shape:
// lowercase, underscores for punctuation and spaces, one required prefix.
func (rule *identifierRule) normalize(value string) string {
	token := identifierToken(value)
	switch {
	case rule.prefix != "":
		token = strings.TrimPrefix(token, rule.prefix+"_")
		if token == "" {
			token = "1"
		}
		return rule.prefix + "_" + token
	case rule.letter:
		if token == "" || token[0] >= '0' && token[0] <= '9' {
			token = "n" + token
		}
		return token
	case rule.fallback != "" && token == "":
		return rule.fallback
	}
	return token
}

// discriminatedUnion rewrites a union of objects told apart by a constant
// type or kind into one object schema with an enum discriminator and an
// if/then branch per value. A validator then names the failing branch and its
// missing field instead of reporting that no branch matched.
func discriminatedUnion(branches []any, defs map[string]any) map[string]any {
	if len(branches) < 2 {
		return nil
	}
	var discriminator string
	type branchInfo struct {
		values []any
		schema map[string]any
	}
	infos := make([]branchInfo, 0, len(branches))
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok || branch["type"] != "object" {
			return nil
		}
		props, _ := branch["properties"].(map[string]any)
		var values []any
		for _, key := range []string{"type", "kind"} {
			field, _ := props[key].(map[string]any)
			if field == nil {
				continue
			}
			if value, ok := field["const"]; ok {
				values = []any{value}
			} else if enum, ok := field["enum"].([]any); ok {
				values = enum
			}
			if values != nil {
				if discriminator != "" && discriminator != key {
					return nil
				}
				discriminator = key
				break
			}
		}
		if values == nil {
			return nil
		}
		infos = append(infos, branchInfo{values: values, schema: branch})
	}
	// Every branch's fields appear on the union, nullable, so a field of
	// another value never fails the object; each then branch requires only
	// its own fields, in their strict shape.
	variants := map[string][]map[string]any{}
	var names []string
	var values []any
	var guide []string
	var cases []any
	for _, info := range infos {
		values = append(values, info.values...)
		props := info.schema["properties"].(map[string]any)
		var required []any
		for _, name := range stringSlice(info.schema["required"]) {
			prop, _ := props[name].(map[string]any)
			if name == discriminator || prop != nil && defaultable(name, prop, defs) {
				continue
			}
			required = append(required, name)
		}
		then := map[string]any{"properties": map[string]any{}, "required": required}
		for name, raw := range props {
			prop, ok := raw.(map[string]any)
			if !ok || name == discriminator {
				continue
			}
			relaxSchema(prop, defs)
			if containsString(stringSlice(required), name) {
				then["properties"].(map[string]any)[name] = prop
			}
			if _, seen := variants[name]; !seen {
				names = append(names, name)
			}
			distinct := true
			for _, variant := range variants[name] {
				if reflect.DeepEqual(variant, prop) {
					distinct = false
				}
			}
			if distinct {
				variants[name] = append(variants[name], prop)
			}
		}
		labels := make([]string, 0, len(info.values))
		for _, value := range info.values {
			labels = append(labels, fmt.Sprint(value))
		}
		fields := stringSlice(required)
		if len(fields) == 0 {
			fields = []string{"no other fields"}
		}
		guide = append(guide, strings.Join(labels, "/")+": "+strings.Join(fields, ", "))
		cases = append(cases, map[string]any{
			"if":   map[string]any{"properties": map[string]any{discriminator: map[string]any{"enum": info.values}}, "required": []any{discriminator}},
			"then": then,
		})
	}
	properties := map[string]any{discriminator: map[string]any{"type": "string", "enum": values}}
	sort.Strings(names)
	for _, name := range names {
		alternatives := variants[name]
		if len(alternatives) == 1 {
			properties[name] = nullable(alternatives[0])
			continue
		}
		list := make([]any, 0, len(alternatives)+1)
		for _, alternative := range alternatives {
			list = append(list, alternative)
		}
		properties[name] = map[string]any{"anyOf": append(list, map[string]any{"type": "null"})}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             []any{discriminator},
		"allOf":                cases,
		"description":          fmt.Sprintf("Discriminated by %s. Required fields per value — %s. Fields of other values may be omitted or null.", discriminator, strings.Join(guide, "; ")),
	}
}

// nullable returns a copy of prop that also accepts null.
func nullable(prop map[string]any) map[string]any {
	if acceptsNull(prop) {
		return prop
	}
	copied := make(map[string]any, len(prop)+1)
	maps.Copy(copied, prop)
	_, anyOf := copied["anyOf"]
	_, oneOf := copied["oneOf"]
	switch value := copied["type"].(type) {
	case string:
		if !anyOf && !oneOf {
			if enum, ok := copied["enum"].([]any); ok {
				copied["enum"] = append(append([]any{}, enum...), nil)
			}
			copied["type"] = []any{value, "null"}
			return copied
		}
	case []any:
		copied["type"] = append(append([]any{}, value...), "null")
		return copied
	}
	return map[string]any{"anyOf": []any{prop, map[string]any{"type": "null"}}}
}

func acceptsNull(schema map[string]any) bool {
	switch value := schema["type"].(type) {
	case string:
		if value == "null" {
			return true
		}
	case []any:
		for _, t := range value {
			if t == "null" {
				return true
			}
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		for _, v := range enum {
			if v == nil {
				return true
			}
		}
	}
	if _, ok := schema["const"]; ok && schema["const"] == nil {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if branches, ok := schema[key].([]any); ok {
			for _, branch := range branches {
				if child, ok := branch.(map[string]any); ok && acceptsNull(child) {
					return true
				}
			}
		}
	}
	return false
}

// defaultable reports whether a missing property has a canonical value the
// platform supplies: a constant, an empty array, null, or anchors that
// default to the enclosing section's hunks.
func defaultable(name string, prop map[string]any, defs map[string]any) bool {
	_, hasDefault := defaultValue(prop, defs)
	return hasDefault || anchorsProperty(name, prop, defs)
}

func defaultValue(prop map[string]any, defs map[string]any) (any, bool) {
	resolved := resolveSchema(prop, defs)
	if value, ok := resolved["const"]; ok {
		return value, true
	}
	if resolved["type"] == "array" {
		if minimum, _ := resolved["minItems"].(float64); minimum <= 0 {
			return []any{}, true
		}
	}
	if acceptsNull(resolved) {
		return nil, true
	}
	return nil, false
}

// anchorsProperty recognizes the anchor arrays (anchors, and a presentational
// block's source) whose empty value normalizeAuthored fills from the
// enclosing section's hunks.
func anchorsProperty(name string, prop map[string]any, defs map[string]any) bool {
	if name != "anchors" && name != "source" {
		return false
	}
	return resolveSchema(prop, defs)["type"] == "array"
}

// resolveSchema follows a $ref in a schema node through defs and returns the
// node it names, or the node itself when it has no reference.
func resolveSchema(schema map[string]any, defs map[string]any) map[string]any {
	for range 8 {
		ref, ok := schema["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/$defs/") {
			return schema
		}
		target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			return schema
		}
		schema = target
	}
	return schema
}

func stringSlice(value any) []string {
	if strings, ok := value.([]string); ok {
		return strings
	}
	list, _ := value.([]any)
	result := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func removeStrings(value any, removed ...string) []any {
	var kept []any
	for _, item := range stringSlice(value) {
		if !containsString(removed, item) {
			kept = append(kept, item)
		}
	}
	if kept == nil {
		kept = []any{}
	}
	return kept
}

func containsString(list []string, value string) bool { return slices.Contains(list, value) }

// schemaBranch picks the branch of a schema, resolved through defs, that the
// authored value selects, or nil when none does. Callers that walk an authored
// document beside its schema use it to follow the same branch normalisation
// takes.
func schemaBranch(defs map[string]any, value any, schema map[string]any) map[string]any {
	return (&authoredNormalizer{defs: defs}).branch(value, schema)
}
