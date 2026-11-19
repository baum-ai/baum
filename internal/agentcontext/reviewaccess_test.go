package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// readsServer is a server with every built-in tool over readsDaemon, and a
// Review token for acme/web#13.
func readsServer(t *testing.T) (*Server, *readsDaemon, string) {
	t.Helper()
	d := &readsDaemon{}
	clones := map[string]string{"c-web": "acme/web", "c-evil": "evil/secrets"}
	s := New(Options{API: d, Version: "test", Clone: func(_ context.Context, id string) (string, error) {
		if repository, ok := clones[id]; ok {
			return repository, nil
		}
		return "", errors.New("unknown clone")
	}})
	token, revoke, err := s.Registry.MintReview("acme/web", 13)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	return s, d, token
}

func listedTools(t *testing.T, s *Server, token string) []string {
	t.Helper()
	var names []string
	for name := range listed(t, s, token) {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestReviewTokensSeeEveryReadAndNoWrite(t *testing.T) {
	s, _, token := readsServer(t)
	got := listedTools(t, s, token)
	want := slices.Clone(reviewReadTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("a review lists\n%v\nwant\n%v", got, want)
	}
	// Global reads too: search, the lists, the Timeline, transcripts.
	for _, name := range []string{"search", "pull_requests", "timeline_transcript", "live_session_transcript", "analytics", "decisions"} {
		if !slices.Contains(got, name) {
			t.Errorf("a review lacks %s", name)
		}
	}
	// Every tool it lists says it is read-only.
	r := rpc(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Annotations["readOnlyHint"] != true {
			t.Errorf("%s is listed to a review but not read-only", tool.Name)
		}
	}
	// No proposal, operation, edit, tile tool or the person's own state, to
	// list or to call.
	for _, name := range []string{"pending_review", "review_conversations", "focus", "propose_merge", "propose_review_comment", "propose_reviews",
		"propose_fix", "propose_resolve_thread", "add_tile", "start_tile", "check_tile", "add_custom_tile", "propose_tile_type", "tile_type"} {
		if slices.Contains(got, name) {
			t.Errorf("a review lists %s", name)
		}
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
		if r := rpc(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`); r.Error == nil || r.Error.Code != codeInvalidParams {
			t.Errorf("a review called %s: %+v %s", name, r.Error, r.Result)
		}
	}
}

// Every tool is classified: a review's read, a chat-only read, or a write. A
// new tool fails here until it is, and until then a review never sees it.
func TestEveryToolIsClassifiedForReviews(t *testing.T) {
	s, _, _ := readsServer(t)
	names := map[string]bool{}
	for _, tool := range s.tools {
		names[tool.Name] = true
		review, chatOnly := slices.Contains(reviewReadTools, tool.Name), slices.Contains(chatOnlyReadTools, tool.Name)
		switch {
		case tool.writes() && (review || chatOnly):
			t.Errorf("%s writes but is listed as a read", tool.Name)
		case review && chatOnly:
			t.Errorf("%s is both a review's read and a chat's only", tool.Name)
		case !tool.writes() && !review && !chatOnly:
			t.Errorf("%s only reads: list it in reviewReadTools or chatOnlyReadTools", tool.Name)
		}
	}
	for _, name := range slices.Concat(reviewReadTools, chatOnlyReadTools) {
		if !names[name] {
			t.Errorf("%s is listed but no such tool exists", name)
		}
	}
}

func TestNewToolsAreHiddenFromReviewsUntilListed(t *testing.T) {
	s, _, token := readsServer(t)
	if err := s.RegisterTool(Tool{Name: "new_read", Description: "Reads.", Scope: ScopeRepository,
		Call: func(context.Context, CallContext, json.RawMessage) (string, error) { return "x", nil }}); err != nil {
		t.Fatal(err)
	}
	chatToken, _ := chat(t, s)
	if !listed(t, s, chatToken)["new_read"] || listed(t, s, token)["new_read"] {
		t.Fatal("a tool not listed for reviews is visible to one, or a chat lacks it")
	}
	// A listed name that writes is still hidden, and New refuses it.
	if (Tool{Name: "search", Proposes: true}).reviewReadable() || (Tool{Name: "search", Edits: true}).reviewReadable() {
		t.Fatal("a writing tool is readable by a review")
	}
	if err := checkReviewTools([]Tool{{Name: "diff", Edits: true}}); err == nil {
		t.Fatal("a review's read list accepted a tool that edits")
	}
}

func TestReviewsNeverWrite(t *testing.T) {
	s, d, token := readsServer(t)
	consumer, _ := s.Registry.Authenticate(token)
	if consumer.Kind != Review || consumer.Repository != "acme/web" || consumer.PullRequest != 13 {
		t.Fatalf("minted %+v", consumer)
	}
	if err := s.Registry.SetPage(consumer.ID, PageContext{Page: PageSettings}); err == nil {
		t.Error("a review's page moved")
	}
	if err := s.Registry.SetTurnPage(consumer.ID, PageContext{Page: PageSettings}); err == nil {
		t.Error("a review's turn page was set")
	}
	if _, err := s.Proposals.Propose(context.Background(), consumer.ID, Proposal{Page: PagePullRequest}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a review proposed: %v", err)
	}
	if _, err := s.Proposals.ProposeOperation(context.Background(), consumer.ID, "merge", json.RawMessage(`{}`)); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a review proposed an operation: %v", err)
	}
	// A listed tool that reaches for the person's own state is refused by
	// the reader, whatever it declares, and nothing is read.
	peek := Tool{Name: "peek", Description: "Peeks.", Scope: ScopeRepository, InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`), Reads: []string{"/api/ui-state", "/api/prs/chat", "/api/prs/checks"},
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct{ Path string }
			_ = json.Unmarshal(args, &a)
			r, err := c.Reader.Get(ctx, a.Path)
			if err != nil {
				return "", err
			}
			return string(r.Body), nil
		}}
	if err := s.RegisterTool(peek); err != nil {
		t.Fatal(err)
	}
	reviewReadTools = append(reviewReadTools, "peek")
	t.Cleanup(func() { reviewReadTools = reviewReadTools[:len(reviewReadTools)-1] })
	d.reset()
	for _, path := range []string{"/api/ui-state/review?key=acme%2Fweb%2313", "/api/prs/chat?clone=c-web&pr=13"} {
		if text, failed := data(t, s, token, "peek", map[string]any{"path": path}); !failed || !strings.Contains(text, "not a route this tool reads") {
			t.Errorf("a review read %s: %s", path, text)
		}
	}
	if len(d.requests()) != 0 {
		t.Errorf("refused reads reached the daemon: %v", d.requests())
	}
	if _, failed := data(t, s, token, "peek", map[string]any{"path": "/api/prs/checks?repository=acme%2Fweb&pr=13"}); failed {
		t.Error("a review could not read checks")
	}
	for _, request := range d.requests() {
		if !strings.HasPrefix(request, "GET ") {
			t.Errorf("a review sent %s", request)
		}
	}
}

func TestReviewToolsDefaultToItsPullRequestAndReadAnyOther(t *testing.T) {
	s, d, token := readsServer(t)
	for _, tt := range []struct {
		tool string
		args any
		want string
	}{
		{"checks", nil, "GET /api/prs/checks?head_sha=h13&pr=13&repository=acme%2Fweb"},
		{"checks", map[string]any{"repository": "acme/web"}, "GET /api/prs/checks?head_sha=h13&pr=13&repository=acme%2Fweb"},
		{"checks", map[string]any{"pr": 12}, "GET /api/prs/checks?head_sha=h12&pr=12&repository=acme%2Fweb"},
		// Another repository is read like the review's own.
		{"checks", map[string]any{"repository": "evil/secrets", "pr": 1}, "GET /api/prs/checks?head_sha=hx&pr=1&repository=evil%2Fsecrets"},
	} {
		d.reset()
		if text, failed := data(t, s, token, tt.tool, tt.args); failed {
			t.Errorf("%s %v failed: %s", tt.tool, tt.args, text)
			continue
		}
		if !slices.Contains(d.requests(), tt.want) {
			t.Errorf("%s %v read %v, want %s", tt.tool, tt.args, d.requests(), tt.want)
		}
	}
	// Its page is its pull request.
	if text, _ := data(t, s, token, "current_page", nil); !strings.Contains(text, `"pull_request": 13`) || !strings.Contains(text, `"git_log"`) || strings.Contains(text, `"pending_review"`) {
		t.Errorf("a review's page: %s", text)
	}
}

func TestReviewMCPAttach(t *testing.T) {
	var m ReviewMCP
	if server, revoke, err := m.Attach("acme/web", 13); server != nil || err != nil {
		t.Fatalf("an unserved ReviewMCP attached %v %v", server, err)
	} else {
		revoke()
	}
	s, _, _ := readsServer(t)
	url := ""
	m.Serve(s.Registry, func() string { return url })
	if server, _, err := m.Attach("acme/web", 13); server != nil || err != nil {
		t.Fatalf("attached before the app server listens: %v %v", server, err)
	}
	url = "http://127.0.0.1:4321/api/mcp"
	if _, _, err := m.Attach("not a repository", 13); err == nil {
		t.Fatal("attached a review of no repository")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			server, revoke, err := m.Attach("acme/web", 13)
			if err != nil || server == nil || server.URL != url {
				t.Errorf("attach: %v %v", server, err)
				return
			}
			consumer, ok := s.Registry.Authenticate(server.Token)
			if !ok || consumer.Kind != Review || consumer.PullRequest != 13 {
				t.Errorf("the token is %+v %v", consumer, ok)
			}
			revoke()
			if _, ok := s.Registry.Authenticate(server.Token); ok {
				t.Error("a revoked review token still authenticates")
			}
		})
	}
	wg.Wait()
}
