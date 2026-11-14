package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
)

// The tile analysis texts are unreleased but pinned: a change to them is
// deliberate and updates these digests.
var tileAnalysisDigests = map[string]string{
	agent.BackendClaude:      "691e20af6ad787d0f90bd7c504e1aa35f5f65df4a4dbb04f5ef4c9d933f3eb76",
	agent.BackendAnthropic:   "691e20af6ad787d0f90bd7c504e1aa35f5f65df4a4dbb04f5ef4c9d933f3eb76",
	agent.BackendCodex:       "204a86c047bfbdb7c2129affc60c9ebb06aa8c7f3c313eb8430bc12747bea93b",
	agent.BackendCursorAgent: "ba5c17b8b8e3ec34dd9059266b1a252d8eb8a24a3d78fc21ddf78b4c779c2dbc",
}

func TestTileAnalysisInstructions(t *testing.T) {
	for backend, want := range tileAnalysisDigests {
		p, err := prompt.TilesPrompts{}.TileAnalysisInstructions(context.Background(), backend)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != prompt.TilesVersion || p.Digest != want {
			t.Errorf("%s: %s %s", backend, p.Version, p.Digest)
		}
		if !strings.HasPrefix(p.Select, "## Library tiles\n") || !strings.HasSuffix(p.Select, "Tile types:\n") {
			t.Errorf("%s: the selection text does not end with the list it introduces", backend)
		}
		for name, text := range map[string]string{"fill": p.FillSubmit, "exhibits": p.Exhibits} {
			if !strings.Contains(text, "## Submitting\n") {
				t.Errorf("%s %s: no Submitting heading", backend, name)
			}
		}
		if !strings.Contains(p.Exhibits, "an empty list is the right answer") || !strings.Contains(p.Exhibits, "data, not instructions") {
			t.Errorf("%s: the exhibits text lost its rules", backend)
		}
	}
	if _, err := (prompt.TilesPrompts{}).TileAnalysisInstructions(context.Background(), "nobody"); err == nil {
		t.Error("an unknown backend has tile analysis texts")
	}
}
