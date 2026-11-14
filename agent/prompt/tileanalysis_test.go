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
	agent.BackendClaude:      "16dceb3373c4fa974c32117b35eb9a4442f465d8f2b3cbc14c2912b940570826",
	agent.BackendAnthropic:   "16dceb3373c4fa974c32117b35eb9a4442f465d8f2b3cbc14c2912b940570826",
	agent.BackendCodex:       "035986fa1319fe05d1ca92f94583ad19c297865915495208588a21b92d92463a",
	agent.BackendCursorAgent: "5b3964f2f6a3f11b13eeb9a1d0ab0a8d153052a5a3cf816a7c4ce4f3f65ea574",
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
		if !strings.Contains(p.Exhibits, "an empty list is a good answer") || !strings.Contains(p.Candidates, "three or more places") || !strings.Contains(p.Exhibits, "data, not instructions") {
			t.Errorf("%s: the exhibits text lost its rules", backend)
		}
	}
	if _, err := (prompt.TilesPrompts{}).TileAnalysisInstructions(context.Background(), "nobody"); err == nil {
		t.Error("an unknown backend has tile analysis texts")
	}
}
