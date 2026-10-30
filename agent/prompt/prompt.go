// Package prompt is the review prompt this module ships: the instructions the
// generate-and-repair loop (agent.Generator) gives a model backend. It also
// holds the fix instructions (Fix): fix-public-v1.md, with fix-conflicts.md
// and fix-checks.md for what a fix is for, and the instructions of a chat turn
// (Chat): chat-public-v1.md.
//
// Five review files are embedded:
//
//	review-public-v6.md   the review instructions shared by every backend,
//	                      ending with the "## 10. Submitting" heading
//	group-public-v1.md    what changes for a review group's combined
//	                      review, inserted before that heading by
//	                      GroupPrompts
//	submit-claude.md      how a StructuredOutput backend submits the document
//	submit-codex.md       how a final-message backend returns the document
//	submit-cursor.md      the same for cursor-agent, which has no schema
//	                      enforcement and stops a run on any tool that does
//	                      not read
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
	"strings"

	"github.com/baum-ai/baum/agent"
)

// Version is the prompt version recorded in the document's generator block.
// Change it with the text of review-public-v6.md.
const Version = "review-public-v6"

//go:embed review-public-v6.md
var reviewPublic string

//go:embed submit-claude.md
var submitClaude string

// GroupVersion is the prompt version of a review group's combined review.
// Change it with the text of group-public-v1.md or review-public-v6.md.
const GroupVersion = "group-review-public-v3"

//go:embed group-public-v1.md
var groupPublic string

// submitting is the heading review-public-v6.md ends with, under which the
// backend's submit block goes.
const submitting = "## 10. Submitting\n"

//go:embed submit-codex.md
var submitCodex string

//go:embed submit-cursor.md
var submitCursor string

// Prompts implements agent.Prompts with this module's public review prompt.
type Prompts struct{}

var _ agent.Prompts = Prompts{}

// Instructions returns the review prompt for a backend: the shared text with
// the backend's submit block appended. The claude and anthropic backends submit
// through the StructuredOutput tool; codex and cursor-agent return the document
// as their final message, cursor-agent with the schema the backend appends to
// the prompt. Any other backend is an error.
func (Prompts) Instructions(_ context.Context, backend string) (agent.Prompt, error) {
	submit, err := submitBlock(backend)
	if err != nil {
		return agent.Prompt{}, err
	}
	return composed(Version, reviewPublic+"\n"+submit), nil
}

// GroupPrompts implements agent.Prompts for a review group's combined review:
// the public review prompt with the group instructions (group-public-v1.md)
// inserted before its Submitting heading, then the backend's submit block.
type GroupPrompts struct{}

var _ agent.Prompts = GroupPrompts{}

// Instructions returns the group prompt for a backend, as Prompts does.
func (GroupPrompts) Instructions(_ context.Context, backend string) (agent.Prompt, error) {
	submit, err := submitBlock(backend)
	if err != nil {
		return agent.Prompt{}, err
	}
	shared := strings.TrimSuffix(reviewPublic, submitting)
	return composed(GroupVersion, shared+groupPublic+submitting+"\n"+submit), nil
}

func submitBlock(backend string) (string, error) {
	switch backend {
	case agent.BackendClaude, agent.BackendAnthropic:
		return submitClaude, nil
	case agent.BackendCodex:
		return submitCodex, nil
	case agent.BackendCursorAgent:
		return submitCursor, nil
	default:
		return "", fmt.Errorf("no public review prompt for backend %q", backend)
	}
}

func composed(version, body string) agent.Prompt {
	sum := sha256.Sum256([]byte(body))
	return agent.Prompt{Version: version, Body: body, Digest: hex.EncodeToString(sum[:])}
}
