package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
	"github.com/baum-ai/baum/tiles/view"
)

// viewReply is a view call's reply over the example review: a flow on the
// overview, a table on sec_retry, a verdict, a screenshot and an intent.
const viewReply = `{"overview": {"risk_summary": "Failed jobs now retry on one shared policy.", "verdict": {"text": "Decide who may requeue jobs.", "points": [{"text": "The role comes from a header the caller sets; check it on the server.", "section": "sec_api"}]},
 "screenshots": ["att_736e4be0bc0c6f5e62e3ccb048891c8f"], "diagram": "dg_path",
 "intent": [{"asked": "Retries with a budget", "got": "Yes: a policy with backoff and an attempt budget.", "session": "785faefe-40b5-5521-9932-4dc2d659ec1d", "prompt": 1}]},
 "sections": [{"id": "sec_retry", "tiles": ["dg_waits"]}],
 "diagrams": [
  {"id": "dg_path", "type": "flow", "title": "What happens to a failed job", "caption": null, "source": ["h_141757b19901", "h_113c5f55c94f"],
   "lanes": [{"label": null, "steps": [{"title": "Worker runs the job", "note": null, "tone": null, "join": null}, {"title": "Dead letters", "note": "after the last attempt", "tone": "new", "join": "arrow"}]}]},
  {"id": "dg_waits", "type": "table", "title": "How long a job waits", "caption": null, "source": ["h_141757b19901"], "columns": ["Attempt", "Wait"], "rows": [["1", "Base"], ["2", "2 x Base"]]}]}`

func viewRun(t *testing.T, completer agent.Completer, mutate ...func(*agent.ViewRequest)) (agent.ViewOutcome, error) {
	t.Helper()
	normalized, doc := converted(t)
	r := agent.ViewRequest{Completer: completer, Backend: "claude", Prompts: prompt.TilesPrompts{}, Document: normalized, Converted: doc, Now: fixedNow}
	for _, m := range mutate {
		m(&r)
	}
	return agent.ViewRun(context.Background(), r)
}

func TestViewRunUsesTheReply(t *testing.T) {
	f := &fakeCompleter{replies: []string{viewReply}}
	out, err := viewRun(t, f)
	if err != nil {
		t.Fatal(err)
	}
	c := out.Composition
	if out.Fallback || out.Calls != 1 || len(c.Diagrams) != 2 || c.Overview.Verdict == nil || *c.Overview.Diagram != "dg_path" || len(c.Overview.Intent) != 1 {
		t.Fatalf("outcome %+v", out)
	}
	g := c.Generator
	if g == nil || g.PromptVersion != prompt.ViewVersion || g.Model != "claude-test" || g.ComposedAt != "2026-01-02T03:04:05Z" || g.Fallback {
		t.Fatalf("generator %+v", g)
	}
	request := f.requests[0]
	if string(request.Schema) != string(view.ReplySchema()) || !strings.Contains(request.User, `"sec_retry"`) || !strings.Contains(request.System, "Never use the word") {
		t.Fatalf("request %q", request.User[:120])
	}
	// The input lists the sessions' prompts but never a restricted session.
	if !strings.Contains(request.User, "785faefe") || strings.Contains(request.User, "fe59570b") || strings.Contains(request.User, "sec_fixed_delay") {
		t.Errorf("input: sessions or a stale section")
	}
}

// The library and authored tiles are in every composition: placed where the
// reply places them, else by their hunks.
func TestViewRunPlacesTheAnalysisTiles(t *testing.T) {
	authored := tiles.AuthoredTile{ID: "prose_why", Title: "Why a dead-letter table", Data: json.RawMessage(`{"text": "So an operator can look at a job.", "anchors": [{"type": "hunk", "id": "h_113c5f55c94f"}]}`)}
	table := tiles.AuthoredTile{ID: "table_routes", Title: "Routes", Data: json.RawMessage(`{"columns": ["Route", "Who"], "rows": [["/requeue", "operator"]], "source": [{"type": "hunk", "id": "h_578603ca5fc8"}]}`)}
	reply := strings.Replace(viewReply, `"tiles": ["dg_waits"]`, `"tiles": ["dg_waits", "table_routes"]`, 1)
	for name, tc := range map[string]struct {
		completer agent.Completer
		fallback  bool
	}{
		"reply":    {&fakeCompleter{replies: []string{reply}}, false},
		"fallback": {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := viewRun(t, tc.completer, func(r *agent.ViewRequest) {
				r.Authored = []compose.Authored{{Tile: authored, Adds: "why"}, {Tile: table}}
			})
			if err != nil || out.Fallback != tc.fallback {
				t.Fatalf("%v %v %q", err, out.Fallback, out.FallbackReason)
			}
			where := map[string]string{}
			for _, p := range out.Composition.Sections {
				for _, id := range p.Tiles {
					where[id] = p.ID
				}
			}
			if where["prose_why"] != "sec_deadletter" || len(out.Composition.Authored) != 2 {
				t.Fatalf("placements %v", where)
			}
			if tc.fallback {
				if where["table_routes"] != "sec_api" || len(out.Composition.Diagrams) != 0 || out.Composition.Overview.Verdict != nil ||
					!slices.Equal(out.Composition.Generator.Problems, []string{agent.FallbackNoCompleter}) || !out.Composition.Generator.Fallback {
					t.Fatalf("fallback %+v", out.Composition)
				}
			} else if where["table_routes"] != "sec_retry" {
				t.Fatalf("the reply placed table_routes on sec_retry: %v", where)
			}
			if out.Composition.Generator.Fixes[view.FixPlaced] == 0 {
				t.Errorf("fixes %v", out.Composition.Generator.Fixes)
			}
		})
	}
}

func TestViewRunRepairsOnceThenFallsBack(t *testing.T) {
	f := &fakeCompleter{replies: []string{`{"overview": [`, viewReply}}
	out, err := viewRun(t, f)
	if err != nil || out.Fallback || !out.Repaired || out.Calls != 2 || !strings.Contains(f.requests[1].User, "Your previous reply was rejected") {
		t.Fatalf("repaired: %v %+v", err, out)
	}
	f = &fakeCompleter{replies: []string{`[]`, `[]`}}
	out, err = viewRun(t, f)
	if err != nil || !out.Fallback || out.FallbackReason != agent.FallbackViewInvalid || out.Calls != 2 {
		t.Fatalf("invalid twice: %v %+v", err, out)
	}
	f = &fakeCompleter{replies: []string{`[]`}}
	out, _ = viewRun(t, f, func(r *agent.ViewRequest) { r.MaxCalls = 1 })
	if !out.Fallback || out.Calls != 1 {
		t.Fatalf("one call: %+v", out)
	}
	f = &fakeCompleter{errs: []error{errors.New("down")}}
	out, _ = viewRun(t, f)
	if !out.Fallback || out.FallbackReason != agent.FallbackViewProvider {
		t.Fatalf("provider: %+v", out)
	}
	f = &fakeCompleter{}
	out, _ = viewRun(t, f, func(r *agent.ViewRequest) { r.Skip = agent.FallbackBudget })
	if !out.Fallback || out.FallbackReason != agent.FallbackBudget || len(f.requests) != 0 {
		t.Fatalf("skip: %+v", out)
	}
}

func TestViewRunInterruptedAndTimeouts(t *testing.T) {
	normalized, doc := converted(t)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeCompleter{}
	out, err := agent.ViewRun(done, agent.ViewRequest{Completer: f, Prompts: prompt.TilesPrompts{}, Document: normalized, Converted: doc})
	if !errors.Is(err, agent.ErrViewInterrupted) || len(f.requests) != 0 || out.Composition.Generator != nil {
		t.Errorf("before: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f = &fakeCompleter{block: true, cancel: cancel}
	out, err = agent.ViewRun(ctx, agent.ViewRequest{Completer: f, Prompts: prompt.TilesPrompts{}, Document: normalized, Converted: doc})
	if !errors.Is(err, agent.ErrViewInterrupted) || out.Calls != 1 || out.Composition.Generator != nil {
		t.Errorf("during: %v", err)
	}
	f = &fakeCompleter{block: true}
	out, err = viewRun(t, f, func(r *agent.ViewRequest) { r.Timeout = 20 * time.Millisecond })
	if err != nil || !out.Fallback || out.FallbackReason != agent.FallbackViewProvider {
		t.Errorf("stage timeout: %v %+v", err, out)
	}
	if _, err := agent.ViewRun(context.Background(), agent.ViewRequest{Document: []byte(`{}`)}); err == nil {
		t.Error("a document that is not a review")
	}
}
