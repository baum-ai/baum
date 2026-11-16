package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
	"github.com/baum-ai/baum/tiles/view"
)

// templateCompletion answers a view call with a reply that writes nothing,
// and a composition with the template's layout.
func templateCompletion(_ context.Context, r agent.CompleteRequest, _ int) (agent.Completion, error) {
	if string(r.Schema) == string(view.ReplySchema()) {
		raw := []byte(`{"overview": {"risk_summary": null, "verdict": null, "screenshots": [], "diagram": null, "intent": []}, "sections": [], "diagrams": []}`)
		return agent.Completion{Document: raw, Model: "stub-composer", Usage: agent.Usage{Input: 2000, Output: 300}}, nil
	}
	var inv compose.Inventory
	if err := json.Unmarshal([]byte(r.User[strings.Index(r.User, "\n")+1:]), &inv); err != nil {
		return agent.Completion{}, err
	}
	reply := compose.Reply{Entries: []compose.ReplyEntry{}, Promote: []compose.ReplyPromotion{}, Moves: []tiles.Move{}}
	for _, entry := range inv.Template.Entries {
		e := compose.ReplyEntry{Tile: entry.Tile, Size: entry.Size}
		if entry.Group != "" {
			group := entry.Group
			e.Group = &group
		}
		reply.Entries = append(reply.Entries, e)
	}
	raw, err := json.Marshal(reply)
	return agent.Completion{Document: raw, Model: "stub-composer", Usage: agent.Usage{Input: 2000, Output: 300}}, err
}

// A job's backend forwards the composer's call and reports it as the other
// calls: the phase is "arranging", its call, tokens and usage count, and the
// transcript names it. A backend that cannot complete says so.
func TestTrackedBackendReportsTheComposition(t *testing.T) {
	var mu sync.Mutex
	var reports []Progress
	var entries []agent.TranscriptEntry
	stub := &generatetest.Backend{Version: "9.9.9", CompleteFunc: templateCompletion}
	b := &trackedBackend{Backend: stub, model: "claude-sonnet-4-5", progress: func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, p)
	}, transcript: func(e agent.TranscriptEntry) { entries = append(entries, e) }}
	if generate.CompleterOf(b) == nil {
		t.Fatal("the job's backend cannot complete")
	}
	request := agent.CompleteRequest{System: "s", User: "inventory\n{\"template\":{\"entries\":[{\"tile\":\"overview\",\"size\":\"half\"}]}}"}
	if _, err := b.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	last := reports[len(reports)-1]
	if reports[0].Phase != "arranging" || last.Calls != 1 || last.Tokens != 2300 || last.Accounting == nil || len(last.Accounting.Attempts) != 1 || last.CostUSD == nil {
		t.Fatalf("reports %+v", reports)
	}
	if len(entries) != 2 || entries[0].Kind != "call" || entries[0].Title != "Composition" || entries[1].Status != "completed" {
		t.Errorf("transcript %+v", entries)
	}
	cannot := &trackedBackend{Backend: &generatetest.Backend{}, progress: func(Progress) {}}
	if generate.CompleterOf(cannot) != nil {
		t.Error("a backend without completions offers them")
	}
	if _, err := cannot.Complete(context.Background(), request); !errors.Is(err, generate.ErrCannotComplete) {
		t.Errorf("err %v", err)
	}
}

// A review published through a job's backend with tile reviews on: its drafts
// carry the template's layout, then the composition, which the draft route
// serves; the version keeps it, and a details retry later leaves it as it
// is and asks no composer.
func TestTileReviewThroughAJob(t *testing.T) {
	ctx := context.Background()
	failing := true
	without := generatetest.WithoutSection("sec_rest")
	stub := &generatetest.Backend{Version: "9.9.9", CompleteFunc: templateCompletion}
	stub.RunFunc = generatetest.StagedReplies(func(stage string, request agent.Request) (agent.Result, error, bool) {
		if failing {
			return without(stage, request)
		}
		return agent.Result{}, nil, false
	})
	var mu sync.Mutex
	var reports []Progress
	backend := &trackedBackend{Backend: stub, progress: func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, p)
	}}
	o := reviewOptions(t, stub)
	o.Backend, o.Staged, o.Budgets.MaxCalls, o.Budgets.MaxTokens = backend, true, 16, 0
	o.OnDraft = backend.draft
	o.Tiles = &generate.Tiles{}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(ctx, versions, key, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := versions.Latest(ctx, key)
	if err != nil || len(saved.Composition) == 0 || !slices.Equal(result.FailedSections, []string{"sec_rest"}) {
		t.Fatalf("composition %s, failed %v: %v", saved.Composition, result.FailedSections, err)
	}
	var drafts []Progress
	arranging := false
	for _, p := range reports {
		if p.Draft != nil {
			drafts = append(drafts, p)
		}
		arranging = arranging || p.Phase == "arranging"
	}
	if !arranging || len(drafts) < 2 {
		t.Fatalf("arranging %v, %d draft(s)", arranging, len(drafts))
	}
	final := drafts[len(drafts)-1]
	if !bytes.Equal(final.DraftComposition, result.Composition) || !bytes.Equal(final.Draft, result.Document) {
		t.Error("the last draft is not the finished review with its composition")
	}
	if provisional := drafts[0].DraftComposition; len(provisional) == 0 || bytes.Equal(provisional, final.DraftComposition) {
		t.Error("the first draft has no provisional layout of its own")
	}
	// The queue serves each draft with its layout.
	var d draftTiles
	body, err := d.get("job", 1, final.Draft, final.DraftComposition, tileview.Layout{})
	if err != nil {
		t.Fatal(err)
	}
	var served struct {
		Tiles struct {
			Origin struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(body, &served); err != nil || served.Tiles.Origin.Kind != tiles.OriginGenerated {
		t.Errorf("the final draft is served %q: %v", served.Tiles.Origin.Kind, err)
	}
	if tileview.Key(final.DraftComposition) == "" {
		t.Error("the draft's layout has no cache key")
	}

	// The retry writes sec_rest and keeps the composition.
	failing = false
	completions := stub.CompletionCount()
	if _, err := (&Local{}).completeDetails(ctx, versions, nil, key, Prepared{Item: Item{Kind: KindDetails, Version: 1}}, o); err != nil {
		t.Fatal(err)
	}
	completed, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if failed, _ := generate.Gaps(completed.Document); len(failed) != 0 || !bytes.Equal(completed.Composition, saved.Composition) || stub.CompletionCount() != completions {
		t.Errorf("failed %v, composition kept %v, %d completion(s) more", failed, bytes.Equal(completed.Composition, saved.Composition), stub.CompletionCount()-completions)
	}
}

// With tile reviews on, the view prompt is resolved for the
// repository like the review's, and a custom one marks the version's prompt
// source.
func TestTileReviewUsesTheRepositorysViewPrompt(t *testing.T) {
	registry, clone := registeredClone(t)
	setOverride(t, registry, clone.Repository, prompt.IDReviewView, "ARRANGE FOR ACME")
	o := generate.Options{Staged: true, Tiles: &generate.Tiles{}}
	if err := (&Local{Registry: registry}).applyPrompts(context.Background(), clone.Repository, &o); err != nil {
		t.Fatal(err)
	}
	p, err := o.Tiles.Prompts.ViewInstructions(context.Background(), "claude")
	if err != nil || !strings.Contains(p.Body, "ARRANGE FOR ACME") || !strings.HasSuffix(p.Version, prompt.CustomSuffix) || o.PromptSource != "repository" {
		t.Errorf("%v %q %q", err, p.Version, o.PromptSource)
	}
	plain := generate.Options{Staged: true}
	if err := (&Local{Registry: registry}).applyPrompts(context.Background(), clone.Repository, &plain); err != nil || plain.PromptSource != "" || plain.Tiles != nil {
		t.Errorf("without tiles: %v %q", err, plain.PromptSource)
	}
}

// A repository's standing tile preferences reach a tile review's options
// (generate.Tiles.Preferences), and a repository without any gives none. The
// view call, which replaced the tile grid's composition, does not read them.
func TestTileReviewCarriesTheRepositorysPreferences(t *testing.T) {
	ctx := context.Background()
	registry, _ := registeredClone(t)
	repository := assembletest.Repository.String()
	if _, err := registry.Pick(ctx, repository, nil); err != nil {
		t.Fatal(err)
	}
	compose1 := func() *compose.Preferences {
		t.Helper()
		versions, err := store.Open("", t.TempDir(), true)
		if err != nil {
			t.Fatal(err)
		}
		key := store.Key{Repository: repository, Number: assembletest.Number}
		p := (&Local{Registry: registry}).tilesFor(ctx, versions, key, repository).Preferences
		if p.Text == "" && len(p.Pins) == 0 {
			return nil
		}
		return &p
	}
	if got := compose1(); got != nil {
		t.Errorf("without settings the review carries %+v", got)
	}
	if _, err := registry.UpdateRepositoryIf(ctx, repository, func(s repos.Settings) (repos.Settings, error) {
		s.Tiles = &repos.TileSettings{Text: "Lead with what changed in the data model.", Pins: []string{"risks"}}
		return s, nil
	}); err != nil {
		t.Fatal(err)
	}
	got := compose1()
	if got == nil || got.Text != "Lead with what changed in the data model." || !slices.Equal(got.Pins, []string{"risks"}) {
		t.Errorf("the composer read %+v", got)
	}
}
