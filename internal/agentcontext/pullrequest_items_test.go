package agentcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestPullRequestShowsLatestReviewAndGroups(t *testing.T) {
	s, _ := fakeServer(t)
	chatToken, _ := chat(t, s)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 33})
	defer revoke()
	for _, token := range []string{chatToken, action} {
		text, failed := data(t, s, token, "pull_request", map[string]any{"repository": "acme/web", "pr": 33})
		if failed {
			t.Fatal(text)
		}
		for _, want := range []string{`"version":1`, `"backend":"claude"`, `"model":"opus"`, `"generated_at":"2026-11-01T10:00:00Z"`, `"recorded_cost_usd":0.5`, `"merge_base":"b33"`} {
			if !strings.Contains(text, want) {
				t.Errorf("latest lacks %s: %s", want, text)
			}
		}
		for _, hidden := range []string{"session", "accounting", "digest", "policy", "/home/secret"} {
			if strings.Contains(text, hidden) {
				t.Errorf("pull_request shows %s: %s", hidden, text)
			}
		}
	}
	// The groups and stacks it is a member of, of its repository only.
	text, _ := data(t, s, chatToken, "pull_request", map[string]any{"repository": "acme/web", "pr": 12})
	var out struct {
		Groups []groupRef `json:"groups"`
		Inbox  map[string]any
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || len(out.Groups) != 2 || out.Groups[0].ID != "g-web" || out.Groups[1].ID != "g-stack" || !out.Groups[1].Stack {
		t.Fatalf("groups %s: %v", text, err)
	}
	if strings.Contains(text, "g-evil") || strings.Contains(text, "g-solo") {
		t.Errorf("another group: %s", text)
	}
}

func TestActionGroupDefaultsToItsPullRequestsOnlyGroup(t *testing.T) {
	s, _ := fakeServer(t)
	// Pull request 20 is in the stack group alone.
	one, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 20})
	defer revoke()
	for _, tool := range []string{"group", "group_versions"} {
		if text, failed := data(t, s, one, tool, nil); failed || !strings.Contains(text, "g-stack") {
			t.Errorf("%s of the only group: %v %s", tool, failed, text)
		}
	}
	// Pull request 12 is in two, 99 in none: the agent must name one.
	for pr, want := range map[int]string{12: "g-web, g-stack", 99: "is in none"} {
		a, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: pr})
		defer revoke()
		if text, failed := data(t, s, a, "group", nil); !failed || !strings.Contains(text, want) {
			t.Errorf("pull request %d: %v %s", pr, failed, text)
		}
	}
	// Another repository's group is never its default.
	if text, failed := data(t, s, one, "group", map[string]any{"id": "g-evil"}); !failed || strings.Contains(text, "Loot") {
		t.Errorf("a foreign group: %v %s", failed, text)
	}
}

func TestMergeStateIncludesConflictingFiles(t *testing.T) {
	s, d := fakeServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	text, failed := data(t, s, action, "merge_state", nil)
	if failed || !strings.Contains(text, `"conflicts":{"route":"/api/prs/conflicts"`) || !strings.Contains(text, `"files":["conflict.go"]`) {
		t.Fatalf("a conflicting pull request: %v %s", failed, text)
	}
	d.reset()
	text, _ = data(t, s, action, "merge_state", map[string]any{"pr": 12})
	if strings.Contains(text, `"conflicts"`) {
		t.Errorf("conflicts of a pull request without any: %s", text)
	}
	for _, request := range d.requests() {
		if strings.Contains(request, "/api/prs/conflicts") {
			t.Errorf("read %s", request)
		}
	}
	// The action's reader binds the route to its repository.
	if text, failed := data(t, s, action, "merge_state", map[string]any{"repository": "evil/secrets"}); !failed {
		t.Errorf("another repository: %s", text)
	}
}

func TestChecksFallBackToFailingChecksOnlyWhenNotFound(t *testing.T) {
	s, d := fakeServer(t)
	token, _ := chat(t, s)
	d.reset()
	text, failed := data(t, s, token, "checks", map[string]any{"repository": "acme/web", "pr": 12})
	if failed || !strings.Contains(text, `"name":"lint"`) || !strings.Contains(text, `"failing":[{"name":"test"`) {
		t.Fatalf("checks: %v %s", failed, text)
	}
	if strings.Contains(strings.Join(d.requests(), " "), "failing-checks") {
		t.Errorf("failing-checks read although all checks answered: %v", d.requests())
	}
	// Failing means completed and failed: the running lint is not.
	if strings.Contains(strings.SplitN(text, `"failing"`, 2)[1], "lint") {
		t.Errorf("a running check is failing: %s", text)
	}
	d.reset()
	text, failed = data(t, s, token, "checks", map[string]any{"repository": "acme/web", "pr": 15})
	if failed || !strings.Contains(text, `"only_failing":true`) {
		t.Fatalf("old daemon: %v %s", failed, text)
	}
	if got := strings.Join(d.requests(), " "); !strings.Contains(got, "/api/prs/checks") || !strings.Contains(got, "/api/prs/failing-checks") {
		t.Errorf("requests %v", d.requests())
	}
}

func TestEntryPrefersASavedReviewThenAClone(t *testing.T) {
	s, _ := fakeServer(t)
	token, _ := chat(t, s)
	for pr, status := range map[int]string{31: "second", 32: "b"} {
		text, failed := data(t, s, token, "pull_request", map[string]any{"repository": "acme/web", "pr": pr})
		if failed || !strings.Contains(text, `"status":"`+status+`"`) {
			t.Errorf("pull request %d: %v %s", pr, failed, text)
		}
	}
}

// An index the daemon cannot answer is unavailable, not a pull request that
// is missing from it.
func TestUnavailableIndexAndInbox(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"capability off": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"available":false,"code":"index_unavailable"}`))
		},
		"server error": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
	} {
		clone := func(context.Context, string) (string, error) { return "acme/web", nil }
		s := New(Options{API: handler, Clone: clone})
		chatToken, _ := chat(t, s)
		action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
		for _, token := range []string{chatToken, action} {
			text, failed := data(t, s, token, "diff", map[string]any{"repository": "acme/web", "pr": 12})
			if !failed || !strings.Contains(text, "unavailable") || strings.Contains(text, "not open in") || strings.Contains(text, "own pull request's repository") {
				t.Errorf("%s: %v %s", name, failed, text)
			}
		}
		revoke()
	}
}

// What an action reads of the inbox leaves out the user's saved inboxes and
// recounts what is left.
func TestActionInboxHasNoSavedInboxes(t *testing.T) {
	d := &fakeDaemon{}
	reader := NewReader(d, nil).within([]string{"/api/inbox"}, "acme/web")
	response, err := reader.Get(context.Background(), "/api/inbox")
	if err != nil {
		t.Fatal(err)
	}
	var inbox struct {
		Count int
		Items []map[string]any
	}
	if err := json.Unmarshal(response.Body, &inbox); err != nil || inbox.Count != 1 || len(inbox.Items) != 3 {
		t.Fatalf("inbox %s: %v", response.Body, err)
	}
	for _, item := range inbox.Items {
		if _, ok := item["inboxes"]; ok {
			t.Errorf("an item names its saved inboxes: %v", item)
		}
	}
	if strings.Contains(string(response.Body), "mine") || strings.Contains(string(response.Body), "secret query") {
		t.Errorf("saved inboxes: %s", response.Body)
	}
}

// bigFacts serves a pull request whose facts are huge: padding makes the
// document over the usual read bound, as a large pull request's can be.
type bigFacts struct {
	mu     sync.Mutex
	seen   []string
	files  int
	pad    int
	built  bool
	patch  int
	cached bool
}

func (b *bigFacts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.seen = append(b.seen, r.URL.Path)
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch r.URL.Path {
	case "/api/index":
		_, _ = fmt.Fprint(w, `{"entries":[{"pull_request":{"repository":"acme/web","number":12,"head_sha":"h12"},"clone_id":"c-web","version_count":0}]}`)
	case "/api/facts":
		b.mu.Lock()
		b.built = true
		b.mu.Unlock()
		var hunks []string
		for i := range b.files {
			hunks = append(hunks, fmt.Sprintf(`{"path":"f%d.go","change":"modified","additions":1,"deletions":0}`, i))
		}
		_, _ = fmt.Fprintf(w, `{"review":{"head":"h12"},"pad":"%s","index":{"hunks":[%s]}}`, strings.Repeat("x", b.pad), strings.Join(hunks, ","))
	case "/api/facts/patch":
		_, _ = fmt.Fprintf(w, `{"patch":%q}`, q.Get("path")+strings.Repeat("+", b.patch))
	case "/api/facts/commits":
		b.mu.Lock()
		ok := b.built || b.cached
		b.mu.Unlock()
		if !ok || q.Get("head") != "h12" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `[]`)
	default:
		http.NotFound(w, r)
	}
}

func bigServer(t *testing.T, b *bigFacts) (*Server, string) {
	t.Helper()
	s := New(Options{API: b, Clone: func(context.Context, string) (string, error) { return "acme/web", nil }})
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	t.Cleanup(revoke)
	return s, action
}

// A review document or facts over 8 MiB are read, up to the daemon's own
// bound; any other route stays at 8 MiB.
func TestDiffReadsFactsOverTheUsualBound(t *testing.T) {
	b := &bigFacts{files: 2, pad: 9 << 20, patch: 10}
	s, action := bigServer(t, b)
	text, failed := data(t, s, action, "diff", nil)
	if failed || !strings.Contains(text, `"path":"f0.go"`) || !strings.Contains(text, `"path":"f1.go"`) {
		t.Fatalf("a 9 MiB facts document: %v %.300s", failed, text)
	}
	if readLimit("/api/facts") != maxDocumentRead || readLimit("/api/document") != maxDocumentRead || readLimit("/api/patch") != maxRead || readLimit("/api/facts/patch") != maxRead {
		t.Error("read limits")
	}
	// Past the document bound it fails, saying so.
	b = &bigFacts{files: 1, pad: maxDocumentRead + 1}
	s, action = bigServer(t, b)
	if text, failed := data(t, s, action, "diff", nil); !failed || !strings.Contains(text, "too large") {
		t.Errorf("over the document bound: %v %.200s", failed, text)
	}
}

// Commits and sessions read facts at the head the index shows without
// reading their payload, and build them only when the daemon has none.
func TestCommitsDoNotReadTheFactsPayloadWhenTheHeadIsKnown(t *testing.T) {
	b := &bigFacts{files: 1, pad: 9 << 20, cached: true}
	s, action := bigServer(t, b)
	if text, failed := data(t, s, action, "commits", nil); failed || text != "[]" {
		t.Fatalf("commits: %v %s", failed, text)
	}
	for _, p := range b.seen {
		if p == "/api/facts" {
			t.Errorf("read the facts payload: %v", b.seen)
		}
	}
	// With none built, one read builds them and the commits are read again.
	b = &bigFacts{files: 1, pad: 100}
	s, action = bigServer(t, b)
	if text, failed := data(t, s, action, "commits", nil); failed || text != "[]" {
		t.Fatalf("commits before facts: %v %s", failed, text)
	}
	if !b.built {
		t.Error("the facts were not built")
	}
}

func TestDiffStopsAtAByteBudgetAndListsTheRest(t *testing.T) {
	// Five files of 400 KB each: the third patch crosses 1 MiB.
	b := &bigFacts{files: 5, pad: 10, patch: 400 << 10}
	s, _ := bigServer(t, b)
	text, err := callDirect(s, "diff", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Files []struct {
			Path  string
			Patch *string
		}
		Omitted struct {
			Reason string
			Files  []string
		} `json:"patches_omitted"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	var withPatch []string
	total := 0
	for _, f := range out.Files {
		if f.Patch != nil {
			withPatch = append(withPatch, f.Path)
			total += len(*f.Patch)
		}
	}
	if strings.Join(withPatch, ",") != "f0.go,f1.go,f2.go" || total >= patchBudget+400<<10 {
		t.Errorf("patches for %v, %d bytes", withPatch, total)
	}
	if strings.Join(out.Omitted.Files, ",") != "f3.go,f4.go" || out.Omitted.Reason == "" {
		t.Errorf("omitted %+v", out.Omitted)
	}
	// A file asked for by path is read whatever the others cost.
	if text, err := callDirect(s, "diff", `{"path":"f4.go"}`); err != nil || !strings.Contains(text, `"patch":"f4.go+`) || strings.Contains(text, "patches_omitted") {
		t.Errorf("one file: %v %.200s", err, text)
	}
}

// callDirect runs a tool as the action of acme/web#12 without the envelope,
// for results larger than one chunk.
func callDirect(s *Server, name, args string) (string, error) {
	s.mu.RLock()
	tool, _ := s.tool(name)
	s.mu.RUnlock()
	c := CallContext{Consumer: Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12}, Reader: s.reader.within(tool.Reads, "acme/web")}
	return tool.Call(context.Background(), c, json.RawMessage(args))
}
