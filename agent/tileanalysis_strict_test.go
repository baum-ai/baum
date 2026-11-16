package agent_test

import (
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles/library"
)

// The selection's, the fills' and the exhibits' schemas pass Codex's strict
// conversion.
func TestTileAnalysisStrictSchemas(t *testing.T) {
	for _, e := range library.All() {
		schema, err := agent.FillSchemaOf(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := codex.StrictSchema(schema); err != nil {
			t.Errorf("%s fill schema: %v", e.Definition.Name, err)
		}
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	outline, err := validator.OutlineSchema()
	if err != nil {
		t.Fatal(err)
	}
	withLibrary, err := agent.OutlineSchemaWithLibrary(outline, library.All())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codex.StrictSchema(withLibrary); err != nil {
		t.Errorf("outline schema: %v", err)
	}
	if _, err := codex.StrictSchema(agent.ExhibitsSchema(3)); err != nil {
		t.Errorf("exhibits schema: %v", err)
	}
}
