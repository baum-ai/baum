package prompt

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/agent"
)

// tilesFill opens a library tile's fill, before the type's own
// instructions (review-tiles-fill-public-v1.md, under TilesVersion). It is
// built in and not replaceable yet.
//
//go:embed review-tiles-fill-public-v1.md
var tilesFill string

var _ agent.TileAnalysisPrompts = TilesPrompts{}

// TileAnalysisInstructions returns the fill texts for a backend: the fill
// text and the backend's stage submit block. Digest covers both.
func (p TilesPrompts) TileAnalysisInstructions(_ context.Context, backend string) (agent.TileAnalysisPrompt, error) {
	var submit string
	switch backend {
	case agent.BackendClaude, agent.BackendAnthropic:
		submit = submitStageClaude
	case agent.BackendCodex:
		submit = submitStageCodex
	case agent.BackendCursorAgent:
		submit = submitStageCursor
	default:
		return agent.TileAnalysisPrompt{}, fmt.Errorf("no public tile fill prompt for backend %q", backend)
	}
	out := agent.TileAnalysisPrompt{
		Version: TilesVersion, Fill: tilesFill,
		FillSubmit: stagedSubmitting + "\n" + strings.ReplaceAll(submit, "{part}", "the tile's answer"),
	}
	digest := sha256.New()
	for _, text := range []string{out.Fill, out.FillSubmit} {
		digest.Write([]byte(text))
		digest.Write([]byte{0})
	}
	out.Digest = hex.EncodeToString(digest.Sum(nil))
	return out, nil
}
