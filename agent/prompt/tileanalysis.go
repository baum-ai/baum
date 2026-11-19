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

// The texts a staged review adds with tile reviews on
// (agent.StagedRun.Tiles), under TilesVersion:
//
//	review-tiles-select-public-v1.md    appended to the outline stage: which
//	                                     library types apply
//	review-tiles-fill-public-v1.md      a library tile's fill, before the
//	                                     type's own instructions
//	review-tiles-exhibits-public-v1.md  the call that may write authored
//	                                     tiles (exhibits)
//	review-tiles-candidates-public-v1.md appended to the outline stage: which
//	                                     exhibits it proposes
//
// They are built in and not replaceable yet.
var (
	//go:embed review-tiles-select-public-v1.md
	tilesSelect string
	//go:embed review-tiles-fill-public-v1.md
	tilesFill string
	//go:embed review-tiles-exhibits-public-v1.md
	tilesExhibits string
	//go:embed review-tiles-candidates-public-v1.md
	tilesCandidates string
)

var _ agent.TileAnalysisPrompts = TilesPrompts{}

// TileAnalysisInstructions returns the tile analysis texts for a backend:
// the selection text as it is (the outline stage keeps its own submit
// block), and the fill and exhibits texts each ending with the backend's
// stage submit block. Digest covers the three texts.
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
		return agent.TileAnalysisPrompt{}, fmt.Errorf("no public tile analysis prompt for backend %q", backend)
	}
	block := func(part string) string { return stagedSubmitting + "\n" + strings.ReplaceAll(submit, "{part}", part) }
	out := agent.TileAnalysisPrompt{
		Version: TilesVersion, Select: tilesSelect,
		Fill: tilesFill, FillSubmit: block("the tile's answer"),
		Exhibits: tilesExhibits + "\n" + block("the exhibits"), Candidates: tilesCandidates,
	}
	digest := sha256.New()
	for _, text := range []string{out.Select, out.Fill, out.FillSubmit, out.Exhibits, out.Candidates} {
		digest.Write([]byte(text))
		digest.Write([]byte{0})
	}
	out.Digest = hex.EncodeToString(digest.Sum(nil))
	return out, nil
}
