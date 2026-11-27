package schema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// ViewCompositionURL is the review view composition schema's $id.
const ViewCompositionURL = "https://static.usebaum.com/schemas/composition/2.json"

// ViewCompositionName is the value of a review view composition's top-level
// "schema" field. It replaced baum.composition/1 (the tile grid's layout),
// which is no longer read.
const ViewCompositionName = "baum.composition/2"

// ViewComposition is the schema of a stored review view composition: the
// overview's model-written parts, the tiles placed on each section and the
// tiles themselves. A $ref it does not define names a definition of the
// merged tile schema (MergedTiles); use CompileViewComposition. Only
// versions stored before baum.review/2 carry one; in Go, only tests compile
// the schema.
//
//go:embed composition-2.json
var ViewComposition []byte

var mergedViewComposition = sync.OnceValues(func() ([]byte, error) {
	tiles, err := mergedTiles()
	if err != nil {
		return nil, err
	}
	return mergeSchema("composition-2.json", ViewComposition, tiles)
})

func mergeSchema(file string, composition, tiles []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(composition, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding %s: %w", file, err)
	}
	var tilesRoot struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(tiles, &tilesRoot); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged tile schema: %w", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		return nil, fmt.Errorf("schema: %s has no $defs", file)
	}
	for name, definition := range tilesRoot.Defs {
		if _, ok := defs[name]; ok {
			return nil, fmt.Errorf("schema: $defs/%s is defined by both %s and the tile schema", name, file)
		}
		defs[name] = definition
	}
	return json.Marshal(root)
}

// CompileViewComposition returns the review view composition schema, with
// the merged tile schema's definitions, resolved as JSON Schema draft
// 2020-12. The result is safe for concurrent use.
func CompileViewComposition() (*jsonschema.Resolved, error) {
	merged, err := mergedViewComposition()
	if err != nil {
		return nil, err
	}
	return compileMerged(bytes.Clone(merged))
}

func compileMerged(merged []byte) (*jsonschema.Resolved, error) {
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
