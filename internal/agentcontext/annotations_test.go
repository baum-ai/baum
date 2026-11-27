package agentcontext

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// tools/list tells a client which tools only read, so it can approve them
// without asking, and which change state.
func TestToolsListAnnotatesEveryTool(t *testing.T) {
	s := newServer(t)
	if err := s.RegisterTool(Tool{Name: "propose_thing", Description: "d", Proposes: true,
		Call: func(context.Context, CallContext, json.RawMessage) (string, error) { return "", nil }}); err != nil {
		t.Fatal(err)
	}
	token, _ := chat(t, s)
	r := rpc(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Tools []struct {
			Name        string
			Annotations map[string]any
		}
	}
	if err := json.Unmarshal(r.Result, &list); err != nil || len(list.Tools) < 3 {
		t.Fatalf("tools/list: %v %s", err, r.Result)
	}
	for _, tool := range list.Tools {
		a := tool.Annotations
		if a == nil || a["openWorldHint"] != false {
			t.Errorf("%s: annotations %v", tool.Name, a)
			continue
		}
		// A proposing tool records a proposal; add_tile, and a tool that
		// makes a tile, changes the person's own page at once. Neither only
		// reads, and neither destroys anything.
		edits := slices.Contains(addPageTools, tool.Name) || slices.Contains(tileMakingTools, tool.Name)
		if strings.HasPrefix(tool.Name, "propose_") || edits && tool.Name != "tile_type" && tool.Name != "tile_guide" {
			if a["readOnlyHint"] != false || a["destructiveHint"] != false {
				t.Errorf("%s: annotations %v", tool.Name, a)
			}
			continue
		}
		if a["readOnlyHint"] != true {
			t.Errorf("%s: annotations %v, want read-only", tool.Name, a)
		}
		if _, ok := a["destructiveHint"]; ok {
			t.Errorf("%s: a read-only tool carries destructiveHint", tool.Name)
		}
	}
}
