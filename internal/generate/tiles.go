package generate

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
)

// Tiles, for a staged run of one pull request (Options.Tiles), arranges the
// review's tiles once its analysis has ended: one call through the backend's
// agent.Completer (agent.ComposeRun) chooses the layout, which is stored with
// the version (Result.Composition). The review itself is published exactly as
// without it: the composition never fails a run, and a composition that
// cannot be made leaves the version with the template's layout or none.
type Tiles struct {
	// Prompts defaults to prompt.TilesPrompts{}.
	Prompts agent.ComposePrompts
	// Previous is the previous version's stored composition
	// (store.ParseComposition), nil without one. Publish sets it from the
	// latest version.
	Previous *tiles.Composition
	// Preferences are the repository's text and pins.
	Preferences compose.Preferences
}

// ComposeReport is what a run's compose stage did (Result.Compose).
type ComposeReport struct {
	// Outcome is what the stage spent; it is already in Result.Outcome.
	Outcome agent.Outcome
	// Fallback says the composition is the template's layout, and
	// FallbackReason says why (agent.Fallback*), the host's own words.
	Fallback       bool
	FallbackReason string
	Repaired       bool
	// Fixes counts what normalising the composer's layout fixed, by kind.
	Fixes map[tiles.FixKind]int
	// Inventory is the composer's user message and Reply its last reply,
	// masked; empty when no call was made.
	Inventory string
	Reply     []byte
	// Provisional is the layout drafts showed while the review was written
	// (provisional), for the finished review.
	Provisional json.RawMessage
	// Elapsed is the stage's wall time.
	Elapsed time.Duration
	// Problem, when set, is why the version has no composition at all.
	Problem string
}

// composeMinTokens is the least of a token budget left in which the composer
// is called: an inventory and a layout take a few thousand tokens.
const composeMinTokens = 15000

// composeCalls is how many calls the run's budgets leave the composer: two
// (one repair) without a call budget, else what is left, at most two. Zero
// skips it, as does a token budget with less than composeMinTokens left.
func composeCalls(b agent.Budgets, spent agent.Outcome) int {
	if b.MaxTokens > 0 && b.MaxTokens-spent.Tokens < composeMinTokens {
		return 0
	}
	if b.MaxCalls > 0 {
		return min(max(b.MaxCalls-spent.Calls, 0), 2)
	}
	return 2
}

// converted is the tile document of a completed review, as the store
// converts it to normalise a composition (tiles.FromReview of the normalised
// stored form).
func converted(document []byte, draft bool) (*tiles.Document, error) {
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return nil, err
	}
	return tiles.FromReview(normalized, tiles.Options{Draft: draft})
}

// provisional is the layout of a draft, or of a review the composer has not
// arranged: the previous version's composition renormalised against it, when
// there is one that applies, so a new version's page keeps the layout the
// reader knows; else the change kind's template with the pins. It is nil
// when the document does not convert, or when making it fails in any way, a
// panic included: the draft is then shown in the converted layout.
func provisional(document []byte, pins []string, previous *tiles.Composition) (out []byte) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	doc, err := converted(document, true)
	if err != nil {
		return nil
	}
	c, err := provisionalLayout(doc, pins, previous)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return encoded
}

// provisionalLayout is provisional's layout of a converted draft; tests
// replace it.
var provisionalLayout = func(doc *tiles.Document, pins []string, previous *tiles.Composition) (tiles.Composition, error) {
	if previous != nil && !previous.Empty() {
		kept := *previous
		kept.Generator, kept.Layout.Hidden = nil, nil
		kept.Layout.Entries = slices.Clone(previous.Layout.Entries)
		if n, report := tiles.NormalizeComposition(doc, &kept); report.Counts()[tiles.FixUnusable] == 0 && !n.Empty() {
			return n, nil
		}
	}
	c, _, err := agent.TemplateComposition(doc, pins)
	return c, err
}

// detached is ctx without its deadline: it is cancelled when ctx is
// cancelled, but not when ctx's deadline passes, so the stage it bounds keeps
// its own time limit.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	inner, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel(context.Cause(ctx))
		}
	})
	return inner, func() {
		stop()
		cancel(nil)
	}
}

// composeTiles arranges the tiles of the completed review document: the
// composition to store and its generator, or none, and what the stage did.
// It runs after the analysis, which had the run's whole wall clock and
// budgets, on a context that follows ctx's cancellation but not its
// deadline, bounded by agent.DefaultComposeTimeout: the run may end up to
// that much after its wall clock. It makes at most the calls the run's
// budgets leave (composeCalls), and none, the template applying, when they
// leave none. Only a cancelled ctx is an error (agent.ErrComposeInterrupted):
// the run is stopped and stores nothing. Any other failure, a panic
// included, leaves the version without a composition and says why in the
// report.
func composeTiles(ctx context.Context, o Options, document []byte, spent agent.Outcome, redactor *bundle.Redactor) (composition []byte, generator *tiles.Generator, report *ComposeReport, err error) {
	started := time.Now()
	report = &ComposeReport{}
	defer func() {
		report.Elapsed = time.Since(started)
		if r := recover(); r != nil {
			composition, generator, err = nil, nil, nil
			report.Problem = "arranging the tiles failed"
		}
	}()
	t := o.Tiles
	doc, convertErr := converted(document, false)
	if convertErr != nil {
		report.Problem = "the review does not convert to tiles"
		return nil, nil, report, nil
	}
	report.Provisional = provisional(document, t.Preferences.Pins, t.Previous)
	prompts := t.Prompts
	if prompts == nil {
		prompts = prompt.TilesPrompts{}
	}
	request := agent.ComposeRequest{Completer: CompleterOf(o.Backend), Backend: o.Backend.Name(), Prompts: prompts, Document: doc,
		Previous: t.Previous, Preferences: t.Preferences, Redactor: redactor, CallTimeout: o.PassTimeout,
		Timeout: agent.DefaultComposeTimeout, MaxCalls: composeCalls(o.Budgets, spent)}
	if request.MaxCalls == 0 {
		request.Skip = agent.FallbackBudget
	}
	composeCtx, cancel := detached(ctx)
	defer cancel()
	out, err := composeRun(composeCtx, request)
	report.Outcome = out.Outcome
	report.Fallback, report.FallbackReason, report.Repaired = out.Fallback, out.FallbackReason, out.Repaired
	report.Inventory, report.Reply = out.Inventory, out.Reply
	if errors.Is(err, agent.ErrComposeInterrupted) {
		return nil, nil, report, err
	}
	if err != nil {
		report.Problem = "the tiles could not be arranged"
		return nil, nil, report, nil
	}
	if g := out.Composition.Generator; g != nil {
		report.Fixes = g.Fixes
		copied := *g
		generator = &copied
	}
	if composition, err = json.Marshal(out.Composition); err != nil {
		report.Problem = "the composition could not be encoded"
		return nil, nil, report, nil
	}
	return composition, generator, report, nil
}

// composeRun is agent.ComposeRun; tests replace it.
var composeRun = agent.ComposeRun
