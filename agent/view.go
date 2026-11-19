package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// ViewPrompts supplies the instructions of the call that writes a review's
// page view (ViewRun).
type ViewPrompts interface {
	// ViewInstructions returns the view prompt for a backend. Its Version
	// and Digest are recorded in the composition's generator.
	ViewInstructions(ctx context.Context, backend string) (Prompt, error)
}

// ViewRequest is one view call over a written review.
type ViewRequest struct {
	// Completer makes the call; nil falls back at once.
	Completer Completer
	// Backend names the completer's backend, for the prompt.
	Backend string
	Prompts ViewPrompts
	// Document is the review, in its normalised stored form
	// (review.NormalizeStored), and Converted the tile document
	// tiles.FromReview converted from it, which checks the library and
	// authored tiles; nil leaves the composition without them.
	Document  []byte
	Converted *tiles.Document
	// Fills are the library tiles the analysis selected, the ready ones with
	// their tiles (view.FillOf); Authored are the authored tiles it wrote
	// that passed the host's checks. The composition holds both, whatever
	// the call replies: a tile it does not place is placed by the hunks it
	// covers (view.Place). Saved are the definition digests of the person's
	// saved tile types the host offered.
	Fills    []view.Fill
	Authored []view.Authored
	Saved    []string
	// Redactor masks secrets in the replies before they are read; nil uses
	// bundle.NewRedactor.
	Redactor *bundle.Redactor
	// CallTimeout bounds each call; zero leaves only the stage's deadline.
	CallTimeout time.Duration
	// Timeout bounds the whole stage when ctx has no deadline; zero is
	// DefaultViewTimeout.
	Timeout time.Duration
	// MaxOutputTokens caps each reply; zero is DefaultViewOutputTokens.
	MaxOutputTokens int
	// Now stamps the composition; nil is time.Now.
	Now func() time.Time
	// MaxCalls bounds the calls, the repair included: zero is two, and one
	// makes no repair.
	MaxCalls int
	// Skip, when set, is why the host leaves the call out (FallbackBudget):
	// ViewRun makes no call and falls back with this reason.
	Skip string

	// redact masks a reply; nil is RedactTaskOutput. Tests replace it.
	redact func(context.Context, *bundle.Redactor, []byte, *Outcome) ([]byte, error)
}

// DefaultViewTimeout bounds the view stage, its repair included, when the
// caller's context has no deadline. A view call takes 10 to 40 seconds.
const DefaultViewTimeout = 90 * time.Second

// DefaultViewOutputTokens caps a view reply: a few diagrams and the
// overview are a few thousand tokens.
const DefaultViewOutputTokens = 12000

// ErrViewInterrupted is returned when the caller's context ended before or
// during the stage: the outcome counts what was spent but holds no
// composition, not even the fallback.
var ErrViewInterrupted = errors.New("agent: the view call was interrupted")

// The reasons a view composition fell back, which its generator's problems
// record, beside FallbackNoCompleter and FallbackBudget. They are the host's
// words and quote nothing a model wrote.
const (
	FallbackViewPrompt    = "the view prompt could not be loaded"
	FallbackViewInput     = "the view call's input could not be built"
	FallbackViewProvider  = "the view call failed"
	FallbackViewInvalid   = "the view call's reply did not match its schema after one repair"
	FallbackViewRedaction = "the view call's reply could not be checked for secrets"
)

// ViewOutcome is what ViewRun did. Its Outcome counts the calls, tokens,
// usage and repairs, as a stage's does.
type ViewOutcome struct {
	Outcome
	// Composition is checked (view.Normalize), every library and authored
	// tile placed, its generator set: the call's, or the fallback's.
	Composition view.Composition
	// Report counts what the checks fixed and the tiles the host placed.
	Report view.Report
	// Input is the user message the call read.
	Input string
	// Reply is the last reply, masked; nil when no call answered.
	Reply []byte
	// Fallback says no reply was used; FallbackReason says why.
	Fallback       bool
	FallbackReason string
	// Repaired says a second call was made after an invalid reply.
	Repaired bool
}

// ViewRun writes a review's page view with one structured call through a
// Completer, with no tools and no files: the call reads the review's
// summary, recommendation, risk, sections, context, attachments, linked
// sessions' first prompts and the library and authored tiles
// (view.BuildInput), and replies with the overview's parts, the tiles placed
// on each section and the diagrams it draws (view.Reply). A reply that does
// not decode gets one repair call, which resends the input with the
// problems; anything the host can check is checked instead
// (view.Normalize: an item that breaks a rule is dropped, never the whole
// reply). The library and authored tiles the call does not place are
// placed by the hunks they cover (view.Place), and the generator is set
// with the prompt, the model, the time and the fixes by kind.
//
// Whatever fails (no completer, the prompt, the input, the call, a reply
// invalid after the repair, redaction), the composition is the fallback:
// no diagrams, no verdict and no risk summary, the library and authored
// tiles placed by their hunks, Fallback set and the reason in the
// generator's problems. Every reply is masked for secrets before it is read
// (RedactTaskOutput).
//
// Without a deadline on ctx the stage is bounded by Timeout
// (DefaultViewTimeout); when that bound passes, it falls back. When ctx
// itself ends, before or during the stage, ViewRun makes no further call
// and returns ErrViewInterrupted with no composition. The only other error
// is for a document that is not a review.
func ViewRun(ctx context.Context, r ViewRequest) (ViewOutcome, error) {
	var out ViewOutcome
	review, err := view.ParseReview(r.Document)
	if err != nil {
		return out, fmt.Errorf("agent: the view applies only to a review: %w", err)
	}
	parent := ctx
	interrupted := func() (ViewOutcome, error) {
		return out, fmt.Errorf("%w: %w", ErrViewInterrupted, parent.Err())
	}
	if parent.Err() != nil {
		return interrupted()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmp.Or(r.Timeout, DefaultViewTimeout))
		defer cancel()
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	// The library and authored tiles, checked once: the input lists those
	// that pass, and every composition holds them.
	library, authored := extras(r.Fills, r.Authored, r.Saved)
	base, baseReport := view.Normalize(review, r.Converted, &view.Composition{Library: library, Authored: authored})
	extras := view.ExtraTiles(r.Converted, &base)

	var prompt Prompt
	fallback := func(reason string) (ViewOutcome, error) {
		c := base
		c.Sections = []view.Placement{}
		return r.finish(out, review, c, baseReport, extras, prompt, true, reason, now)
	}
	if r.Skip != "" {
		return fallback(r.Skip)
	}
	if r.Completer == nil {
		return fallback(FallbackNoCompleter)
	}
	if r.Prompts == nil {
		return fallback(FallbackViewPrompt)
	}
	if prompt, err = r.Prompts.ViewInstructions(ctx, r.Backend); err != nil {
		return fallback(FallbackViewPrompt)
	}
	if out.Input, err = view.BuildInput(review, r.inputTiles(&base, extras)).Message(); err != nil {
		return fallback(FallbackViewInput)
	}
	redactor := r.Redactor
	if redactor == nil {
		if redactor, err = bundle.NewRedactor(); err != nil {
			return fallback(FallbackViewRedaction)
		}
	}

	request := CompleteRequest{System: prompt.Body, User: out.Input, Schema: view.ReplySchema(),
		MaxOutputTokens: cmp.Or(r.MaxOutputTokens, DefaultViewOutputTokens)}
	var reply view.Reply
	for attempt := 0; ; attempt++ {
		raw, err := completeOnce(ctx, r.Completer, r.CallTimeout, r.redact, &out.Outcome, request, redactor)
		if parent.Err() != nil {
			return interrupted()
		}
		if err != nil {
			if errors.Is(err, errRedaction) {
				return fallback(FallbackViewRedaction)
			}
			return fallback(FallbackViewProvider)
		}
		out.Reply = raw
		var problems []string
		if reply, problems = view.ParseReply(raw); problems == nil {
			break
		}
		if attempt == 1 || r.MaxCalls == 1 {
			out.Findings = strings.Join(problems, "\n")
			return fallback(FallbackViewInvalid)
		}
		out.Repaired = true
		out.ValidationRepairs++
		request.User = viewRepairMessage(out.Input, raw, problems)
	}
	var report view.Report
	c := reply.Composition(&report)
	c.Library, c.Authored = base.Library, base.Authored
	n, checked := view.Normalize(review, r.Converted, &c)
	report.Merge(checked)
	return r.finish(out, review, n, report, extras, prompt, false, "", now)
}

// finish places the tiles the composition leaves unplaced and sets the
// generator.
func (r ViewRequest) finish(out ViewOutcome, review *view.Review, c view.Composition, report view.Report, extras []tiles.Tile, prompt Prompt, fallback bool, reason string, now func() time.Time) (ViewOutcome, error) {
	if fallback {
		c.Overview = view.Overview{Screenshots: []string{}, Intent: []view.Intent{}}
		c.Diagrams = nil
	}
	if placed := view.Place(review, &c, view.ExtraIDs(&c), view.Covers(extras)); placed > 0 {
		report.Merge(view.PlacedReport(placed))
	}
	g := tiles.Generator{PromptVersion: prompt.Version, PromptDigest: prompt.Digest, Model: boundedBytes(out.Model, 200),
		ComposedAt: now().UTC().Format(time.RFC3339), Fallback: fallback, Fixes: report.Counts()}
	if reason != "" {
		g.Problems = []string{reason}
	}
	checked, err := g.Checked()
	if err != nil {
		return out, err
	}
	c.Generator = &checked
	out.Composition, out.Report = c, report
	out.Fallback, out.FallbackReason = fallback, reason
	return out, nil
}

// inputTiles are the library and authored tiles as the call's input lists
// them, in the composition's order.
func (r ViewRequest) inputTiles(c *view.Composition, extras []tiles.Tile) []view.InputTile {
	libraryIDs := map[string]view.Fill{}
	for _, f := range r.Fills {
		libraryIDs[tiles.LibraryTileID(f.Type)] = f
	}
	adds := map[string]string{}
	for _, a := range r.Authored {
		adds[a.Tile.ID] = a.Adds
	}
	var out []view.InputTile
	for _, t := range extras {
		in := view.InputTile{ID: t.ID, Kind: "exhibit", Type: t.Type, Title: t.Title, Glance: cmp.Or(adds[t.ID], t.Headline), Hunks: t.Covers}
		if f, ok := libraryIDs[t.ID]; ok && t.Type == tiles.TypeCustom {
			in.Kind, in.Glance = "library", f.Glance
		}
		if t.State != tiles.StateReady {
			in.State = t.State
		}
		out = append(out, in)
	}
	return out
}

// viewRepairMessage is the user message of the repair call: the input
// again, the rejected reply as data and what was wrong with it.
func viewRepairMessage(input string, rejected []byte, problems []string) string {
	return repairMessage(input, rejected, problems)
}
