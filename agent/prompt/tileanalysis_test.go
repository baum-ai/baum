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
	agent.BackendClaude:      "5137bfb5b8b2690d6e9d4cef08634afdb336fa91c2693fabe9d342ddbd5c3907",
	agent.BackendAnthropic:   "5137bfb5b8b2690d6e9d4cef08634afdb336fa91c2693fabe9d342ddbd5c3907",
	agent.BackendCodex:       "9321237d931f8d9e1563841038292dd703182ea04ba7784c92b958a0f0079a13",
	agent.BackendCursorAgent: "03d7cb9a634a5080499260c03d41a5bbb9e3cb30eb71d2db4b93cc7fc2424fad",
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
