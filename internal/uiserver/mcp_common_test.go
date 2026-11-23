package uiserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// fakeLive answers GET /api/live-sessions as the daemon's session list does.
func fakeLive() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/live-sessions" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"sessions": []map[string]any{
			{"agent": "claude", "session_id": "s-1", "title": "Search the repo", "repository": "owner/repo", "branch": "search", "waiting_message": "private"},
		}})
	})
}

// fakeTimeline answers the Timeline's features and decisions for any query.
func fakeTimeline() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repository := map[string]string{"id": "owner/repo", "full_name": "owner/repo"}
		switch {
		case r.Method != http.MethodGet:
			http.NotFound(w, r)
		case r.URL.Path == "/api/timeline/features":
			writeJSON(w, map[string]any{"features": []map[string]any{{"id": "f-1", "title": "Search everywhere", "repository": repository}}, "next_cursor": nil})
		case r.URL.Path == "/api/timeline/decisions":
			writeJSON(w, map[string]any{"decisions": []map[string]any{{"id": "d-1", "summary": "Search is local only", "category": "architecture", "repository": repository}}, "next_cursor": nil})
		default:
			http.NotFound(w, r)
		}
	})
}

// daemon_status and search read through the daemon's LocalAPI: the review
// index, the groups store and the registry are real; the background service
// is real but holds no state, and the queue's list, live sessions and Timeline
// are fakes that answer the shapes the real routes do.
func TestCommonToolsReadTheRealRoutes(t *testing.T) {
	f := newPullFixture(t)
	chatToken, _, revokeChat, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeChat()
	actionToken, _, revokeAction, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: "owner/repo", PullRequest: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeAction()

	text, failed := f.callTool(t, chatToken, "daemon_status", nil)
	if failed || !strings.Contains(text, `"health":"ok"`) || !strings.Contains(text, `"version":"test"`) || !strings.Contains(text, `"repositories":[]`) {
		t.Fatalf("daemon_status: %v %s", failed, text)
	}
	// The queue's two running jobs, as the fake queue lists them.
	if !strings.Contains(text, `"running":2`) || !strings.Contains(text, `"queued":0`) {
		t.Errorf("queue depth: %s", text)
	}
	for _, leak := range []string{"secret", "/home/", "accounting", "j-running"} {
		if strings.Contains(text, leak) {
			t.Errorf("status carries %q: %s", leak, text)
		}
	}

	// Every source answers: the review index (with its two clones' entries),
	// the groups store, the session list, the registry and the Timeline.
	text, failed = f.callTool(t, chatToken, "search", map[string]any{"query": "search"})
	if failed {
		t.Fatalf("search: %s", text)
	}
	for _, want := range []string{
		`"kind":"pull_request","title":"Add search"`, `"opens":{"page":"pull_request","repository":"owner/repo","pull_request":7}`,
		`"kind":"group","title":"Search"`, `"kind":"session","title":"Search the repo"`, `"session":"claude:s-1"`,
		`"kind":"feature","title":"Search everywhere"`, `"kind":"decision","title":"Search is local only"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("search lacks %s: %s", want, text)
		}
	}
	if strings.Contains(text, "unavailable") || strings.Contains(text, "private") || strings.Contains(text, "Another repository") {
		t.Errorf("search: %s", text)
	}
	text, _ = f.callTool(t, chatToken, "search", map[string]any{"query": "owner/repo", "kinds": []string{"repository"}})
	if !strings.Contains(text, `"kind":"repository","title":"owner/repo"`) || !strings.Contains(text, `"page":"repositories"`) {
		t.Errorf("repository search: %s", text)
	}
	if text, failed = f.callTool(t, chatToken, "search", map[string]any{"query": "x", "limit": 51}); !failed {
		t.Errorf("limit 51: %s", text)
	}

	// An action has neither tool; the call is refused as an unknown tool.
	for _, name := range []string{"search", "daemon_status"} {
		r := rawCall(t, f, actionToken, name)
		if !strings.Contains(r, "unknown tool: "+name) {
			t.Errorf("an action called %s: %s", name, r)
		}
	}
}

// rawCall is a tools/call of name, answered raw.
func rawCall(t *testing.T, f *pullFixture, token, name string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"query":"x"}}}`
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	return w.Body.String()
}
