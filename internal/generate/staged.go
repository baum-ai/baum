package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/sessions"
)

// continuing is the staged run that completes a saved version (Options.
// Continue): its outline and context, and the version itself, refitted to
// the diff just assembled, for the details it reuses. The pull request must
// still be at the version's head, base and merge base.
func continuing(saved store.Version, assembled assemble.Result) (agent.StagedRun, error) {
	m := saved.Metadata
	if assembled.Head != m.Head || assembled.Base != m.Base || assembled.MergeBase != m.MergeBase {
		return agent.StagedRun{}, fmt.Errorf("the pull request moved since version %d was generated; generate a new version", m.Version)
	}
	outline, context, err := review.StoredOutline(saved.Document)
	if err != nil {
		return agent.StagedRun{}, fmt.Errorf("version %d: %w", m.Version, err)
	}
	reuse, err := review.PreviousVersionOf(saved.Document, m.Version)
	if err != nil {
		return agent.StagedRun{}, fmt.Errorf("version %d: %w", m.Version, err)
	}
	// Its anchors are fitted to the diff as a previous version's are.
	fitted := assembled.Bundle
	fitted.Previous = reuse
	if err := review.FitPreviousHunkAnchors(&fitted); err != nil {
		return agent.StagedRun{}, fmt.Errorf("version %d: %w", m.Version, err)
	}
	return agent.StagedRun{Outline: outline, Context: context, Reuse: reuse}, nil
}

// drafts starts the goroutine that turns a staged run's drafts into what
// OnDraft receives, through prepare (masking and completion; nil skips the
// draft), and returns the run's Progress and the function that waits for the
// last draft. A draft that lands while the previous one is being prepared
// replaces any still waiting, so the stages never wait on it.
func drafts(ctx context.Context, deliver func([]byte), prepare func([]byte) []byte) (func(agent.StagedProgress), func()) {
	waiting := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for draft := range waiting {
			if ctx.Err() != nil {
				continue
			}
			if prepared := prepare(draft); prepared != nil {
				deliver(prepared)
			}
		}
	}()
	progress := func(p agent.StagedProgress) {
		// Progress calls come one at a time, so only this call sends.
		for {
			select {
			case waiting <- p.Document:
				return
			default:
				select {
				case <-waiting:
				default:
				}
			}
		}
	}
	return progress, func() {
		close(waiting)
		<-done
	}
}

// Gaps names the sections of a completed staged document published
// without their details, and reports whether it has no context.
func Gaps(document []byte) ([]string, bool) {
	var doc struct {
		Summary struct {
			Context json.RawMessage `json:"context"`
		} `json:"summary"`
		Sections []struct {
			ID      string `json:"id"`
			Details string `json:"details"`
		} `json:"sections"`
	}
	if json.Unmarshal(document, &doc) != nil {
		return nil, false
	}
	var failed []string
	for _, section := range doc.Sections {
		if section.Details == "failed" {
			failed = append(failed, section.ID)
		}
	}
	return failed, len(doc.Summary.Context) == 0 || string(doc.Summary.Context) == "null"
}

// markedStagedPrompts is markedPrompts for a staged review: the shared text,
// which starts every stage's prompt, starts with the marker.
type markedStagedPrompts struct{ agent.StagedPrompts }

func (p markedStagedPrompts) StagedInstructions(ctx context.Context, backend string) (agent.StagedPrompt, error) {
	staged, err := p.StagedPrompts.StagedInstructions(ctx, backend)
	if err != nil {
		return staged, err
	}
	staged.Shared = sessions.GenerationRunMarker + "\n" + staged.Shared
	// Digest stays that of the four texts sent, as prompt.StagedPrompts
	// computes it: each followed by a NUL byte.
	digest := sha256.New()
	for _, text := range []string{staged.Shared, staged.Outline, staged.Details, staged.Context} {
		digest.Write([]byte(text))
		digest.Write([]byte{0})
	}
	staged.Digest = hex.EncodeToString(digest.Sum(nil))
	return staged, nil
}
