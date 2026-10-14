// Package prompt is the review prompt this module ships: the instructions the
// generate-and-repair loop (agent.Generator) gives a model backend.
//
// Three files are embedded:
//
//	review-public-v1.md   the review instructions shared by every backend,
//	                      ending with the "## 10. Submitting" heading
//	submit-claude.md      how a StructuredOutput backend submits the document
//	submit-codex.md       how a final-message backend returns the document
//
// Instructions joins the shared text with the backend's submit text under the
// trailing heading, sets Version to Version and Digest to the sha256 of the
// joined Body. The repair wording stays in agent.RepairText and the tile
// guidance in tilecatalogue, so neither belongs in these files.
package prompt

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"

	"github.com/baumai/baum/agent"
)

// Version is the prompt version recorded in the document's generator block.
// Change it with the text of review-public-v1.md.
const Version = "review-public-v1"

//go:embed review-public-v1.md
var reviewPublic string

//go:embed submit-claude.md
var submitClaude string

//go:embed submit-codex.md
var submitCodex string

// Prompts implements agent.Prompts with this module's public review prompt.
type Prompts struct{}

var _ agent.Prompts = Prompts{}

// Instructions returns the review prompt for a backend: the shared text with
// the backend's submit block appended. The claude and anthropic backends submit
// through the StructuredOutput tool; codex returns the document as its final
// message. Any other backend is an error.
func (Prompts) Instructions(_ context.Context, backend string) (agent.Prompt, error) {
	var submit string
	switch backend {
	case agent.BackendClaude, agent.BackendAnthropic:
		submit = submitClaude
	case agent.BackendCodex:
		submit = submitCodex
	default:
		return agent.Prompt{}, fmt.Errorf("no public review prompt for backend %q", backend)
	}
	body := reviewPublic + "\n" + submit
	sum := sha256.Sum256([]byte(body))
	return agent.Prompt{Version: Version, Body: body, Digest: hex.EncodeToString(sum[:])}, nil
}
