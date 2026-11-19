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
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// Tiles, for a staged run of one pull request (Options.Tiles), adds the tile
// analysis to the review's stages (agent.StagedTiles: library selection in
// the outline, a fill per selected library type and the exhibits call beside
// the details and the context) and presents the review on its page once its
// analysis has ended: one call through the backend's agent.Completer
// (agent.ViewRun, the view call, which replaced the composition of the tile
// grid) writes the overview's parts and diagrams and places the tiles, which
// is stored with the version (Result.Composition, a baum.composition/2) with
// the library and authored tiles. The review itself is published as without
// it: the composition never fails a run, and a composition that cannot be
// made leaves the version with the fallback or none.
type Tiles struct {
	// Prompts defaults to prompt.TilesPrompts{}. When it does not supply
	// the tile analysis texts (agent.TileAnalysisPrompts), the built-in ones
	// are used.
	Prompts agent.ViewPrompts
	// Library are the library types offered; nil offers every built-in one
	// (library.All). NoLibrary offers none, NoExhibits writes no authored
	// tile.
	Library               []library.Entry
	NoLibrary, NoExhibits bool
	// Previous is the previous version's stored review view composition
	// (store.ParseComposition), nil without one: the tile analysis reuses
	// its library and authored tiles' data. Publish sets it from the latest
	// version.
	Previous *view.Composition
	// Always are the library types the person added to the pull request's
	// review, with the last data held for each (agent.StagedTiles.Always):
	// each is filled, or its data reused, and appears in the composition.
	// A saved type pinned to the repository may be named here with its
	// last data; it is filled only when Saved returns it.
	Always []agent.AlwaysTile
	// Saved, when set, chooses the person's saved tile types for the run
	// once the pull request is assembled (tilelibrary.Offer): always are
	// filled whatever the outline answers, as Always are, and offered join
	// the library types the outline is offered. Their tiles are placed as
	// the built-in types' are.
	Saved SavedTypes

	// saved are the definition digests of the saved types the run offers.
	saved []string
}

// SavedTypes chooses the saved tile types for a run: diffPaths are the
// paths its diff changes, taken the library types it offers already (the
// built-in ones and those the person added).
type SavedTypes func(ctx context.Context, diffPaths, taken []string) (always, offered []library.Entry)

// withSaved is t with the saved types Saved chooses for the pull request:
// they join the library offered, after the built-in types, and the always
// ones join Always. Without Saved, or with no library, it is t.
func (t *Tiles) withSaved(ctx context.Context, b *bundle.Bundle) *Tiles {
	if t == nil || t.Saved == nil || t.NoLibrary {
		return t
	}
	out := *t
	base := t.Library
	if base == nil {
		base = library.All()
	}
	var taken []string
	for _, e := range base {
		taken = append(taken, e.Definition.Name)
	}
	for _, a := range t.Always {
		if _, ok := library.Get(a.Name); ok && !slices.Contains(taken, a.Name) {
			taken = append(taken, a.Name)
		}
	}
	paths := map[string]bool{}
	var diffPaths []string
	for _, h := range b.Diff {
		for _, p := range []string{h.Path, h.OldPath} {
			if p != "" && !paths[p] {
				paths[p] = true
				diffPaths = append(diffPaths, p)
			}
		}
	}
	always, offered := t.Saved(ctx, diffPaths, taken)
	out.Library = slices.Clone(base)
	out.Always = slices.Clone(t.Always)
	add := func(e library.Entry) bool {
		if slices.ContainsFunc(out.Library, func(have library.Entry) bool { return have.Definition.Name == e.Definition.Name }) {
			return false
		}
		out.Library = append(out.Library, e)
		out.saved = append(out.saved, e.Definition.Digest)
		return true
	}
	for _, e := range always {
		if add(e) && !slices.ContainsFunc(out.Always, func(a agent.AlwaysTile) bool { return a.Name == e.Definition.Name }) {
			out.Always = append(out.Always, agent.AlwaysTile{Name: e.Definition.Name})
		}
	}
	for _, e := range offered {
		add(e)
	}
	return &out
}

// ComposeReport is what a run's compose stage, the view call, did
// (Result.Compose).
type ComposeReport struct {
	// Outcome is what the stage spent; it is already in Result.Outcome.
	Outcome agent.Outcome
	// Fallback says no reply was used (no diagrams, verdict or risk
	// summary; the tiles placed by their hunks), and FallbackReason says why
	// (agent.Fallback*), the host's own words.
	Fallback       bool
	FallbackReason string
	Repaired       bool
	// Fixes counts what the host's checks fixed, and the tiles it placed, by
	// kind.
	Fixes map[tiles.FixKind]int
	// Inventory is the view call's user message (its input) and Reply its
	// last reply, masked; empty when no call was made.
	Inventory string
	Reply     []byte
	// Provisional is the composition drafts showed while the review was
	// written (provisional), for the finished review.
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

// converted is the normalised stored form of a completed review and its
// tile document, as the store converts it to check a composition
// (tiles.FromReview of the normalised stored form).
func converted(document []byte, draft bool) ([]byte, *tiles.Document, error) {
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return nil, nil, err
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{Draft: draft})
	return normalized, doc, err
}

// provisional is the review view composition of a draft, or of a review the
// view call has not presented: no overview parts and no diagrams, and the
// draft's library tiles, pending or filled (draftLibrary), each placed on
// the section whose hunks it covers, or, for one that covers none yet (a
// pending one), the hunks the outline chose for it (hunks, by tile ID), so
// it keeps its place once filled. The tile grid lays them out with the
// change kind's template. It is nil when the document does not convert, or
// when making it fails in any way, a panic included: the draft is then shown
// without one.
func provisional(document []byte, library []tiles.LibraryTile, hunks map[string][]string) (out []byte) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	normalized, doc, err := converted(document, true)
	if err != nil {
		return nil
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(provisionalView(r, doc, library, hunks))
	if err != nil {
		return nil
	}
	return encoded
}

// provisionalView is provisional's composition of a converted draft; tests
// replace it.
var provisionalView = func(r *view.Review, doc *tiles.Document, library []tiles.LibraryTile, hunks map[string][]string) view.Composition {
	c, _ := view.Normalize(r, doc, &view.Composition{Library: library})
	covers := view.Covers(view.ExtraTiles(doc, &c))
	for id, chosen := range hunks {
		if len(covers[id]) == 0 {
			covers[id] = chosen
		}
	}
	view.Place(r, &c, view.ExtraIDs(&c), covers)
	return c
}

// draftHunks are the hunks the outline chose for each selected library
// type's tile, by tile ID.
func draftHunks(p *agent.TilesProgress) map[string][]string {
	if p == nil {
		return nil
	}
	out := map[string][]string{}
	for _, f := range p.Fills {
		out[tiles.LibraryTileID(f.Type)] = f.Hunks
	}
	return out
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

// composeTiles presents the completed review document on its page with the
// view call (agent.ViewRun): the review view composition to store and its
// generator, or none, and what the stage did. It runs after the analysis,
// which had the run's whole wall clock and budgets, on a context that
// follows ctx's cancellation but not its deadline, bounded by
// agent.DefaultViewTimeout: the run may end up to that much after its wall
// clock. It makes at most the calls the run's budgets leave (composeCalls),
// and none, the fallback applying, when they leave none. Only a cancelled
// ctx is an error (agent.ErrViewInterrupted): the run is stopped and stores
// nothing. Any other failure, a panic included, leaves the version without
// a composition and says why in the report.
func composeTiles(ctx context.Context, o Options, document []byte, spent agent.Outcome, redactor *bundle.Redactor, analysis *agent.TilesOutcome) (composition []byte, generator *tiles.Generator, report *ComposeReport, err error) {
	started := time.Now()
	report = &ComposeReport{}
	defer func() {
		report.Elapsed = time.Since(started)
		if r := recover(); r != nil {
			composition, generator, err = nil, nil, nil
			report.Problem = "presenting the review failed"
		}
	}()
	t := o.Tiles
	normalized, doc, convertErr := converted(document, false)
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
	report.Provisional = provisional(document, draftLibrary(progressOf(analysis), entries), draftHunks(progressOf(analysis)))
	prompts := t.Prompts
	if prompts == nil {
		prompts = prompt.TilesPrompts{}
	}
	request := agent.ViewRequest{Completer: CompleterOf(o.Backend), Backend: o.Backend.Name(), Prompts: prompts, Document: normalized, Converted: doc,
		Fills: fills, Authored: authored, Saved: t.saved, Redactor: redactor, CallTimeout: o.PassTimeout,
		Timeout: agent.DefaultViewTimeout, MaxCalls: composeCalls(o.Budgets, spent)}
	if request.MaxCalls == 0 {
		request.Skip = agent.FallbackBudget
	}
	viewCtx, cancel := detached(ctx)
	defer cancel()
	out, err := viewRun(viewCtx, request)
	report.Outcome = out.Outcome
	report.Fallback, report.FallbackReason, report.Repaired = out.Fallback, out.FallbackReason, out.Repaired
	report.Inventory, report.Reply = out.Input, out.Reply
	if errors.Is(err, agent.ErrViewInterrupted) {
		return nil, nil, report, err
	}
	if err != nil {
		report.Problem = "the review could not be presented"
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

// viewRun is agent.ViewRun; tests replace it.
var viewRun = agent.ViewRun

// staged is the tile analysis of a run with Tiles.
func (t *Tiles) staged() *agent.StagedTiles {
	prompts, ok := t.Prompts.(agent.TileAnalysisPrompts)
	if !ok {
		prompts = prompt.TilesPrompts{}
	}
	out := &agent.StagedTiles{Prompts: prompts, Exhibits: !t.NoExhibits, Previous: t.Previous, Always: t.Always}
	if !t.NoLibrary {
		out.Library = t.Library
		if out.Library == nil {
			out.Library = library.All()
		}
	}
	// A type the person added is offered whatever the run offers.
	for _, added := range t.Always {
		if slices.ContainsFunc(out.Library, func(e library.Entry) bool { return e.Definition.Name == added.Name }) {
			continue
		}
		if e, ok := library.Get(added.Name); ok {
			out.Library = append(slices.Clone(out.Library), e)
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
	// review (view.DuplicatesTile).
	Duplicates []string
}

// TileExtras is extras, for tools that compose an analysis again (replay's
// compose generator over an earlier tiles run).
func TileExtras(doc *tiles.Document, t *agent.TilesOutcome) ([]view.Fill, []view.Authored) {
	fills, authored, _ := extras(doc, t, library.All())
	return fills, authored
}

// extras turns the tile analysis into what the composer places: each
// selected type's fill (ready ones with their library tile, its related
// items written against the review) and the authored tiles that do not
// restate the review. doc is the review's converted tile document.
func extras(doc *tiles.Document, t *agent.TilesOutcome, entries []library.Entry) ([]view.Fill, []view.Authored, []string) {
	if t == nil {
		return nil, nil, nil
	}
	// The entries are the types the host offered: the built-in ones and the
	// person's saved types (Tiles.Saved), never one a pull request names.
	byName := map[string]library.Entry{}
	for _, e := range entries {
		byName[e.Definition.Name] = e
	}
	var fills []view.Fill
	var ready []tiles.Tile
	for _, f := range t.Fills {
		e, ok := byName[f.Type]
		if !ok {
			continue
		}
		state := "failed"
		var data json.RawMessage
		if f.State == agent.FillReady {
			state, data = "ready", f.Data
			if e.DeclaresRelated() {
				data = library.WithRelated(f.Data, doc)
			}
			ready = append(ready, tiles.Tile{ID: tiles.LibraryTileID(f.Type), Type: tiles.TypeCustom, Data: data})
		}
		if f.State == agent.FillNotApplies {
			continue
		}
		fills = append(fills, view.FillOf(e, f.Hunks, f.Why, state, data))
	}
	var authored []view.Authored
	var duplicates []string
	if x := t.Exhibits; x != nil {
		for _, a := range x.Tiles {
			if view.DuplicatesTile(doc, ready, a) {
				duplicates = append(duplicates, a.ID)
				continue
			}
			authored = append(authored, view.Authored{Tile: a, Adds: x.Adds[a.ID]})
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
