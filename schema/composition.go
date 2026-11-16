package schema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// CompositionURL is the composition schema's $id.
const CompositionURL = "https://static.usebaum.com/schemas/composition/1.json"

// CompositionName is the value of a composition's top-level "schema" field.
const CompositionName = "baum.composition/1"

// Composition is the schema of a stored composition: how a generated review
// is presented as tiles. A $ref it does not define names a definition of the
// merged tile schema (MergedTiles); use MergedComposition or
// CompileComposition rather than resolving it alone.
//
//go:embed composition-1.json
var Composition []byte

// MergedComposition returns the composition schema with the merged tile
// schema's definitions merged into its $defs. A name defined by both is an
// error.
func MergedComposition() ([]byte, error) {
	merged, err := mergedComposition()
	return bytes.Clone(merged), err
}

var mergedComposition = sync.OnceValues(func() ([]byte, error) {
	tiles, err := mergedTiles()
	if err != nil {
		return nil, err
	}
	return mergeComposition(Composition, tiles)
})

func mergeComposition(composition, tiles []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(composition, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding composition-1.json: %w", err)
	}
	var tilesRoot struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(tiles, &tilesRoot); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged tile schema: %w", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		return nil, fmt.Errorf("schema: composition-1.json has no $defs")
	}
	for name, definition := range tilesRoot.Defs {
		if _, ok := defs[name]; ok {
			return nil, fmt.Errorf("schema: $defs/%s is defined by both composition-1.json and the tile schema", name)
		}
		defs[name] = definition
	}
	return json.Marshal(root)
}

// CompileComposition returns the merged composition schema
// (MergedComposition) resolved as JSON Schema draft 2020-12. The result is
// safe for concurrent use.
func CompileComposition() (*jsonschema.Resolved, error) {
	merged, err := MergedComposition()
	if err != nil {
		return nil, err
	}
	var root jsonschema.Schema
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged composition schema: %w", err)
	}
	resolved, err := root.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema: resolving the composition schema: %w", err)
	}
	return resolved, nil
}
