package agentcontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	r := NewRegistry()
	token, id, revoke, err := r.Mint(Consumer{Kind: Chat, ID: "chosen"})
	if err != nil {
		t.Fatal(err)
	}
	other, otherID, _, err := r.Mint(Consumer{Kind: Chat})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{token, id, other, otherID} {
		if b, err := base64.RawURLEncoding.DecodeString(value); err != nil || len(b) != 32 {
			t.Fatalf("%q is not 32 random bytes", value)
		}
	}
	if token == other || token == id || id == "chosen" || id == otherID {
		t.Fatal("tokens and ids must be independent")
	}
	c, ok := r.Authenticate(token)
	if !ok || c.ID != id || c.Kind != Chat {
		t.Fatalf("%+v %v", c, ok)
	}
	for _, wrong := range []string{"", id, token[:len(token)-1], token + "x", strings.ToUpper(token)} {
		if _, ok := r.Authenticate(wrong); ok {
			t.Fatalf("%q authenticated", wrong)
		}
	}
	revoke()
	revoke()
	if _, ok := r.Authenticate(token); ok {
		t.Fatal("revoked token authenticated")
	}
	if _, ok := r.Consumer(id); ok {
		t.Fatal("revoked consumer found")
	}
	if _, ok := r.Authenticate(other); !ok {
		t.Fatal("revoking one token revoked another")
	}
}

func TestMintValidatesTheConsumer(t *testing.T) {
	r := NewRegistry()
	for _, c := range []Consumer{
		{},
		{Kind: "admin"},
		{Kind: Chat, Repository: "acme/web"},
		{Kind: Chat, PullRequest: 1},
		{Kind: Action},
		{Kind: Action, Repository: "acme/web"},
		{Kind: Action, Repository: "acme/web", PullRequest: -1},
		{Kind: Action, Repository: "acme", PullRequest: 1},
		{Kind: Action, Repository: "acme/web/extra", PullRequest: 1},
		{Kind: Action, Repository: "github.com/acme/web", PullRequest: 1},
		{Kind: Action, Repository: "acme/..", PullRequest: 1},
		{Kind: Action, Repository: " acme/web", PullRequest: 1},
		{Kind: Action, Repository: "-acme/web", PullRequest: 1},
	} {
		if token, _, revoke, err := r.Mint(c); err == nil || token != "" || revoke != nil {
			t.Fatalf("minted %+v", c)
		}
	}
	if len(r.byID) != 0 {
		t.Fatal("a refused consumer was stored")
	}
	for _, c := range []Consumer{{Kind: Chat}, {Kind: Action, Repository: "acme/web.js", PullRequest: 1}} {
		if _, _, _, err := r.Mint(c); err != nil {
			t.Fatal(c, err)
		}
	}
}

func TestPageIsACopy(t *testing.T) {
	r := NewRegistry()
	_, id, _, _ := r.Mint(Consumer{Kind: Chat})
	filters := map[string]string{"q": "a"}
	if err := r.SetPage(id, PageContext{Page: PageInbox, Filters: filters}); err != nil {
		t.Fatal(err)
	}
	filters["q"] = "b"
	page, _ := r.Page(id)
	page.Filters["q"] = "c"
	if page, _ := r.Page(id); page.Filters["q"] != "a" {
		t.Fatal("the stored page was changed from outside")
	}
	for _, bad := range []PageContext{
		{Page: PageInbox, Focus: json.RawMessage(`{`)},
		{Page: PageInbox, Focus: json.RawMessage(`"` + strings.Repeat("x", maxFocus) + `"`)},
		{Page: PageInbox, Repository: strings.Repeat("x", maxPageText+1)},
	} {
		if err := r.SetPage(id, bad); err == nil {
			t.Fatal("accepted a page over the limits")
		}
	}
}

func TestActionIsBoundToItsPullRequest(t *testing.T) {
	s := newServer(t)
	var targets []string
	if err := s.RegisterTool(Tool{
		Name: "read_pr", Description: "Reads a pull request's jobs.", Scope: ScopeRepository, Reads: []string{"/api/jobs"},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string"},"number":{"type":"integer"}}}`),
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
			}
			_ = json.Unmarshal(args, &a)
			repository, number, err := c.Target(a.Repository, a.Number)
			if err != nil {
				return "", err
			}
			targets = append(targets, repository+"#"+strconv.Itoa(number))
			response, err := c.Reader.Get(ctx, "/api/jobs?repository="+repository+"&pr="+strconv.Itoa(number))
			return string(response.Body), err
		},
	}); err != nil {
		t.Fatal(err)
	}
	token, id, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	page, ok := s.Registry.Page(id)
	if !ok || page.Page != PagePullRequest || page.Repository != "acme/web" || page.PullRequest != 12 {
		t.Fatalf("action page %+v", page)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err == nil {
		t.Fatal("an action's page moved")
	}
	if r := call(t, s, token, "read_pr", map[string]any{}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, token, "read_pr", map[string]any{"repository": "ACME/web", "number": 3}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, token, "read_pr", map[string]any{"repository": "evil/secrets"}); !r.IsError || !strings.Contains(r.Content[0].Text, "only its own pull request's repository") {
		t.Fatalf("another repository was read: %+v", r)
	}
	if strings.Join(targets, ",") != "acme/web#12,acme/web#3" {
		t.Fatal(targets)
	}
	// An action cannot propose, whatever page it names.
	if err := s.RegisterPage(PageSpec{Name: PagePullRequest, Proposer: &fakeProposer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Propose(context.Background(), id, Proposal{Page: PagePullRequest, Target: "x", After: json.RawMessage(`1`)}); !errors.Is(err, ErrReadOnly) {
		t.Fatal(err)
	}

	chatToken, chatID := chat(t, s)
	if r := call(t, s, chatToken, "read_pr", map[string]any{}); !r.IsError {
		t.Fatal("a chat without a page read a default repository")
	}
	if err := s.Registry.SetPage(chatID, PageContext{Page: PagePullRequest, Repository: "other/repo", PullRequest: 4}); err != nil {
		t.Fatal(err)
	}
	if r := call(t, s, chatToken, "read_pr", map[string]any{}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, chatToken, "read_pr", map[string]any{"repository": "third/repo", "number": 1}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if got := strings.Join(targets[2:], ","); got != "other/repo#4,third/repo#1" {
		t.Fatal(got)
	}
}

// A tool that skips Target cannot take an action outside its repository:
// the action's reader reads only the routes in actionRoutes, each bound the
// way its handler finds what it serves.
func TestActionReaderIsBoundToItsRepository(t *testing.T) {
	saved := actionRoutes
	t.Cleanup(func() { actionRoutes = saved })
	actionRoutes = append(append([]actionRoute(nil), saved...),
		actionRoute{pattern: "/api/clonecheck", bind: byClone},
		actionRoute{pattern: "/api/repositories/{owner}/{name}/clone-target", bind: byPath},
	)
	var seen []string
	jobs := map[string]string{"j-web": "acme/web", "j-other": "evil/secrets"}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/jobs/"); ok {
			// A transcript names no repository.
			if job, ok := strings.CutSuffix(id, "/transcript"); ok {
				_, _ = fmt.Fprintf(w, `{"entries":[],"job":%q}`, job)
				return
			}
			repository, found := jobs[id]
			if !found {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"repository":%q,"title":"secret title"}`, id, repository)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	clones := map[string]string{"c-web": "acme/web", "c-other": "evil/secrets"}
	s := New(Options{API: api, Clone: func(_ context.Context, id string) (string, error) {
		if repository, ok := clones[id]; ok {
			return repository, nil
		}
		return "", errors.New("unknown clone")
	}})
	if err := s.RegisterTool(Tool{
		Name: "raw_read", Description: "Reads a path as given.", Scope: ScopeRepository,
		Reads:       []string{"/api/prs", "/api/jobs", "/api/clonecheck", "/api/repositories", "/api/timeline"},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct{ Path string }
			_ = json.Unmarshal(args, &a)
			response, err := c.Reader.Get(ctx, a.Path)
			return string(response.Body), err
		},
	}); err != nil {
		t.Fatal(err)
	}
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	chatToken, _ := chat(t, s)
	for _, tt := range []struct {
		path    string
		allowed bool
		reached bool
	}{
		{"/api/jobs?repository=ACME/web&pr=12", true, true},
		{"/api/jobs/j-web", true, true},
		{"/api/jobs/j-web/transcript?after=3", true, true},
		{"/api/clonecheck?clone=c-web", true, true},
		{"/api/repositories/acme/web/clone-target", true, true},
		// No handler reads repo=, and /api/prs is not an action's route.
		{"/api/prs?repo=acme/web", false, false},
		{"/api/prs?repository=acme/web", false, false},
		{"/api/jobs", false, false},
		{"/api/jobs?pr=12", false, false},
		{"/api/jobs?repository=evil/secrets&pr=1", false, false},
		{"/api/jobs?repository=acme/web&repository=evil/x&pr=1", false, false},
		{"/api/jobs?repository=acme/web&pr=12&limit=5", false, false},
		{"/api/jobs?repository=acme/web&pr=12&refresh=1", false, false},
		{"/api/clonecheck?clone=c-other", false, false},
		{"/api/clonecheck?clone=unknown", false, false},
		{"/api/repositories/evil/secrets/clone-target", false, false},
		{"/api/timeline/decisions", false, false},
		// Found by id: the job answered, but it is another repository's, so
		// nothing of it comes back.
		{"/api/jobs/j-other", false, true},
		{"/api/jobs/missing", false, true},
		// A transcript names no repository: the job it belongs to is read
		// first, and the transcript only when that job is the action's.
		{"/api/jobs/j-other/transcript", false, true},
		{"/api/jobs/missing/transcript", false, true},
		{"/api/jobs/j-web/transcript?after=3&limit=5", false, false},
	} {
		seen = nil
		r := call(t, s, action, "raw_read", map[string]string{"path": tt.path})
		if r.IsError == tt.allowed || (len(seen) > 0) != tt.reached {
			t.Errorf("action read %s: reached %v: %s", tt.path, seen, r.Content[0].Text)
		}
		if strings.HasSuffix(strings.Split(tt.path, "?")[0], "/transcript") && !tt.allowed {
			for _, got := range seen {
				if strings.Contains(got, "/transcript") {
					t.Errorf("%s read the transcript of another repository's job: %v", tt.path, seen)
				}
			}
		}
		// The error names the path asked for, nothing the API answered.
		if (strings.Contains(r.Content[0].Text, "evil/secrets") && !strings.Contains(tt.path, "evil/secrets")) || (!tt.allowed && strings.Contains(r.Content[0].Text, "secret title")) {
			t.Errorf("%s leaked: %s", tt.path, r.Content[0].Text)
		}
	}
	// A chat is bound to no repository; it reads its tool's prefixes.
	if r := call(t, s, chatToken, "raw_read", map[string]string{"path": "/api/jobs/j-other"}); r.IsError || !strings.Contains(r.Content[0].Text, "evil/secrets") {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, chatToken, "raw_read", map[string]string{"path": "/api/prs?refresh=1"}); !r.IsError {
		t.Fatal("a chat asked for a refresh")
	}
}
