package prompt

import (
	"context"
	_ "embed"

	"github.com/baum-ai/baum/agent"
)

// ViewVersion is the prompt version of the view call. Change it with the
// text of review-view-public-v1.md.
const ViewVersion = "review-view-public-v1"

// reviewView is the view prompt: the system prompt of the single call
// (agent.ViewRun) that writes a review's page view. Like the composition
// prompt, it has no Submitting heading: the backend adds how to submit.
//
//go:embed review-view-public-v1.md
var reviewView string

var _ agent.ViewPrompts = TilesPrompts{}

// ViewInstructions returns the view prompt, the review-view entry's
// override when there is one. It is the same for every backend that
// completes (agent.Completer). Digest is the sha256 of Body.
func (p TilesPrompts) ViewInstructions(_ context.Context, _ string) (agent.Prompt, error) {
	text, custom := p.Overrides.text(IDReviewView, reviewView)
	return composed(withCustom(ViewVersion, custom), text), nil
}
