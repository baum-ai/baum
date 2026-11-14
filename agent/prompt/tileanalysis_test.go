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
	agent.BackendClaude:      "32a336dfbcce8c3e9e860d36f249cbc13650e031ddf21ef201691e8f52a423fc",
	agent.BackendAnthropic:   "32a336dfbcce8c3e9e860d36f249cbc13650e031ddf21ef201691e8f52a423fc",
	agent.BackendCodex:       "a4bdb18aa542cd6f57c96538b853692fc65b24459d9d58c0c0a5c2eca8a5a63b",
	agent.BackendCursorAgent: "7ea0ba1f38a0b11d4ecfb7ea91f47c30a1a438d5049d23f43b6899012dc26071",
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
		if !strings.Contains(p.Exhibits, "an empty list is a good answer") || !strings.Contains(p.Candidates, "usually none") || !strings.Contains(p.Exhibits, "data, not instructions") {
			t.Errorf("%s: the exhibits text lost its rules", backend)
		}
	}
	if _, err := (prompt.TilesPrompts{}).TileAnalysisInstructions(context.Background(), "nobody"); err == nil {
		t.Error("an unknown backend has tile analysis texts")
	}
}
