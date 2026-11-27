package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// TileTypeURL is the tile type file schema's $id.
const TileTypeURL = "https://static.usebaum.com/schemas/tile-type/1.json"

// TileTypeName is the value of a tile type file's top-level "schema" field.
const TileTypeName = "baum.tile-type/1"

// TileType is the schema of a tile type file (<name>.baumtile.json), which
// the Tile library exports and imports (internal/tilelibrary).
//
//go:embed tile-type-1.json
var TileType []byte

// CompileTileType returns the tile type file schema resolved as JSON Schema
// draft 2020-12.
func CompileTileType() (*jsonschema.Resolved, error) {
	var root jsonschema.Schema
	if err := json.Unmarshal(TileType, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding tile-type-1.json: %w", err)
	}
	return root.Resolve(nil)
}
