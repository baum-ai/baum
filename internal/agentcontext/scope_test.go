package agentcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func listed(t *testing.T, s *Server, token string) map[string]bool {
	t.Helper()
	r := rpc(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if r.Error != nil || json.Unmarshal(r.Result, &list) != nil {
		t.Fatal(r.Error, string(r.Result))
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestActionsNeitherListNorCallGlobalTools(t *testing.T) {
	s := newServer(t)
	called := 0
	for _, tool := range []Tool{
		{Name: "all_pulls", Description: "Every pull request.", Call: func(context.Context, CallContext, json.RawMessage) (string, error) { called++; return "all", nil }},
		{Name: "this_pull", Description: "One pull request.", Scope: ScopeRepository, Call: func(context.Context, CallContext, json.RawMessage) (string, error) { return "one", nil }},
	} {
		if err := s.RegisterTool(tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RegisterPage(PageSpec{Name: PagePullRequest, Tools: []string{"all_pulls", "this_pull"}}); err != nil {
		t.Fatal(err)
	}
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	chatToken, _ := chat(t, s)
	if names := listed(t, s, action); names["all_pulls"] || !names["this_pull"] || !names["current_page"] || !names["read_more"] {
		t.Fatalf("action lists %v", names)
	}
	if names := listed(t, s, chatToken); !names["all_pulls"] || !names["this_pull"] {
		t.Fatalf("chat lists %v", names)
	}
	r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"all_pulls"}}`)
	if r.Error == nil || r.Error.Code != codeInvalidParams || called != 0 {
		t.Fatalf("an action called a global tool: %+v %s", r.Error, r.Result)
	}
	if data, _ := unwrap(t, call(t, s, action, "current_page", nil).Content[0].Text); !strings.Contains(data, `"this_pull"`) || strings.Contains(data, "all_pulls") {
		t.Fatalf("an action's page names a global tool: %s", data)
	}
	if r := call(t, s, chatToken, "all_pulls", nil); r.IsError || called != 1 {
		t.Fatal("a chat could not call a global tool")
	}
}

func TestRevokeDuringACallLeavesNothing(t *testing.T) {
	s := newServer(t)
	settings := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: settings}); err != nil {
		t.Fatal(err)
	}
	var revoke func()
	for _, tool := range []Tool{
		{Name: "long", Description: "Long.", Call: func(context.Context, CallContext, json.RawMessage) (string, error) {
			revoke()
			return strings.Repeat("x", 2*MaxChunk), nil
		}},
		{Name: "propose", Description: "Proposes.", Call: func(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
			_, err := c.Propose(ctx, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"new"`)})
			return "", err
		}},
	} {
		if err := s.RegisterTool(tool); err != nil {
			t.Fatal(err)
		}
	}
	token, _, r := mint(t, s, Consumer{Kind: Chat})
	revoke = r
	reply := rpc(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"long"}}`)
	if reply.Error == nil {
		t.Fatalf("a revoked consumer got a cut result: %s", reply.Result)
	}
	if len(s.envelope.pending) != 0 {
		t.Fatal("a continuation was kept for a revoked consumer")
	}

	// The proposer's validation runs after Propose's first check; a revoke
	// then must still keep the proposal from being stored.
	token, id, r := mint(t, s, Consumer{Kind: Chat})
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	settings.onValidate = r
	if result := call(t, s, token, "propose", nil); !result.IsError || !strings.Contains(result.Content[0].Text, "revoked") {
		t.Fatalf("a proposal was recorded for a revoked consumer: %+v", result)
	}
	if len(s.Proposals.List(id)) != 0 || len(s.Proposals.open) != 0 {
		t.Fatal("a proposal was kept for a revoked consumer")
	}
}
