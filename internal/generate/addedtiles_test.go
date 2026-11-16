package generate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// apiChangeData is api_change data citing a hunk.
func apiChangeData(hunk string) string {
	return fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the widgets client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, hunk)
}

// addedReplies answers as analysisReplies does, with no exhibits and the
// outline saying api_change does not apply, and the api_change fill with
// fill (pass number from 1), which defaults to data on the first hunk.
func addedReplies(t *testing.T, fill func(pass int, request agent.Request) (agent.Result, error)) func(context.Context, agent.Request, int) (agent.Result, error) {
	base := analysisReplies(t, "", nil)
	passes := 0
	return func(ctx context.Context, request agent.Request, n int) (agent.Result, error) {
		if request.Stage != "fill-api-change" {
			return base(ctx, request, n)
		}
		passes++
		if fill != nil {
			return fill(passes, request)
		}
		h := firstHunk(t, request.Directory)
		return agent.Result{Document: []byte(`{"applies":true,"data":` + apiChangeData(h.ID) + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
}

func fillCalls(f *fixture, stage string) int {
	n := 0
	for _, r := range f.backend.Requests {
		if r.Stage == stage {
			n++
		}
	}
	return n
}

func libraryNames(c *tiles.Composition) []string {
	var out []string
	for _, lib := range c.Library {
		var def struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(lib.Definition, &def)
		out = append(out, def.Name)
	}
	return out
}

// A type the person added is filled whatever the outline answers about it,
// told that the person added it, and appears in the new version's
// composition beside the types the outline selected.
func TestAnAddedTypeIsAlwaysFilled(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = addedReplies(t, nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change"}}
	_, _, result := publishTiles(t, f, o)
	c := storedComposition(t, result.Composition)
	if names := libraryNames(c); len(names) != 2 || !strings.Contains(strings.Join(names, ","), "api_change") {
		t.Fatalf("library tiles: %v", names)
	}
	if fillCalls(f, "fill-api-change") != 1 {
		t.Errorf("%d api_change fills", fillCalls(f, "fill-api-change"))
	}
	for _, r := range f.backend.Requests {
		if r.Stage == "fill-api-change" && !strings.Contains(r.Prompt, "The person reading this review added this tile") {
			t.Error("the fill was not told the person added the tile")
		}
	}
}

// A type the person added whose last data cites only hunks this diff has,
// with no new hunk in a file it cites, is reused without a call; a new hunk
// in a cited file makes it filled again.
func TestAnAddedTypesLastDataIsReused(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = addedReplies(t, nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change"}}
	s, key, first := publishTiles(t, f, o)
	doc, err := tiles.FromReview(mustNormalize(t, first.Document), tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	diff := library.DiffOf(doc)
	var hunks []string
	for _, h := range diff {
		hunks = append(hunks, h.ID)
	}
	last := json.RawMessage(apiChangeData(diff[0].ID))
	o.Tiles.Previous = nil
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change", Last: last, LastHunks: hunks}}
	before := fillCalls(f, "fill-api-change")
	second, err := generate.Publish(context.Background(), s, key, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fillCalls(f, "fill-api-change") != before {
		t.Errorf("the reusable data was filled again")
	}
	reused := false
	for _, fill := range second.Compose.Tiles.Fills {
		reused = reused || fill.Type == "api_change" && fill.Reused && fill.State == agent.FillReady
	}
	if !reused || !strings.Contains(strings.Join(libraryNames(storedComposition(t, second.Composition)), ","), "api_change") {
		t.Fatalf("not reused: %+v", second.Compose.Tiles.Fills)
	}
	// The cited hunk is new to this diff: it touches a cited file.
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change", Last: last, LastHunks: hunks[1:]}}
	if _, err := generate.Publish(context.Background(), s, key, o, nil); err != nil {
		t.Fatal(err)
	}
	if fillCalls(f, "fill-api-change") != before+1 {
		t.Errorf("data citing a changed file was reused")
	}
}

// An added type's fill that fails is dropped, and the review is published
// without its tile; one that first answers the type does not apply is sent
// back once and filled.
func TestAnAddedTypesFillIsDroppedOrRepaired(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = addedReplies(t, func(int, agent.Request) (agent.Result, error) {
		return agent.Result{}, errors.New("the provider failed")
	})
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change"}}
	_, _, result := publishTiles(t, f, o)
	if names := libraryNames(storedComposition(t, result.Composition)); strings.Contains(strings.Join(names, ","), "api_change") {
		t.Errorf("a failed fill made a tile: %v", names)
	}

	f = newFixture(t)
	f.backend.RunFunc = addedReplies(t, func(pass int, request agent.Request) (agent.Result, error) {
		if pass == 1 {
			return agent.Result{Document: []byte(`{"applies":false,"data":null}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
		}
		if !strings.Contains(request.Prompt, "the person added this tile") {
			t.Error("the repair does not say the person added the tile")
		}
		return agent.Result{Document: []byte(`{"applies":true,"data":{"endpoints":[],"docs":null,"consumers":[]}}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	})
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o = tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	o.Tiles.Always = []agent.AlwaysTile{{Name: "api_change"}}
	_, _, result = publishTiles(t, f, o)
	if names := libraryNames(storedComposition(t, result.Composition)); !strings.Contains(strings.Join(names, ","), "api_change") {
		t.Errorf("the repaired fill made no tile: %v", names)
	}
}

// FillTile fills an added type outside a review: it assembles the pull
// request, makes one call with the type's fill schema, and checks and masks
// the data; data that still does not fit after the repair is dropped.
func TestFillTile(t *testing.T) {
	f := newFixture(t)
	entry, _ := library.Get("api_change")
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		if request.Stage != "fill-api-change" {
			t.Errorf("stage %q", request.Stage)
		}
		// The prompt gives the hunks with their IDs.
		id := regexp.MustCompile(`"id":"(h_[0-9a-f]+)"`).FindStringSubmatch(request.Prompt)
		if id == nil {
			t.Fatal("the prompt names no hunk")
		}
		return agent.Result{Document: []byte(`{"applies":true,"data":` + apiChangeData(id[1]) + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
	result, err := generate.FillTile(context.Background(), f.options(t), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != agent.FillReady || !strings.Contains(string(result.Data), "the widgets client") || result.Outcome.Calls != 1 {
		t.Fatalf("fill: %+v", result)
	}
	if len(f.backend.Requests) != 1 || !strings.Contains(f.backend.Requests[0].Prompt, "added this tile") {
		t.Errorf("requests: %d", len(f.backend.Requests))
	}

	f = newFixture(t)
	f.backend.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"applies":true,"data":` + apiChangeData("h_000000000000") + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
	result, err = generate.FillTile(context.Background(), f.options(t), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != agent.FillDropped || len(f.backend.Requests) != 2 {
		t.Errorf("bad data: %+v after %d calls", result, len(f.backend.Requests))
	}
}

func mustNormalize(t *testing.T, document []byte) []byte {
	t.Helper()
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}
