package jobs

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
)

// A details retry of a baum.review/1 version that carries a composition
// keeps it: the retry's drafts show it, and the completed version keeps it.
func TestDetailsRetryKeepsTheComposition(t *testing.T) {
	ctx := context.Background()
	failing := true
	without := generatetest.WithoutSection("sec_rest")
	stub := &generatetest.Backend{Version: "9.9.9"}
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
	result, err := generate.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	composition := tileviewtest.ViewComposition(t)
	if err := versions.Put(ctx, key, store.Version{}, store.Version{Metadata: result.Metadata, Document: result.Document, Composition: composition}); err != nil {
		t.Fatal(err)
	}
	saved, err := versions.Latest(ctx, key)
	if err != nil || len(saved.Composition) == 0 {
		t.Fatalf("composition %s: %v", saved.Composition, err)
	}

	failing = false
	mu.Lock()
	reports = nil
	mu.Unlock()
	if _, err := (&Local{}).completeDetails(ctx, versions, nil, key, Prepared{Item: Item{Kind: KindDetails, Version: 1}}, o); err != nil {
		t.Fatal(err)
	}
	completed, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if failed, _ := generate.Gaps(completed.Document); len(failed) != 0 || !bytes.Equal(completed.Composition, saved.Composition) {
		t.Errorf("failed %v, composition kept %v", failed, bytes.Equal(completed.Composition, saved.Composition))
	}
	drafts := 0
	for _, p := range reports {
		if p.Draft != nil {
			drafts++
			if !bytes.Equal(p.DraftComposition, saved.Composition) {
				t.Error("a draft of the retry does not carry the version's composition")
			}
		}
	}
	if drafts == 0 {
		t.Error("the retry reported no draft")
	}
}

// With tile reviews on, the review's prompts are resolved for the
// repository, and a custom one marks the version's prompt source.
func TestTileReviewUsesTheRepositorysPrompts(t *testing.T) {
	registry, clone := registeredClone(t)
	setOverride(t, registry, clone.Repository, prompt.IDReviewMap, "MAP FOR ACME")
	o := generate.Options{Staged: true, Tiles: &generate.Tiles{}, V2: &generate.V2{}}
	if err := (&Local{Registry: registry}).applyPrompts(context.Background(), clone.Repository, &o); err != nil {
		t.Fatal(err)
	}
	p, err := o.V2.Prompts.ReviewV2Instructions(context.Background(), "claude")
	if err != nil || !strings.Contains(p.Map, "MAP FOR ACME") || o.PromptSource != "repository" {
		t.Errorf("%v %q", err, o.PromptSource)
	}
	plain := generate.Options{Staged: true}
	if err := (&Local{Registry: registry}).applyPrompts(context.Background(), clone.Repository, &plain); err != nil || plain.PromptSource != "" {
		t.Errorf("without tiles: %v %q", err, plain.PromptSource)
	}
}
