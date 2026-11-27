package agent_test

import (
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/tiles/library"
)

// The fills' schemas pass Codex's strict conversion.
func TestFillStrictSchemas(t *testing.T) {
	for _, e := range library.All() {
		schema, err := agent.FillSchemaOf(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := codex.StrictSchema(schema); err != nil {
			t.Errorf("%s fill schema: %v", e.Definition.Name, err)
		}
	}
}
