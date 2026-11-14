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
	"github.com/baum-ai/baum/tiles/library"
)

// Tiles, for a staged run of one pull request (Options.Tiles), adds the tile
// analysis to the review's stages (agent.StagedTiles: library selection in
// the outline, a fill per selected library type and the exhibits call beside
// the details and the context) and arranges the review's tiles once its
// analysis has ended: one call through the backend's agent.Completer
// (agent.ComposeRun) chooses the layout, which is stored with the version
// (Result.Composition) with the library and authored tiles. The review itself
// is published as without it: the composition never fails a run, and a
// composition that cannot be made leaves the version with the template's
// layout or none.
type Tiles struct {
	// Prompts defaults to prompt.TilesPrompts{}. When it does not supply
	// the tile analysis texts (agent.TileAnalysisPrompts), the built-in ones
	// are used.
	Prompts agent.ComposePrompts
	// Library are the library types offered; nil offers every built-in one
	// (library.All). NoLibrary offers none, NoExhibits writes no authored
	// tile.
	Library               []library.Entry
	NoLibrary, NoExhibits bool
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
	// Tiles is what the tile analysis gave the composition, nil without
	// one.
	Tiles *TilesReport
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
// panic included: the draft is then shown in the converted layout. library
// are the draft's library tiles, pending or filled (draftLibrary), which take
// their default places.
func provisional(document []byte, pins []string, previous *tiles.Composition, library []tiles.LibraryTile) (out []byte) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	doc, err := converted(document, true)
	if err != nil {
		return nil
	}
	c, err := provisionalLayout(doc, pins, previous, library)
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
var provisionalLayout = func(doc *tiles.Document, pins []string, previous *tiles.Composition, library []tiles.LibraryTile) (tiles.Composition, error) {
	if previous != nil && !previous.Empty() {
		kept := *previous
		kept.Generator, kept.Layout.Hidden = nil, nil
		kept.Layout.Entries = slices.Clone(previous.Layout.Entries)
		// This version's library tiles, not the previous version's; its
		// authored tiles come with the composition.
		kept.Library, kept.Authored = library, nil
		tiles.PlaceExtras(&kept, doc)
		if n, report := tiles.NormalizeComposition(doc, &kept); report.Counts()[tiles.FixUnusable] == 0 && !n.Empty() {
			return n, nil
		}
	}
	c, _, err := agent.TemplateCompositionWith(doc, pins, &tiles.Composition{Library: library})
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
func composeTiles(ctx context.Context, o Options, document []byte, spent agent.Outcome, redactor *bundle.Redactor, analysis *agent.TilesOutcome) (composition []byte, generator *tiles.Generator, report *ComposeReport, err error) {
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
	entries := t.Library
	if entries == nil {
		entries = library.All()
	}
	fills, authored, duplicates := extras(doc, analysis, entries)
	report.Tiles = &TilesReport{TilesOutcome: analysis, Duplicates: duplicates}
	report.Provisional = provisional(document, t.Preferences.Pins, t.Previous, draftLibrary(progressOf(analysis), entries))
	prompts := t.Prompts
	if prompts == nil {
		prompts = prompt.TilesPrompts{}
	}
	request := agent.ComposeRequest{Completer: CompleterOf(o.Backend), Backend: o.Backend.Name(), Prompts: prompts, Document: doc,
		Fills: fills, Authored: authored, Previous: t.Previous, Preferences: t.Preferences, Redactor: redactor, CallTimeout: o.PassTimeout,
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

// staged is the tile analysis of a run with Tiles.
func (t *Tiles) staged() *agent.StagedTiles {
	prompts, ok := t.Prompts.(agent.TileAnalysisPrompts)
	if !ok {
		prompts = prompt.TilesPrompts{}
	}
	out := &agent.StagedTiles{Prompts: prompts, Exhibits: !t.NoExhibits}
	if !t.NoLibrary {
		out.Library = t.Library
		if out.Library == nil {
			out.Library = library.All()
		}
	}
	if len(out.Library) == 0 && !out.Exhibits {
		return nil
	}
	return out
}

// TilesReport is what the tile analysis gave the composition
// (Result.TilesAnalysis): the outcome of its calls, already in the run's,
// and the authored tiles the host dropped as duplicates.
type TilesReport struct {
	*agent.TilesOutcome
	// Duplicates are the authored tiles dropped because they restate the
	// review (compose.DuplicatesTile).
	Duplicates []string
}

// extras turns the tile analysis into what the composer places: each
// selected type's fill (ready ones with their library tile, its related
// items written against the review) and the authored tiles that do not
// restate the review. doc is the review's converted tile document.
func extras(doc *tiles.Document, t *agent.TilesOutcome, entries []library.Entry) ([]compose.Fill, []compose.Authored, []string) {
	if t == nil {
		return nil, nil, nil
	}
	byName := map[string]library.Entry{}
	for _, e := range entries {
		byName[e.Definition.Name] = e
	}
	var fills []compose.Fill
	var ready []tiles.Tile
	for _, f := range t.Fills {
		e, ok := byName[f.Type]
		if !ok {
			continue
		}
		state := "failed"
		var data json.RawMessage
		if f.State == agent.FillReady {
			state, data = "ready", library.WithRelated(f.Data, doc)
			ready = append(ready, tiles.Tile{ID: tiles.LibraryTileID(f.Type), Type: tiles.TypeCustom, Data: data})
		}
		if f.State == agent.FillNotApplies {
			continue
		}
		fills = append(fills, compose.FillOf(e, f.Hunks, f.Why, state, data))
	}
	var authored []compose.Authored
	var duplicates []string
	if x := t.Exhibits; x != nil {
		for _, a := range x.Tiles {
			if compose.DuplicatesTile(doc, ready, a) {
				duplicates = append(duplicates, a.ID)
				continue
			}
			authored = append(authored, compose.Authored{Tile: a, Adds: x.Adds[a.ID]})
		}
	}
	return fills, authored, duplicates
}

// draftLibrary is the library tiles of a draft: each selected type, pending
// until its fill lands, then ready with its data; a fill that failed or
// found the type does not apply is left out.
func draftLibrary(p *agent.TilesProgress, entries []library.Entry) []tiles.LibraryTile {
	if p == nil {
		return nil
	}
	var out []tiles.LibraryTile
	for _, f := range p.Fills {
		for _, e := range entries {
			if e.Definition.Name != f.Type {
				continue
			}
			switch f.State {
			case agent.FillPending:
				out = append(out, tiles.LibraryTile{Definition: e.Encoded, Data: json.RawMessage(`{}`), Pending: true})
			case agent.FillReady:
				out = append(out, tiles.LibraryTile{Definition: e.Encoded, Data: f.Data})
			}
		}
	}
	return out
}

// progressOf is the draft view of a finished analysis: the fills as they
// ended, with the selection.
func progressOf(t *agent.TilesOutcome) *agent.TilesProgress {
	if t == nil {
		return nil
	}
	return &agent.TilesProgress{Selection: t.Selection, Fills: t.Fills}
}
