package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Provisional is the layout drafts showed while the review was written:
	// the template's (agent.TemplateComposition) for the finished review.
	Provisional json.RawMessage
	// Elapsed is the stage's wall time.
	Elapsed time.Duration
	// Problem, when set, is why the version has no composition at all.
	Problem string
}

// The compose stage's share of a run's limits. When the run has a wall clock,
// the analysis (outline, details and context) ends ComposeReserve before it,
// at most a quarter of it, and the composition has what is left; with less
// than composeMinimum left it is skipped and the template applies. When call
// or token budgets are set, the analysis leaves two calls and 60,000 tokens to
// it, if they are large enough to spare them.
const (
	ComposeReserve       = 75 * time.Second
	ComposeMinimum       = 60 * time.Second
	composeReserveCalls  = 2
	composeReserveTokens = 60000
)

// composeReserve is the time the analysis leaves the composition.
func composeReserve(wallClock time.Duration) time.Duration {
	if wallClock <= 0 {
		return 0
	}
	return min(ComposeReserve, wallClock/4)
}

// composeMinimum is the least time left in which the composer is called.
func composeMinimum(wallClock time.Duration) time.Duration {
	if wallClock <= 0 {
		return ComposeMinimum
	}
	return min(ComposeMinimum, composeReserve(wallClock)*4/5)
}

// analysisBudgets are the budgets the analysis of a run with Tiles has.
func analysisBudgets(b agent.Budgets) agent.Budgets {
	if b.MaxCalls > 2*composeReserveCalls {
		b.MaxCalls -= composeReserveCalls
	}
	if b.MaxTokens > 2*composeReserveTokens {
		b.MaxTokens -= composeReserveTokens
	}
	return b
}

// budgetLeft reports whether the run's budgets leave the composition room
// after the analysis spent spent.
func budgetLeft(b agent.Budgets, spent agent.Outcome) bool {
	if b.MaxCalls > 0 && b.MaxCalls-spent.Calls < 1 {
		return false
	}
	if b.MaxTokens > 0 && b.MaxTokens-spent.Tokens < composeReserveTokens/2 {
		return false
	}
	return true
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
// arranged: the change kind's template with the pins, encoded, or nil when
// the document does not convert.
func provisional(document []byte, pins []string) []byte {
	doc, err := converted(document, true)
	if err != nil {
		return nil
	}
	c, _, err := agent.TemplateComposition(doc, pins)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return encoded
}

// detached is ctx without its deadline: it ends when ctx is cancelled, but
// the stage it bounds keeps its own time limit.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	inner, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	return inner, func() {
		stop()
		cancel(nil)
	}
}

// composeTiles arranges the tiles of the completed review document: the
// composition to store and its generator, or none, and what the stage did.
// It is bounded by deadline (zero for none), at most
// agent.DefaultComposeTimeout, and skipped, to the template, when less than
// composeMinimum is left or the budgets are spent. Only a cancelled ctx is an
// error (agent.ErrComposeInterrupted): the run is stopped and stores nothing.
// Any other failure, a panic included, leaves the version without a
// composition and says why in the report.
func composeTiles(ctx context.Context, o Options, deadline time.Time, document []byte, spent agent.Outcome, redactor *bundle.Redactor) (composition []byte, generator *tiles.Generator, report *ComposeReport, err error) {
	started := time.Now()
	report = &ComposeReport{}
	defer func() { report.Elapsed = time.Since(started) }()
	t := o.Tiles
	doc, convertErr := converted(document, false)
	if convertErr != nil {
		report.Problem = "the review does not convert to tiles"
		return nil, nil, report, nil
	}
	report.Provisional = provisional(document, t.Preferences.Pins)
	prompts := t.Prompts
	if prompts == nil {
		prompts = prompt.TilesPrompts{}
	}
	request := agent.ComposeRequest{Completer: CompleterOf(o.Backend), Backend: o.Backend.Name(), Prompts: prompts, Document: doc,
		Previous: t.Previous, Preferences: t.Preferences, Redactor: redactor, CallTimeout: o.PassTimeout, Timeout: agent.DefaultComposeTimeout}
	if !deadline.IsZero() {
		left := time.Until(deadline)
		if left < composeMinimum(o.Budgets.WallClock) {
			request.Skip = agent.FallbackNoTime
		} else {
			request.Timeout = min(request.Timeout, left)
		}
	}
	if request.Skip == "" && !budgetLeft(o.Budgets, spent) {
		request.Skip = agent.FallbackBudget
	}
	composeCtx, cancel := detached(ctx)
	defer cancel()
	out, err := safeCompose(composeCtx, request)
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

// safeCompose is agent.ComposeRun, with a panic an error: a bug in arranging
// the tiles must not cost the review.
func safeCompose(ctx context.Context, request agent.ComposeRequest) (out agent.ComposeOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("arranging the tiles panicked: %v", r)
		}
	}()
	return agent.ComposeRun(ctx, request)
}
