package replay_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/tiles/view"
)

// completing is a backend that also completes: it answers the view call
// (viewCompletion).
type completing struct {
	*generatetest.Backend
	calls atomic.Int32
}

// CanComplete overrides the stub's, which completes only with a CompleteFunc.
func (c *completing) CanComplete() bool { return true }

func (c *completing) Complete(_ context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	c.calls.Add(1)
	if string(r.Schema) == string(view.ReplySchema()) {
		return viewCompletion(r)
	}
	return agent.Completion{}, errors.New("not a view call")
}

// viewCompletion answers a view call: a flow on the first section, citing
// its first hunk, and a risk summary.
func viewCompletion(r agent.CompleteRequest) (agent.Completion, error) {
	var in view.Input
	if err := json.Unmarshal([]byte(r.User[strings.Index(r.User, "\n")+1:]), &in); err != nil {
		return agent.Completion{}, err
	}
	reply := map[string]any{
		"overview": map[string]any{"risk_summary": "Small and covered.", "verdict": nil, "screenshots": []any{}, "diagram": nil, "intent": []any{}},
		"sections": []any{}, "diagrams": []any{},
	}
	if len(in.Sections) > 0 && len(in.Sections[0].Hunks) > 0 {
		s := in.Sections[0]
		reply["sections"] = []any{map[string]any{"id": s.ID, "tiles": []any{"dg_first"}}}
		reply["diagrams"] = []any{map[string]any{"id": "dg_first", "type": "flow", "title": "How it runs", "caption": nil, "source": []any{s.Hunks[0].ID},
			"lanes": []any{map[string]any{"label": nil, "steps": []any{map[string]any{"title": "Before", "note": nil, "tone": nil, "join": nil},
				map[string]any{"title": "After", "note": nil, "tone": "new", "join": "arrow"}}}}}}
	}
	raw, err := json.Marshal(reply)
	return agent.Completion{Document: raw, Model: "claude-opus-5-5", Usage: agent.Usage{Input: 5000, Output: 500}}, err
}
