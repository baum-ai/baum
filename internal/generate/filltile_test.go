package generate_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/tiles/library"
)

// apiChangeData is api_change data citing a hunk.
func apiChangeData(hunk string) string {
	return fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the widgets client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, hunk)
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
