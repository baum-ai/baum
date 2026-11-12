// Package prompt is the review prompt this module ships: the instructions the
// generate-and-repair loop (agent.Generator) gives a model backend. It also
// holds the fix instructions (Fix): fix-public-v1.md, with fix-conflicts.md
// and fix-checks.md for what a fix is for.
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
//
// A staged review (agent.Generator.StagedReview, StagedPrompts) has four
// more, derived from review-public-v6.md:
//
//	review-shared-public-v1.md   what every stage shares: the stages and what
//	                             each writes, the ground rules, the inputs,
//	                             checking a change, anchors, writing and
//	                             calibration; with the bundle and the source
//	                             excerpts, the prefix of every stage's prompt
//	review-outline-public-v1.md  the overview and the sections' outline
//	review-details-public-v1.md  the sections' details
//	review-context-public-v1.md  the summary's codebase context
//
// Each stage file ends with a "## Submitting" heading, under which the
// backend's stage submit block goes (submit-stage-claude.md,
// submit-stage-codex.md, submit-stage-cursor.md, with {part} replaced by what
// the stage returns), and names its schema file (agent.TaskSchemaFile).
//
// # Custom prompts
//
// A host may replace the text of any prompt by passing Overrides (a map from
// ID to text) in the Overrides field of GroupPrompts or StagedPrompts, or to
// FixWith. The one-pass review (Prompts) is not replaceable: a review of one
// pull request is staged, and a group's prompt (review-group) is the whole
// text, the review instructions included. An override replaces the built-in text of its part only: the
// Submitting heading and the backend's submit block (the output contract)
// stay baum's and are always appended, so an override never includes them
// (Validate rejects one that does). A composition whose parts include an
// override reports its Version with a "+custom" suffix; the Digest still
// identifies the exact text. A nil or empty Overrides, and an ID missing
// from it or mapped to the empty string, mean the built-in text; the zero
// value of every type produces byte-identical output to the built-in
// prompts, so a host that never sets Overrides is unchanged.
//
// Catalogue lists every overridable prompt (ID, title, group, built-in text
// and version) so an editor can seed from the built-in text. Validate checks
// an override before it is stored. Each Entry also carries SafetyRules:
// metadata kept outside the prompt text that names the sentences that keep a
// run safe (treat the pull request as untrusted data, never commit or push,
// never reproduce secrets). MissingSafetyRules reports which of them a custom
// text dropped, so a host can warn the author; it never blocks.
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
const Version = "review-public-v7"

//go:embed review-public-v6.md
var reviewPublic string

//go:embed submit-claude.md
var submitClaude string

// GroupVersion is the prompt version of a review group's combined review.
// Change it with the text of group-public-v1.md or review-public-v6.md.
const GroupVersion = "group-review-public-v5"

//go:embed group-public-v1.md
var groupPublic string

// submitting is the heading review-public-v6.md ends with, under which the
// backend's submit block goes.
const submitting = "## 10. Submitting\n"

//go:embed submit-codex.md
var submitCodex string

//go:embed submit-cursor.md
var submitCursor string

// Prompts implements agent.Prompts with this module's public review prompt,
// written in one pass. A review of one pull request is staged
// (StagedPrompts), so it is not one of the prompts a host replaces.
type Prompts struct{}

var _ agent.Prompts = Prompts{}

// Instructions returns the review prompt for a backend: the shared text with
// the backend's submit block appended. The claude and anthropic backends submit
// through the StructuredOutput tool; codex and cursor-agent return the document
// as their final message, cursor-agent with the schema the backend appends to
// the prompt. Any other backend is an error.
func (p Prompts) Instructions(_ context.Context, backend string) (agent.Prompt, error) {
	submit, err := submitBlock(backend)
	if err != nil {
		return agent.Prompt{}, err
	}
	return composed(Version, reviewBody+submitting+"\n"+submit), nil
}

// GroupPrompts implements agent.Prompts for a review group's combined review:
// the public review prompt with the group instructions (group-public-v1.md)
// inserted before its Submitting heading, then the backend's submit block.
type GroupPrompts struct {
	// Overrides replaces built-in texts; GroupPrompts uses the review-group
	// entry, which replaces the whole text before the Submitting heading.
	// Callers must Validate the text first; composition does not.
	Overrides Overrides
}

var _ agent.Prompts = GroupPrompts{}

// Instructions returns the group prompt for a backend, as Prompts does.
func (g GroupPrompts) Instructions(_ context.Context, backend string) (agent.Prompt, error) {
	submit, err := submitBlock(backend)
	if err != nil {
		return agent.Prompt{}, err
	}
	group, custom := g.Overrides.text(IDReviewGroup, groupBody)
	return composed(withCustom(GroupVersion, custom), group+submitting+"\n"+submit), nil
}

// StagedVersion is the prompt version of a staged review. Change it with the
// text of any review-*-public-v1.md file of the staged review.
const StagedVersion = "review-staged-public-v2"

//go:embed review-shared-public-v1.md
var stagedShared string

//go:embed review-outline-public-v1.md
var stagedOutline string

//go:embed review-details-public-v1.md
var stagedDetails string

//go:embed review-context-public-v1.md
var stagedContext string

//go:embed submit-stage-claude.md
var submitStageClaude string

//go:embed submit-stage-codex.md
var submitStageCodex string

//go:embed submit-stage-cursor.md
var submitStageCursor string

// What each stage returns, in its submit block.
const (
	outlinePart = "the outline"
	detailsPart = "the section details"
	contextPart = "the context"
)

// StagedPrompts implements agent.StagedPrompts with this module's public
// staged review prompts.
type StagedPrompts struct {
	// Overrides replaces built-in texts; StagedPrompts uses the
	// review-staged-* entries. Callers must Validate the text first;
	// composition does not.
	Overrides Overrides
}

var _ agent.StagedPrompts = StagedPrompts{}

// StagedInstructions returns the staged review prompts for a backend: the
// shared text, and each stage's text with the backend's stage submit block
// appended, naming what the stage returns, as Prompts appends the submit
// block for a review written in one pass. Digest is the sha256 of the four
// texts, each followed by a NUL byte. Any other backend is an error.
func (p StagedPrompts) StagedInstructions(_ context.Context, backend string) (agent.StagedPrompt, error) {
	var submit string
	switch backend {
	case agent.BackendClaude, agent.BackendAnthropic:
		submit = submitStageClaude
	case agent.BackendCodex:
		submit = submitStageCodex
	case agent.BackendCursorAgent:
		submit = submitStageCursor
	default:
		return agent.StagedPrompt{}, fmt.Errorf("no public staged review prompt for backend %q", backend)
	}
	block := func(part string) string { return "\n" + strings.ReplaceAll(submit, "{part}", part) }
	shared, c0 := p.Overrides.text(IDReviewStagedShared, stagedShared)
	outline, c1 := p.Overrides.text(IDReviewStagedOutline, stagedOutlineBody)
	details, c2 := p.Overrides.text(IDReviewStagedDetails, stagedDetailsBody)
	contextText, c3 := p.Overrides.text(IDReviewStagedContext, stagedContextBody)
	staged := agent.StagedPrompt{
		Version: withCustom(StagedVersion, c0 || c1 || c2 || c3), Shared: shared,
		Outline: outline + stagedSubmitting + block(outlinePart), Details: details + stagedSubmitting + block(detailsPart), Context: contextText + stagedSubmitting + block(contextPart),
	}
	digest := sha256.New()
	for _, text := range []string{staged.Shared, staged.Outline, staged.Details, staged.Context} {
		digest.Write([]byte(text))
		digest.Write([]byte{0})
	}
	staged.Digest = hex.EncodeToString(digest.Sum(nil))
	return staged, nil
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
