package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/review"
)

// ReviewGenerator generates a pull request's review document as the desktop
// app does (generate.Run). It is the only part of the package that knows the
// document's format.
type ReviewGenerator struct {
	// Staged writes the review in stages, as the desktop does.
	Staged bool
	// Reuse keeps the pipeline's reuse of the previous version's section
	// details. By default every section is generated, so a prompt change is
	// measured on all of them. A saved bundle does not hold the images the
	// previous version was offered, so with Reuse the pipeline reuses details
	// only for a bundle that offers no image; the app also reuses them when
	// the images are unchanged.
	Reuse bool
	// PromptOverrides replace staged review prompts by id.
	PromptOverrides prompt.Overrides

	once      sync.Once
	validator *review.DocumentValidator
	err       error
}

func (g *ReviewGenerator) Name() string { return "review" }

func (g *ReviewGenerator) check() error {
	for id, text := range g.PromptOverrides {
		if !g.Staged {
			return fmt.Errorf("prompt override %s needs a staged run: a review written in one pass has no replaceable prompt", id)
		}
		ids := prompt.ReviewIDs(false, true)
		if !slices.Contains(ids, id) {
			return fmt.Errorf("prompt override %q is not a staged review prompt; use one of %v", id, ids)
		}
		if err := prompt.Validate(id, text); err != nil {
			return fmt.Errorf("prompt override %s: %w", id, err)
		}
	}
	return nil
}

func (g *ReviewGenerator) Describe(ctx context.Context, backend string) (map[string]any, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	var version, digest string
	if g.Staged {
		instructions, err := prompt.StagedPrompts{Overrides: g.PromptOverrides}.StagedInstructions(ctx, backend)
		if err != nil {
			return nil, err
		}
		version, digest = instructions.Version, instructions.Digest
	} else {
		instructions, err := prompt.Prompts{}.Instructions(ctx, backend)
		if err != nil {
			return nil, err
		}
		version, digest = instructions.Version, instructions.Digest
	}
	overrides := []map[string]string{}
	ids := make([]prompt.ID, 0, len(g.PromptOverrides))
	for id := range g.PromptOverrides {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		sum := sha256.Sum256([]byte(g.PromptOverrides[id]))
		overrides = append(overrides, map[string]string{"id": string(id), "sha256": hex.EncodeToString(sum[:])})
	}
	return map[string]any{"staged": g.Staged, "reuse": g.Reuse, "prompt_version": version, "prompt_digest": digest, "prompt_overrides": overrides}, nil
}

func (g *ReviewGenerator) validate() (*review.DocumentValidator, error) {
	g.once.Do(func() { g.validator, g.err = review.NewDocumentValidator() })
	return g.validator, g.err
}

func (g *ReviewGenerator) Generate(ctx context.Context, o generate.Options, c Case) (Generated, error) {
	validator, err := g.validate()
	if err != nil {
		return Generated{}, err
	}
	o.Staged, o.NoReuse, o.Validator = g.Staged, !g.Reuse, validator
	if g.Staged {
		o.StagedPrompts = prompt.StagedPrompts{Overrides: g.PromptOverrides}
	}
	result, err := generate.Run(ctx, o)
	out := Generated{Outcome: result.Outcome}
	if err != nil {
		return out, err
	}
	out.Document = result.Document
	out.PromptVersion, out.PromptDigest, out.BundleDigest = result.Metadata.PromptVersion, result.Metadata.PromptDigest, result.Metadata.BundleDigest
	out.Notes = slices.Clone(result.FailedSections)
	if result.ContextFailed {
		out.Notes = append(out.Notes, "context")
	}
	out.Details = map[string]any{"failed_sections": result.FailedSections, "context_failed": result.ContextFailed}
	return out, nil
}

// Validate checks the document as the stored form, against the case's bundle.
func (g *ReviewGenerator) Validate(document []byte, c Case) []string {
	validator, err := g.validate()
	if err != nil {
		return []string{err.Error()}
	}
	b, err := c.FreshBundle()
	if err != nil {
		return []string{err.Error()}
	}
	return validator.ValidateStored(document, &b)
}
