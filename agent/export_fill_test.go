package agent

import (
	"github.com/baum-ai/baum/tiles/library"
)

// FillSchemaOf is fillSchema, for the strict schema tests.
func FillSchemaOf(e library.Entry) ([]byte, error) { return fillSchema(e) }
