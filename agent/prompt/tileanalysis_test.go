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
	agent.BackendClaude:      "5b1ced4a6fbcbeda887e941c9c56d4fbe26d57e3b0020f12b2eb9c5d22e6920a",
	agent.BackendAnthropic:   "5b1ced4a6fbcbeda887e941c9c56d4fbe26d57e3b0020f12b2eb9c5d22e6920a",
	agent.BackendCodex:       "4d1fa526e8fa592d15b0fcd4c7bbccdfb13b98e0c39a8bd962acd6a121e6228d",
	agent.BackendCursorAgent: "fbe4d3a959ed55f0b3fd32cf8adef6f747fbc587752a3e1e4f95f470f75a9efc",
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
