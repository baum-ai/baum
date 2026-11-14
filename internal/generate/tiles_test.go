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
	"github.com/baum-ai/baum/tiles/compose"
)

var tilesNow = func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) }

// tilesOptions is a staged run with tile reviews on, at a fixed time and
// without a token budget, which would leave the composition no room.
func tilesOptions(t *testing.T, f *fixture) generate.Options {
	o := stagedOptions(t, f)
	o.Budgets.MaxTokens = 0
	o.Now = tilesNow
	o.Tiles = &generate.Tiles{}
	return o
}

// inventoryOf reads the inventory a completion request carries.
func inventoryOf(t *testing.T, r agent.CompleteRequest) compose.Inventory {
	t.Helper()
	var inv compose.Inventory
	if err := json.Unmarshal([]byte(r.User[strings.Index(r.User, "\n")+1:]), &inv); err != nil {
		t.Errorf("the inventory does not decode: %v", err)
	}
	return inv
}

// keepTemplate answers with the template's layout, as the inventory gives it,
// with lead first at half when set, and promote's promotions placed first.
func keepTemplate(t *testing.T, lead string, promote func(compose.Inventory) []compose.ReplyPromotion) func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
	return func(_ context.Context, r agent.CompleteRequest, _ int) (agent.Completion, error) {
		inv := inventoryOf(t, r)
		reply := compose.Reply{Entries: []compose.ReplyEntry{}, Promote: []compose.ReplyPromotion{}, Moves: []tiles.Move{}}
		if promote != nil {
			reply.Promote = promote(inv)
			for _, p := range reply.Promote {
				reply.Entries = append(reply.Entries, compose.ReplyEntry{Tile: p.ID, Size: tiles.SizeHalf})
			}
		}
		why := "it needs a decision"
		if lead != "" {
			reply.Entries = append(reply.Entries, compose.ReplyEntry{Tile: lead, Size: tiles.SizeHalf, Why: &why})
		}
		for _, entry := range inv.Template.Entries {
			if entry.Tile == lead {
				continue
			}
			e := compose.ReplyEntry{Tile: entry.Tile, Size: entry.Size}
			if entry.Group != "" {
				group := entry.Group
				e.Group = &group
			}
			reply.Entries = append(reply.Entries, e)
		}
		raw, err := json.Marshal(reply)
		return agent.Completion{Document: raw, Model: "stub-composer", Usage: agent.Usage{Input: 3000, Output: 400}}, err
	}
}

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

// With tile reviews off nothing is arranged: no completion, no composition,
// drafts without a layout. With them on, the review published is the same
// bytes; only the layout is added, and what the composer spent is counted.
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
			t.Fatal("a draft has a layout")
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
		t.Errorf("the composer's call is not counted: %+v against %+v", arranged.Metadata, plain.Metadata)
	}
	// The cost is priced from these attempts, the composer's among them.
	if last := arranged.Metadata.Accounting.Attempts[len(arranged.Metadata.Accounting.Attempts)-1]; last.Model != "stub-composer" || last.Usage.Input != 3000 {
		t.Errorf("the composer's attempt %+v", last)
	}
}

// With tile reviews on, the composer's layout is stored with the version,
// with its generator, and the tile route serves it.
func TestTilesOnStoresAndServesTheComposition(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	f.backend.CompleteFunc = keepTemplate(t, "sec_rest", nil)
	s, key, result := publishTiles(t, f, tilesOptions(t, f))
	latest, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	c := storedComposition(t, latest.Composition)
	if c.Generator == nil || c.Generator.Fallback || c.Generator.Model != "stub-composer" || c.Generator.PromptVersion == "" || c.Generator.ComposedAt == "" {
		t.Fatalf("generator %+v", c.Generator)
	}
	if result.Compose == nil || result.Compose.Fallback || result.Compose.Problem != "" || f.backend.CompletionCount() != 1 {
		t.Fatalf("compose %+v", result.Compose)
	}
	request := f.backend.Completions[0]
	if request.System == "" || !strings.Contains(request.User, "sec_rest") || len(request.Schema) == 0 {
		t.Errorf("the request %+v", request)
	}
	if inv := inventoryOf(t, request); inv.PullRequest.Previous || inv.Previous != nil {
		t.Errorf("a first version was offered a previous one")
	}
	served := tileview.Build(latest.Document, latest.Composition, tiles.Options{})
	if served.Tiles == nil || served.Problem != "" {
		t.Fatalf("served %q", served.Problem)
	}
	if served.Tiles.Origin.Kind != tiles.OriginGenerated || served.Tiles.Layout.Entries[0].Tile != "overview" && served.Tiles.Layout.Entries[0].Tile != "sec_rest" {
		t.Errorf("served origin %q, layout %+v", served.Tiles.Origin.Kind, served.Tiles.Layout.Entries)
	}
	if i := slices.IndexFunc(served.Tiles.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == "sec_rest" }); i < 0 || served.Tiles.Layout.Entries[i].Group != "" {
		t.Errorf("the lead change is not featured: %+v", served.Tiles.Layout.Entries)
	}
	if !strings.Contains(f.progress.String(), "tiles arranged") {
		t.Errorf("progress:\n%s", f.progress.String())
	}
	f.requireClean(t)
}

// Whatever goes wrong with the composer, the review is published: an invalid
// reply or a provider error falls back to the template, stored as such; a
// backend that cannot complete gets the template without a call; and a run
// with too little time or budget left skips the call.
func TestTilesFallBackAndStillPublish(t *testing.T) {
	cases := []struct {
		name     string
		complete func(context.Context, agent.CompleteRequest, int) (agent.Completion, error)
		options  func(*generate.Options)
		ctx      func() (context.Context, context.CancelFunc)
		reason   string
		calls    int
	}{
		{name: "invalid reply", reason: agent.FallbackInvalid, calls: 2,
			complete: func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
				return agent.Completion{Document: []byte(`{"entries":"no"}`), Model: "stub-composer"}, nil
			}},
		{name: "provider error", reason: agent.FallbackProvider, calls: 1,
			complete: func(context.Context, agent.CompleteRequest, int) (agent.Completion, error) {
				return agent.Completion{Model: "stub-composer", Usage: agent.Usage{Input: 10}}, errors.New("overloaded")
			}},
		{name: "cannot complete", reason: agent.FallbackNoCompleter},
		{name: "deadline nearly spent", reason: agent.FallbackNoTime,
			complete: keepTemplate(t, "", nil),
			// The job's own deadline comes long before the wall clock's
			// reserve is reached, leaving less than the composer needs.
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Second)
			}},
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
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if c.ctx != nil {
				ctx, cancel = c.ctx()
			}
			defer cancel()
			s, err := store.Open("", t.TempDir(), true)
			if err != nil {
				t.Fatal(err)
			}
			key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
			result, err := generate.Publish(ctx, s, key, o, nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, f.progress.String())
			}
			latest, err := s.Latest(context.Background(), key)
			if err != nil || latest.Metadata.Version != 1 {
				t.Fatalf("not published: %v", err)
			}
			stored := storedComposition(t, latest.Composition)
			if stored.Generator == nil || !stored.Generator.Fallback || !slices.Equal(stored.Generator.Problems, []string{c.reason}) {
				t.Fatalf("generator %+v", stored.Generator)
			}
			if !result.Compose.Fallback || result.Compose.FallbackReason != c.reason || f.backend.CompletionCount() != c.calls {
				t.Errorf("compose %+v, %d call(s)", result.Compose, f.backend.CompletionCount())
			}
			if !strings.Contains(f.progress.String(), "tiles laid out by the template: "+c.reason) {
				t.Errorf("progress:\n%s", f.progress.String())
			}
			f.requireClean(t)
		})
	}
}

// A run cancelled while the composer works stops there: nothing is stored,
// the run is interrupted as at any other point, and what the composer was
// asked counts as spent.
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

// Drafts are laid out by the template while the review is written, then
// once by the composition; a composer that keeps the template moves nothing
// on the page.
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
		c := storedComposition(t, d.Composition)
		if c.Generator != nil || c.Empty() {
			t.Fatalf("a provisional layout %+v", c)
		}
	}
	landing := func(d generate.Draft) []tiles.Entry {
		served := tileview.Build(d.Document, d.Composition, tiles.Options{Draft: true})
		if served.Tiles == nil {
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
		t.Errorf("the page jumped:\n%+v\n%+v", before, after)
	}
	if !bytes.Equal(result.Compose.Provisional, drafts[len(drafts)-2].Composition) {
		t.Errorf("the reported provisional layout is not the last draft's")
	}
}

// withTable adds a table block to sec_changes, which a composer may promote.
func withTable(d map[string]any) {
	generatetest.TwoSections(d)
	section := d["sections"].([]any)[0].(map[string]any)
	hunk := section["hunks"].([]string)[0]
	section["blocks"] = []any{map[string]any{"kind": "table", "columns": []any{"Setting", "Default"},
		"rows": []any{[]any{"timeout", "30s"}, []any{"retries", "3"}}, "source": []any{map[string]any{"type": "hunk", "id": hunk}}}}
}

// A second version is offered the first's composition, and a block promoted
// again keeps the ID it had.
func TestTilesSecondVersionKeepsPromotedIDs(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(func(stage string, request agent.Request) (agent.Result, error, bool) {
		document, err := generatetest.Document(request.Directory, withTable)
		return agent.Result{Document: generatetest.ForStage(request, document), Model: "stub-model", Calls: 1, Tokens: 100}, err, true
	})
	promote := func(id string) func(compose.Inventory) []compose.ReplyPromotion {
		return func(inv compose.Inventory) []compose.ReplyPromotion {
			for _, tile := range inv.Tiles {
				for _, block := range tile.Blocks {
					if block.Kind == tiles.TypeTable {
						return []compose.ReplyPromotion{{Block: block.Handle, ID: id, Title: "Defaults"}}
					}
				}
			}
			t.Error("the inventory offers no table")
			return nil
		}
	}
	f.backend.CompleteFunc = keepTemplate(t, "", promote("table_defaults"))
	s, key, _ := publishTiles(t, f, tilesOptions(t, f))
	first, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if c := storedComposition(t, first.Composition); len(c.Promotions) != 1 || c.Promotions[0].ID != "table_defaults" {
		t.Fatalf("first promotions %+v", c.Promotions)
	}
	f.backend.CompleteFunc = keepTemplate(t, "", promote("table_settings"))
	if _, err := generate.Publish(context.Background(), s, key, tilesOptions(t, f), nil); err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	second, err := s.Latest(context.Background(), key)
	if err != nil || second.Metadata.Version != 2 {
		t.Fatalf("version %d: %v", second.Metadata.Version, err)
	}
	inv := inventoryOf(t, f.backend.Completions[len(f.backend.Completions)-1])
	if !inv.PullRequest.Previous || inv.Previous == nil || len(inv.Previous.Promoted) != 1 || inv.Previous.Promoted[0].ID != "table_defaults" {
		t.Fatalf("the previous composition was not offered: %+v", inv.Previous)
	}
	if c := storedComposition(t, second.Composition); len(c.Promotions) != 1 || c.Promotions[0].ID != "table_defaults" {
		t.Errorf("second promotions %+v", c.Promotions)
	}
	served := tileview.Build(second.Document, second.Composition, tiles.Options{})
	if served.Tiles == nil || !slices.ContainsFunc(served.Tiles.Tiles, func(tile tiles.Tile) bool { return tile.ID == "table_defaults" }) {
		t.Errorf("the promoted tile is not served: %q", served.Problem)
	}
}
