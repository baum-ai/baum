package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
)

// The fill texts are pinned: a change to them is deliberate and updates
// these digests.
var tileAnalysisDigests = map[string]string{
	agent.BackendClaude:      "071038d458a4cd30b55dacfc4a623875a9050fd94e10e16d0980f41c6fd410dd",
	agent.BackendAnthropic:   "071038d458a4cd30b55dacfc4a623875a9050fd94e10e16d0980f41c6fd410dd",
	agent.BackendCodex:       "5a3635581522842748cb78000ba21f97410c2c1f8d6f9aef3856dd2bde44f542",
	agent.BackendCursorAgent: "2760db4396de02eb4985518908903d0ba523d9870c59bded004559915c7b8fdd",
}

func TestFillInstructions(t *testing.T) {
	for backend, want := range tileAnalysisDigests {
		p, err := prompt.TilesPrompts{}.TileAnalysisInstructions(context.Background(), backend)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != prompt.TilesVersion || p.Digest != want {
			t.Errorf("%s: %s %s", backend, p.Version, p.Digest)
		}
		if !strings.HasPrefix(p.Fill, "## Fill a library tile\n") || !strings.Contains(p.FillSubmit, "## Submitting\n") {
			t.Errorf("%s: the fill text or its Submitting heading is missing", backend)
		}
	}
	if _, err := (prompt.TilesPrompts{}).TileAnalysisInstructions(context.Background(), "nobody"); err == nil {
		t.Error("an unknown backend has fill texts")
	}
}
