package schema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// TilesURL is the tile document schema's $id.
const TilesURL = "https://static.usebaum.com/schemas/tiles/1.json"

// TilesName is the value of a tile document's top-level "schema" field.
const TilesName = "baum.tiles/1"

// Tiles is the tile document schema as written. A $ref it does not define
// names a definition of Document, or one of the parts of Document that
// MergedTiles copies into $defs (see reviewParts); use MergedTiles or
// CompileTiles rather than resolving it alone.
//
//go:embed tiles-1.json
var Tiles []byte

// reviewParts are the parts of the review schema that are not definitions of
// their own, which the tile schema reuses under these fixed names. A block
// part is the branch of $defs/block whose kind is the given one. A rules part
// is only the conditional keywords (allOf, if, then, else) of its part: the
// tile schema's risk, evidence and decision items list the review item's
// properties, adding their own, and apply its rules through it.
var reviewParts = []struct {
	name    string
	path    []string
	block   string
	subpath []string
	rules   bool
}{
	{name: "reviewHeader", path: []string{"properties", "review"}},
	{name: "reviewSummary", path: []string{"properties", "summary"}},
	{name: "reviewStats", path: []string{"properties", "stats"}},
	{name: "reviewIndex", path: []string{"properties", "index"}},
	{name: "reviewAgentReviews", path: []string{"properties", "agent_reviews"}},
	{name: "reviewStack", path: []string{"properties", "stack"}},
	{name: "reviewRisk", path: []string{"$defs", "section", "properties", "risks", "items"}},
	{name: "reviewEvidence", path: []string{"$defs", "section", "properties", "evidence", "items"}},
	{name: "reviewRiskRules", path: []string{"$defs", "section", "properties", "risks", "items"}, rules: true},
	{name: "reviewEvidenceRules", path: []string{"$defs", "section", "properties", "evidence", "items"}, rules: true},
	{name: "reviewDecisionRules", block: "decisions", subpath: []string{"properties", "items", "items"}, rules: true},
	{name: "reviewIntentBlock", block: "intent"},
	{name: "reviewDecision", block: "decisions", subpath: []string{"properties", "items", "items"}},
	{name: "reviewImagesBlock", block: "images"},
	{name: "reviewMediaBlock", block: "media"},
	{name: "reviewTableBlock", block: "table"},
	{name: "reviewDiagramBlock", block: "diagram"},
	{name: "reviewCodeBlock", block: "code"},
}

// MergedTiles returns the tile schema with the review schema's definitions,
// and the review parts it reuses, merged into its $defs, so every reference
// resolves within one document. A name defined by both is an error: the tile
// schema must not shadow a review definition.
func MergedTiles() ([]byte, error) {
	merged, err := mergedTiles()
	return bytes.Clone(merged), err
}

var mergedTiles = sync.OnceValues(func() ([]byte, error) {
	return mergeTiles(Tiles, Document)
})

func mergeTiles(tiles, review []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(tiles, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding tiles-1.json: %w", err)
	}
	var reviewRoot map[string]any
	if err := json.Unmarshal(review, &reviewRoot); err != nil {
		return nil, fmt.Errorf("schema: decoding review-1.json: %w", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		return nil, fmt.Errorf("schema: tiles-1.json has no $defs")
	}
	add := func(name string, definition any) error {
		if definition == nil {
			return fmt.Errorf("schema: review-1.json has no part for $defs/%s", name)
		}
		if _, ok := defs[name]; ok {
			return fmt.Errorf("schema: $defs/%s is defined by both tiles-1.json and review-1.json", name)
		}
		defs[name] = definition
		return nil
	}
	reviewDefs, _ := reviewRoot["$defs"].(map[string]any)
	for name, definition := range reviewDefs {
		if err := add(name, definition); err != nil {
			return nil, err
		}
	}
	for _, part := range reviewParts {
		var definition any
		if part.block != "" {
			definition = lookup(blockBranch(reviewDefs, part.block), part.subpath)
		} else {
			definition = lookup(reviewRoot, part.path)
		}
		if part.rules {
			definition = conditionalRules(definition)
		}
		if err := add(part.name, definition); err != nil {
			return nil, err
		}
	}
	return json.Marshal(root)
}

// conditionalRules returns the conditional keywords of a schema object.
func conditionalRules(definition any) any {
	object, ok := definition.(map[string]any)
	if !ok {
		return nil
	}
	rules := map[string]any{}
	for _, key := range []string{"allOf", "if", "then", "else"} {
		if value, ok := object[key]; ok {
			rules[key] = value
		}
	}
	return rules
}

// blockBranch returns the branch of the review schema's $defs/block whose
// kind is the given one.
func blockBranch(defs map[string]any, kind string) map[string]any {
	block, _ := defs["block"].(map[string]any)
	branches, _ := block["oneOf"].([]any)
	for _, raw := range branches {
		branch, _ := raw.(map[string]any)
		if lookup(branch, []string{"properties", "kind", "const"}) == kind {
			return branch
		}
	}
	return nil
}

func lookup(value map[string]any, path []string) any {
	var current any = value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	if object, ok := current.(map[string]any); ok && object == nil {
		return nil
	}
	return current
}

// CompileTiles returns the merged tile schema (MergedTiles) resolved as JSON
// Schema draft 2020-12. It validates a whole tile document; the data of each
// tile has the shape of $defs/tileData_<type>, which CompileTilesDefinition
// resolves. The result is safe for concurrent use.
func CompileTiles() (*jsonschema.Resolved, error) {
	merged, err := MergedTiles()
	if err != nil {
		return nil, err
	}
	var root jsonschema.Schema
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged tile schema: %w", err)
	}
	resolved, err := root.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema: resolving the tile schema: %w", err)
	}
	return resolved, nil
}

// CompileTilesDefinition resolves one definition of the merged tile schema,
// such as tileData_change, on its own.
func CompileTilesDefinition(name string) (*jsonschema.Resolved, error) {
	merged, err := MergedTiles()
	if err != nil {
		return nil, err
	}
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged tile schema: %w", err)
	}
	if root.Defs[name] == nil {
		return nil, fmt.Errorf("schema: the tile schema has no $defs/%s", name)
	}
	encoded, err := json.Marshal(map[string]any{"$ref": "#/$defs/" + name, "$defs": root.Defs})
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, fmt.Errorf("schema: decoding $defs/%s: %w", name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema: resolving $defs/%s: %w", name, err)
	}
	return resolved, nil
}
