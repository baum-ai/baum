package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

var tilesNow = func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) }

// tilesOptions is a staged run with tile reviews on, at a fixed time and
// without a token budget, which would leave the view call no room.
func tilesOptions(t *testing.T, f *fixture) generate.Options {
	o := stagedOptions(t, f)
	o.Budgets.MaxTokens = 0
	o.Now = tilesNow
	o.Tiles = &generate.Tiles{}
	return o
}

// inputOf reads the input a view call's completion request carries.
func inputOf(t *testing.T, r agent.CompleteRequest) view.Input {
	t.Helper()
	var in view.Input
	if err := json.Unmarshal([]byte(r.User[strings.Index(r.User, "\n")+1:]), &in); err != nil {
		t.Errorf("the input does not decode: %v", err)
	}
	return in
}

// keepTemplate answers the view call with a reply that writes nothing: no
// overview parts, no diagrams, no placements, so the host places the
// analysis's tiles. With lead set, it draws a flow citing that section's
// first hunk and places it on the section, and gives a risk summary.
func keepTemplate(t *testing.T, lead string, _ any) func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
	return func(_ context.Context, r agent.CompleteRequest, _ int) (agent.Completion, error) {
		in := inputOf(t, r)
		reply := map[string]any{
			"overview": map[string]any{"risk_summary": nil, "verdict": nil, "screenshots": []any{}, "diagram": nil, "intent": []any{}},
			"sections": []any{}, "diagrams": []any{},
		}
		for _, s := range in.Sections {
			if s.ID != lead || len(s.Hunks) == 0 {
				continue
			}
			reply["overview"].(map[string]any)["risk_summary"] = "The change is small and covered."
			reply["sections"] = []any{map[string]any{"id": s.ID, "tiles": []any{"dg_lead"}}}
			reply["diagrams"] = []any{map[string]any{"id": "dg_lead", "type": "flow", "title": "How it runs", "caption": nil, "source": []any{s.Hunks[0].ID},
				"lanes": []any{map[string]any{"label": nil, "steps": []any{
					map[string]any{"title": "Before", "note": nil, "tone": nil, "join": nil},
					map[string]any{"title": "After", "note": nil, "tone": "new", "join": "arrow"}}}}}}
		}
		raw, err := json.Marshal(reply)
		return agent.Completion{Document: raw, Model: "stub-composer", Usage: agent.Usage{Input: 3000, Output: 400}}, err
	}
}

// storedComposition reads a stored composition as the grid and the tile
// analysis read it: its library and authored tiles and its generator.
func storedComposition(t *testing.T, raw []byte) *tiles.Composition {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("no composition was stored")
	}
	c, err := tiles.ParseComposition(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// storedView reads a stored review view composition.
func storedView(t *testing.T, raw []byte) *view.Composition {
	t.Helper()
	c, err := view.Parse(raw)
	if err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return c
}

func publishTiles(t *testing.T, f *fixture, o generate.Options) (*store.Store, store.Key, generate.Result) {
	t.Helper()
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(context.Background(), s, key, o, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	return s, key, result
}

// With tile reviews off nothing is presented: no completion, no
// composition, drafts without one. With them on, the review published is the
// same bytes; only the composition is added, and what the view call spent is
// counted.
func TestTilesOffArrangesNothingAndOnKeepsTheReview(t *testing.T) {
	off := newFixture(t)
	off.backend.RunFunc = generatetest.StagedReplies(nil)
	off.backend.CompleteFunc = func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
		t.Error("a run without tiles made a completion")
		return agent.Completion{}, errors.New("unexpected")
	}
	o := tilesOptions(t, off)
	o.Tiles = nil
	var drafts []generate.Draft
	var mu sync.Mutex
	o.OnDraft = func(d generate.Draft) {
		mu.Lock()
		defer mu.Unlock()
		drafts = append(drafts, d)
	}
	s, key, plain := publishTiles(t, off, o)
	latest, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Composition) != 0 || plain.Composition != nil || plain.Compose != nil || off.backend.CompletionCount() != 0 {
		t.Fatalf("composition %s, compose %+v", latest.Composition, plain.Compose)
	}
	for _, d := range drafts {
		if d.Composition != nil {
			t.Fatal("a draft has a composition")
		}
	}
	if plain.Metadata.Calls != 3 {
		t.Errorf("calls %d", plain.Metadata.Calls)
	}

	on := newFixture(t)
	on.backend.RunFunc = generatetest.StagedReplies(nil)
	on.backend.CompleteFunc = keepTemplate(t, "sec_rest", nil)
	_, _, arranged := publishTiles(t, on, tilesOptions(t, on))
	if !bytes.Equal(plain.Document, arranged.Document) {
		t.Errorf("the review differs with tiles on:\n%s\n%s", plain.Document, arranged.Document)
	}
	if arranged.Metadata.Calls != plain.Metadata.Calls+1 || arranged.Metadata.Tokens <= plain.Metadata.Tokens ||
		len(arranged.Metadata.Accounting.Attempts) != len(plain.Metadata.Accounting.Attempts)+1 {
		t.Errorf("the view call is not counted: %+v against %+v", arranged.Metadata, plain.Metadata)
	}
	// The cost is priced from these attempts, the view call's among them.
	if last := arranged.Metadata.Accounting.Attempts[len(arranged.Metadata.Accounting.Attempts)-1]; last.Model != "stub-composer" || last.Usage.Input != 3000 {
		t.Errorf("the view call's attempt %+v", last)
	}
}

// With tile reviews on, the view call's composition is stored with the
// version, with its generator, and the tile route serves its view, the grid
// falling back to the template's layout.
func TestTilesOnStoresAndServesTheComposition(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	f.backend.CompleteFunc = keepTemplate(t, "sec_rest", nil)
	s, key, result := publishTiles(t, f, tilesOptions(t, f))
	latest, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	c := storedView(t, latest.Composition)
	if c.Generator == nil || c.Generator.Fallback || c.Generator.Model != "stub-composer" || c.Generator.PromptVersion == "" || c.Generator.ComposedAt == "" {
		t.Fatalf("generator %+v", c.Generator)
	}
	if len(c.Diagrams) != 1 || c.Overview.RiskSummary == "" || len(c.Sections) != 1 || c.Sections[0].ID != "sec_rest" {
		t.Fatalf("composition %s", latest.Composition)
	}
	if result.Compose == nil || result.Compose.Fallback || result.Compose.Problem != "" || f.backend.CompletionCount() != 1 {
		t.Fatalf("compose %+v", result.Compose)
	}
	request := f.backend.Completions[0]
	if request.System == "" || !strings.Contains(request.User, "sec_rest") || string(request.Schema) != string(view.ReplySchema()) {
		t.Errorf("the request %+v", request)
	}
	served := tileview.Build(latest.Document, latest.Composition, tiles.Options{})
	if served.Tiles == nil || served.View == nil || served.Problem != "" {
		t.Fatalf("served %q", served.Problem)
	}
	if served.Tiles.Origin.Kind != tiles.OriginGenerated {
		t.Errorf("the grid's origin %q", served.Tiles.Origin.Kind)
	}
	if len(served.View.Tiles) != 1 || served.View.Tiles[0].ID != "dg_lead" || served.View.Overview.RiskSummary == "" {
		t.Errorf("the view %+v", served.View)
	}
	if !strings.Contains(f.progress.String(), "page view written") {
		t.Errorf("progress:\n%s", f.progress.String())
	}
	f.requireClean(t)
}

// Whatever goes wrong with the view call, the review is published: an
// invalid reply or a provider error falls back, stored as such; a backend
// that cannot complete gets the fallback without a call; and the run's call
// and token budgets bound the view call, which skips the call when they
// leave nothing and never makes more calls than they allow.
func TestTilesFallBackAndStillPublish(t *testing.T) {
	invalid := func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
		return agent.Completion{Document: []byte(`{"diagrams":"no"}`), Model: "stub-composer"}, nil
	}
	cases := []struct {
		name     string
		complete func(context.Context, agent.CompleteRequest, int) (agent.Completion, error)
		options  func(*generate.Options)
		reason   string
		calls    int
	}{
		{name: "invalid reply", reason: agent.FallbackViewInvalid, calls: 2, complete: invalid},
		{name: "provider error", reason: agent.FallbackViewProvider, calls: 1,
			complete: func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
				return agent.Completion{Model: "stub-composer", Usage: agent.Usage{Input: 10}}, errors.New("overloaded")
			}},
		{name: "cannot complete", reason: agent.FallbackNoCompleter},
		{name: "one call left, no repair", reason: agent.FallbackViewInvalid, calls: 1, complete: invalid,
			// The analysis makes three calls.
			options: func(o *generate.Options) { o.Budgets.MaxCalls = 4 }},
		{name: "no call left", reason: agent.FallbackBudget,
			complete: keepTemplate(t, "", nil),
			options:  func(o *generate.Options) { o.Budgets.MaxCalls = 3 }},
		{name: "budget spent", reason: agent.FallbackBudget,
			complete: keepTemplate(t, "", nil),
			options:  func(o *generate.Options) { o.Budgets.MaxTokens = 10000 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.backend.RunFunc = generatetest.StagedReplies(nil)
			f.backend.CompleteFunc = c.complete
			o := tilesOptions(t, f)
			o.Budgets.WallClock = 10 * time.Minute
			if c.options != nil {
				c.options(&o)
			}
			s, err := store.Open("", t.TempDir(), true)
			if err != nil {
				t.Fatal(err)
			}
			key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
			result, err := generate.Publish(context.Background(), s, key, o, nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, f.progress.String())
			}
			latest, err := s.Latest(context.Background(), key)
			if err != nil || latest.Metadata.Version != 1 {
				t.Fatalf("not published: %v", err)
			}
			if max := o.Budgets.MaxCalls; max > 0 && latest.Metadata.Calls > max {
				t.Errorf("%d calls recorded, more than the budget's %d", latest.Metadata.Calls, max)
			}
			stored := storedView(t, latest.Composition)
			if stored.Generator == nil || !stored.Generator.Fallback || !slices.Equal(stored.Generator.Problems, []string{c.reason}) || len(stored.Diagrams) != 0 {
				t.Fatalf("generator %+v", stored.Generator)
			}
			if !result.Compose.Fallback || result.Compose.FallbackReason != c.reason || f.backend.CompletionCount() != c.calls {
				t.Errorf("compose %+v, %d call(s)", result.Compose, f.backend.CompletionCount())
			}
			if !strings.Contains(f.progress.String(), "page view fell back: "+c.reason) {
				t.Errorf("progress:\n%s", f.progress.String())
			}
			f.requireClean(t)
		})
	}
}

// A run cancelled while the view call works stops there: nothing is stored,
// the run is interrupted as at any other point, and what the call was asked
// counts as spent.
func TestTilesCancelledWhileComposing(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.backend.CompleteFunc = func(ctx context.Context, _ agent.CompleteRequest, _ int) (agent.Completion, error) {
		cancel()
		<-ctx.Done()
		return agent.Completion{Model: "stub-composer", Usage: agent.Usage{Input: 10}}, ctx.Err()
	}
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(ctx, s, key, tilesOptions(t, f), nil)
	if generate.CategoryOf(err) != generate.CategoryInterrupted {
		t.Fatalf("err %v", err)
	}
	if _, err := s.Latest(context.Background(), key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a version was stored: %v", err)
	}
	if result.Composition != nil || result.Compose == nil || result.Compose.Outcome.Calls != 1 || result.Outcome.Calls != 4 {
		t.Errorf("composition %s, compose %+v, calls %d", result.Composition, result.Compose, result.Outcome.Calls)
	}
	f.requireClean(t)
}

// Drafts carry a provisional review view composition while the review is
// written, then once the stored one; the grid's layout of both is the
// template's, so it does not move.
func TestTilesDraftsGoFromTheTemplateToTheComposition(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	var mu sync.Mutex
	var drafts []generate.Draft
	o.OnDraft = func(d generate.Draft) {
		mu.Lock()
		defer mu.Unlock()
		drafts = append(drafts, d)
	}
	_, _, result := publishTiles(t, f, o)
	if len(drafts) < 2 {
		t.Fatalf("%d draft(s)", len(drafts))
	}
	final := drafts[len(drafts)-1]
	if !bytes.Equal(final.Document, result.Document) || !bytes.Equal(final.Composition, result.Composition) {
		t.Fatal("the last draft is not the finished review with its composition")
	}
	for _, d := range drafts[:len(drafts)-1] {
		if c := storedView(t, d.Composition); c.Generator != nil || len(c.Diagrams) != 0 {
			t.Fatalf("a provisional composition %+v", c)
		}
	}
	landing := func(d generate.Draft) []tiles.Entry {
		served := tileview.Build(d.Document, d.Composition, tiles.Options{Draft: true})
		if served.Tiles == nil || served.View == nil {
			t.Fatalf("draft not served: %s", served.Problem)
		}
		visible := tiles.Visible(served.Tiles.Layout)
		var shown []tiles.Entry
		for i, e := range served.Tiles.Layout.Entries {
			if visible[i] {
				shown = append(shown, tiles.Entry{Tile: e.Tile, Size: e.Size, Group: e.Group})
			}
		}
		return shown
	}
	before, after := landing(drafts[len(drafts)-2]), landing(final)
	if !slices.Equal(before, after) {
		t.Errorf("the grid jumped:\n%+v\n%+v", before, after)
	}
	if !bytes.Equal(result.Compose.Provisional, drafts[len(drafts)-2].Composition) {
		t.Errorf("the reported provisional composition is not the last draft's")
	}
}

// A second version is written with the first's stored composition as its
// previous one: the tile analysis reads its library tiles, and the view
// call starts afresh.
func TestTilesSecondVersionReadsTheFirst(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	f.backend.CompleteFunc = keepTemplate(t, "sec_rest", nil)
	s, key, _ := publishTiles(t, f, tilesOptions(t, f))
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	if _, err := generate.Publish(context.Background(), s, key, tilesOptions(t, f), nil); err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	second, err := s.Latest(context.Background(), key)
	if err != nil || second.Metadata.Version != 2 {
		t.Fatalf("version %d: %v", second.Metadata.Version, err)
	}
	if c := storedView(t, second.Composition); len(c.Diagrams) != 0 || c.Generator == nil || c.Generator.Fallback {
		t.Errorf("second composition %s", second.Composition)
	}
}

// The analysis runs exactly as with tile reviews off, whatever the wall clock
// and budgets: every call gets the same budgets and the same deadline, and a
// slow outline that fits the wall clock without tiles fits it with them.
func TestTilesLeaveTheAnalysisItsLimits(t *testing.T) {
	type call struct {
		stage               string
		maxCalls, maxTokens int
		left                time.Duration
	}
	run := func(t *testing.T, wall time.Duration, calls, tokens int, slow time.Duration, tiles bool) ([]call, generate.Result, error) {
		f := newFixture(t)
		var mu sync.Mutex
		var seen []call
		var first time.Time
		replies := generatetest.StagedReplies(nil)
		f.backend.RunFunc = func(ctx context.Context, r agent.Request, n int) (agent.Result, error) {
			c := call{stage: generatetest.Stage(r), maxCalls: r.MaxCalls, maxTokens: r.MaxTokens}
			// The outline's deadline is the wall clock from its start; every
			// later call has the same one.
			if d, ok := ctx.Deadline(); ok {
				mu.Lock()
				if first.IsZero() {
					first = d
					c.left = time.Until(d).Round(time.Second)
				} else {
					c.left = d.Sub(first)
				}
				mu.Unlock()
			}
			mu.Lock()
			seen = append(seen, c)
			mu.Unlock()
			if c.stage == agent.StageOutline && slow > 0 {
				time.Sleep(slow)
			}
			return replies(ctx, r, n)
		}
		f.backend.CompleteFunc = keepTemplate(t, "", nil)
		o := tilesOptions(t, f)
		o.Budgets.WallClock, o.Budgets.MaxCalls, o.Budgets.MaxTokens = wall, calls, tokens
		if !tiles {
			o.Tiles = nil
		}
		result, err := generate.Run(context.Background(), o)
		return seen, result, err
	}
	for _, c := range []struct {
		wall          time.Duration
		calls, tokens int
		slow          time.Duration
	}{
		{wall: time.Minute}, {wall: 0}, {wall: time.Minute, calls: 16}, {wall: time.Minute, calls: 5, tokens: 400},
		// The outline takes most of a short wall clock.
		{wall: 4 * time.Second, slow: 3300 * time.Millisecond},
	} {
		off, plain, errOff := run(t, c.wall, c.calls, c.tokens, c.slow, false)
		on, arranged, errOn := run(t, c.wall, c.calls, c.tokens, c.slow, true)
		if errOff != nil || errOn != nil {
			t.Fatalf("%+v: off %v, on %v", c, errOff, errOn)
		}
		if !slices.Equal(off, on) {
			t.Errorf("%+v: the analysis differs with tiles on:\n%+v\n%+v", c, off, on)
		}
		if !bytes.Equal(plain.Document, arranged.Document) || arranged.Composition == nil {
			t.Errorf("%+v: review or composition", c)
		}
	}
}

// A caller whose deadline passes while the view call works does not lose
// the finished review: the call keeps its own bound and the version is
// stored with its composition.
func TestTilesCallerDeadlineDuringComposition(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	answer := keepTemplate(t, "sec_rest", nil)
	f.backend.CompleteFunc = func(c context.Context, r agent.CompleteRequest, n int) (agent.Completion, error) {
		<-ctx.Done()
		if c.Err() != nil {
			t.Error("the view call's context ended with the caller's deadline")
		}
		return answer(c, r, n)
	}
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(ctx, s, key, tilesOptions(t, f), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	latest, err := s.Latest(context.Background(), key)
	if err != nil || len(latest.Composition) == 0 || result.Compose.Fallback {
		t.Fatalf("not stored with its composition: %v %+v", err, result.Compose)
	}
}

// The version keeps the analysis's model, which prices the run, however the
// view call's model is named.
func TestTilesKeepTheAnalysisModel(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	_, _, result := publishTiles(t, f, tilesOptions(t, f))
	if result.Outcome.Model != "stub-model" || result.Metadata.Model != "stub-model" {
		t.Errorf("model %q, metadata %q", result.Outcome.Model, result.Metadata.Model)
	}
}

// A panic while presenting the review costs neither the review nor the
// daemon: in the compose stage the version is published without a
// composition, and in a draft's composition the draft is shown without one.
func TestTilesContainPanics(t *testing.T) {
	t.Run("compose", func(t *testing.T) {
		defer generate.SetViewRun(func(context.Context, agent.ViewRequest) (agent.ViewOutcome, error) { panic("bug") })()
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(nil)
		s, key, result := publishTiles(t, f, tilesOptions(t, f))
		latest, err := s.Latest(context.Background(), key)
		if err != nil || len(latest.Composition) != 0 || result.Compose.Problem == "" {
			t.Fatalf("%v, composition %s, compose %+v", err, latest.Composition, result.Compose)
		}
		if !strings.Contains(f.progress.String(), "the review is published without a composition") {
			t.Errorf("progress:\n%s", f.progress.String())
		}
	})
	t.Run("draft", func(t *testing.T) {
		defer generate.SetProvisionalView(func(*view.Review, *tiles.Document, []tiles.LibraryTile, map[string][]string) view.Composition {
			panic("bug")
		})()
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(nil)
		f.backend.CompleteFunc = keepTemplate(t, "", nil)
		o := tilesOptions(t, f)
		var mu sync.Mutex
		var drafts []generate.Draft
		o.OnDraft = func(d generate.Draft) {
			mu.Lock()
			defer mu.Unlock()
			drafts = append(drafts, d)
		}
		_, _, result := publishTiles(t, f, o)
		if len(drafts) < 2 || drafts[0].Composition != nil || result.Composition == nil {
			t.Fatalf("%d drafts, first composition %s", len(drafts), drafts[0].Composition)
		}
	})
}
