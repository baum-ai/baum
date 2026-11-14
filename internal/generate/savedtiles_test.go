package generate_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/tiles/library"
)

// savedEntry is a saved type of the person's library: the built-in api_change
// type under another name and title, so its definition is not built in.
func savedEntry(t *testing.T, name string) library.Entry {
	t.Helper()
	builtin, _ := library.Get("api_change")
	e := builtin
	def := builtin.Definition
	def.Name, def.Title = name, "Saved "+name
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	e.Definition, e.Encoded = def, encoded
	if library.IsBuiltin(e.Encoded) {
		t.Fatal("the saved entry is built in")
	}
	return e
}

// savedReplies answers as addedReplies does, and each saved type's fill
// with api_change data on the first hunk, counting its calls.
func savedReplies(t *testing.T, calls map[string]int, mu *sync.Mutex) func(context.Context, agent.Request, int) (agent.Result, error) {
	base := addedReplies(t, nil)
	return func(ctx context.Context, request agent.Request, n int) (agent.Result, error) {
		if !strings.HasPrefix(request.Stage, "fill-saved-") {
			return base(ctx, request, n)
		}
		mu.Lock()
		calls[request.Stage]++
		mu.Unlock()
		h := firstHunk(t, request.Directory)
		return agent.Result{Document: []byte(`{"applies":true,"data":` + apiChangeData(h.ID) + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
}

// A saved type pinned to the repository is filled whatever the outline
// answers, told that its instructions are the person's, and its tile is
// placed in the composition; an offered saved type is listed to the outline
// with its glance, and the outline did not select it, so it is not filled.
// Saved is asked once, with the diff's paths and the types the run offers
// already.
func TestSavedTypesArePinnedAndOffered(t *testing.T) {
	f := newFixture(t)
	calls := map[string]int{}
	var mu sync.Mutex
	f.backend.RunFunc = savedReplies(t, calls, &mu)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	pinned, offered := savedEntry(t, "saved_pinned"), savedEntry(t, "saved_offered")
	asked := 0
	var gotPaths, gotTaken []string
	o.Tiles.Saved = func(_ context.Context, diffPaths, taken []string) ([]library.Entry, []library.Entry) {
		asked++
		gotPaths, gotTaken = diffPaths, taken
		return []library.Entry{pinned}, []library.Entry{offered}
	}
	_, _, result := publishTiles(t, f, o)
	if asked != 1 || len(gotPaths) == 0 || !slices.Equal(gotTaken, library.Names) {
		t.Errorf("Saved asked %d times with paths %v, taken %v", asked, gotPaths, gotTaken)
	}
	if calls["fill-saved-pinned"] != 1 || calls["fill-saved-offered"] != 0 {
		t.Errorf("fills: %v", calls)
	}
	names := libraryNames(storedComposition(t, result.Composition))
	if !slices.Contains(names, "saved_pinned") || slices.Contains(names, "saved_offered") {
		t.Errorf("library tiles: %v", names)
	}
	for _, r := range f.backend.Requests {
		switch {
		case r.Stage == agent.StageOutline && !strings.Contains(r.Prompt, "- `saved_offered` (Saved saved_offered; from the person's tile library): ") ||
			r.Stage == agent.StageOutline && !strings.Contains(r.Prompt, "Its glance shows: "+offered.Glance):
			t.Errorf("the outline was not offered the saved type with its glance")
		case r.Stage == "fill-saved-pinned" && !strings.Contains(r.Prompt, "This type is from the person's own tile library"):
			t.Error("the saved type's fill does not frame its instructions as the person's")
		case r.Stage == "fill-schema-change" && strings.Contains(r.Prompt, "person's own tile library"):
			t.Error("a built-in type's fill is framed as a saved type's")
		}
	}
	if used := fillsReady(result); !slices.Contains(used, "saved_pinned") {
		t.Errorf("ready fills: %v", used)
	}
}

func fillsReady(r generate.Result) []string {
	var out []string
	if r.Compose == nil || r.Compose.Tiles == nil || r.Compose.Tiles.TilesOutcome == nil {
		return nil
	}
	for _, f := range r.Compose.Tiles.Fills {
		if f.State == agent.FillReady {
			out = append(out, f.Type)
		}
	}
	return out
}

// Without Saved, or with no library offered, the run offers the built-in
// types only and never asks for saved ones.
func TestNoSavedTypesWithoutALibrary(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = analysisReplies(t, "", nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	o.Tiles.NoLibrary = true
	o.Tiles.Saved = func(context.Context, []string, []string) ([]library.Entry, []library.Entry) {
		t.Error("Saved was asked for a run with no library")
		return nil, nil
	}
	_, _, result := publishTiles(t, f, o)
	if c := storedComposition(t, result.Composition); len(c.Library) != 0 {
		raw, _ := json.Marshal(c.Library)
		t.Errorf("library tiles: %s", raw)
	}
}
