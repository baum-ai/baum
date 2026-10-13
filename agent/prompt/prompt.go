// Package prompt is the review prompt this module ships: the instructions the
// generate-and-repair loop (agent.Generator) gives a model backend.
//
// The prompt is not written yet. Prompts therefore returns ErrNotAuthored for
// every backend, so a caller fails with a clear error instead of reviewing
// with empty instructions.
//
// The layout it will take once written, embedded with go:embed:
//
//	review-public-v1.md   the review instructions shared by every backend
//	submit-claude.md      how a StructuredOutput backend submits the document
//	submit-codex.md       how a final-message backend returns the document
//
// Instructions will join the shared text with the backend's submit text, set
// Version to the file's version and Digest to the sha256 of the joined Body.
// The repair wording stays in agent.RepairText and the tile guidance in
// tilecatalogue, so neither belongs in these files.
package prompt

import (
	"context"
	"errors"

	"github.com/lastpersonlabs/baum/agent"
)

// ErrNotAuthored is returned for every backend until the public prompt exists.
var ErrNotAuthored = errors.New("the public review prompt is not written yet")

// Prompts implements agent.Prompts with this module's public review prompt.
type Prompts struct{}

var _ agent.Prompts = Prompts{}

// Instructions returns ErrNotAuthored.
func (Prompts) Instructions(context.Context, string) (agent.Prompt, error) {
	return agent.Prompt{}, ErrNotAuthored
}
