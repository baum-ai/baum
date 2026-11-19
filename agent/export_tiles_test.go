package agent

import (
	"github.com/baum-ai/baum/tiles/library"
)

// FillSchemaOf is fillSchema, for the strict schema tests.
func FillSchemaOf(e library.Entry) ([]byte, error) { return fillSchema(e) }

// OutlineSchemaWithLibrary is the outline schema with the library array
// for the entries.
func OutlineSchemaWithLibrary(schema []byte, entries []library.Entry) ([]byte, error) {
	return (&tileAnalysis{config: StagedTiles{Library: entries}}).outlineSchema(schema)
}
