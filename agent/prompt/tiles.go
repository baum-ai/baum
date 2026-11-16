package prompt

import (
	"context"
	_ "embed"

	"github.com/baum-ai/baum/agent"
)

// TilesVersion is the prompt version of a tile review's prompts. Change it
// with the text of review-tiles-compose-public-v1.md.
const TilesVersion = "review-tiles-public-v1"

// tilesCompose is the composition prompt: the system prompt of the single
// call (agent.ComposeRun) that arranges a review's tiles from an inventory.
// It has no Submitting heading: the call goes through agent.Completer, whose
// backends add how to submit.
//
//go:embed review-tiles-compose-public-v1.md
var tilesCompose string

// TilesPrompts implements agent.ComposePrompts with this module's public
// tile review prompts.
type TilesPrompts struct {
	// Overrides replaces built-in texts; TilesPrompts uses the
	// review-tiles-compose entry. Callers must Validate the text first;
	// composition does not.
	Overrides Overrides
}

var _ agent.ComposePrompts = TilesPrompts{}

// ComposeInstructions returns the composition prompt. It is the same for
// every backend that completes (agent.Completer): the backend adds how to
// submit. Digest is the sha256 of Body.
func (p TilesPrompts) ComposeInstructions(_ context.Context, _ string) (agent.Prompt, error) {
	text, custom := p.Overrides.text(IDReviewTilesCompose, tilesCompose)
	return composed(withCustom(TilesVersion, custom), text), nil
}
