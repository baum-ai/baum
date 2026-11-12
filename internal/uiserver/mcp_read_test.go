package uiserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/inboxqueue"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
	"github.com/baum-ai/baum/sessions/session"
)

// readFixture is the daemon's LocalAPI with the real handlers of the Inbox,
// the saved inboxes, the saved filters, the live sessions and their
// transcripts, the agents' requests and the Timeline, over real stores, and
// an MCP server reading through it.
//
// Fakes, where the real thing needs a whole app around it: the review index
// is a snapshot served as it is (the real handler builds it from GitHub
// discovery, and filters history itself, which the tools do on their own);
// the session tracker takes its links from a table instead of reading git
// repositories.
type readFixture struct {
	api      *uiserver.LocalHandler
	mcp      *agentcontext.Server
	reads    *requestLog
	home     string
	tracker  *livesessions.Tracker
	requests *agentrequests.Store
	// ids of the saved inboxes.
	inboxIDs []string
	tabID    string
	// decisions and features of owner/repo and of other/repo.
	ownDecision, foreignDecision string
}

// requestLog records the requests that reach the daemon through the MCP
// server.
type requestLog struct {
	mu   sync.Mutex
	next http.Handler
	seen []string
}

func (l *requestLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.seen = append(l.seen, r.Method+" "+r.URL.RequestURI())
	l.mu.Unlock()
	l.next.ServeHTTP(w, r)
}

func (l *requestLog) requests() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// tableLinker links a session's working directory to a repository, branch
// and pull request from a table.
type tableLinker map[string]livesessions.Link

func (l tableLinker) Link(_ context.Context, cwd string) (livesessions.Link, bool) {
	link, ok := l[cwd]
	return link, ok
}

var fixtureTime = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// claudeTurn is a prompt of a Claude Code transcript with the agent's reply,
// and, for the first turn, an image pasted with the prompt.
func claudeTurn(session string, n int, prompt, reply string, image bool) string {
	at := fixtureTime.Add(time.Duration(n) * time.Minute)
	content := fmt.Sprintf("%q", prompt)
	if image {
		pixel := base64.StdEncoding.EncodeToString([]byte("not really a png"))
		content = fmt.Sprintf(`[{"type":"text","text":%q},{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]`, prompt+" [Image #1]", pixel)
	}
	user := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":"/work/demo","gitBranch":"search","type":"user","uuid":"u-%d","sessionId":%q,"timestamp":%q,"promptId":"p-%d","message":{"role":"user","content":%s}}`,
		n, session, at.Format(time.RFC3339Nano), n, content)
	assistant := fmt.Sprintf(`{"parentUuid":"u-%d","isSidechain":false,"cwd":"/work/demo","gitBranch":"search","type":"assistant","uuid":"a-%d","sessionId":%q,"timestamp":%q,"requestId":"req-%d","message":{"id":"msg-%d","role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":%q}]}}`,
		n, n, session, at.Add(time.Second).Format(time.RFC3339Nano), n, n, reply)
	return user + "\n" + assistant + "\n"
}

const plantedDSN = "postgres://app:Tr0ub4dor-3xkq@db.internal:5432/app"

func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	ctx := context.Background()
	registry, clone := readRegistry(t)
	if _, err := registry.Pick(ctx, "other/repo", nil); err != nil {
		t.Fatal(err)
	}
	dir := appdbtest.TempDir(t)

	// The review index: two pull requests of owner/repo that wait on the
	// person (a requested review and their own failing one), one nobody
	// waits on, a closed one and another repository's.
	unresolved := 2
	entry := func(repository string, number int, title string, edit func(*reviewindex.Entry)) reviewindex.Entry {
		e := reviewindex.Entry{PullRequest: discovery.PullRequest{Repository: repository, Number: number, HeadSHA: fmt.Sprintf("%08d", number), Title: title, Author: "ada", State: "OPEN", URL: "https://github.com/" + repository + "/pull/" + fmt.Sprint(number)},
			CloneID: clone.ID, Status: "none"}
		if edit != nil {
			edit(&e)
		}
		return e
	}
	index := reviewindex.Snapshot{Entries: []reviewindex.Entry{
		entry("owner/repo", 7, "Add search", func(e *reviewindex.Entry) {
			e.PullRequest.ReviewRequestedFromMe = true
			e.Unresolved = &unresolved
			e.RiskScore = json.RawMessage(`{"score":4,"status":"ready"}`)
		}),
		entry("owner/repo", 8, "Fix the loop", func(e *reviewindex.Entry) {
			e.PullRequest.AuthoredByMe = true
			e.Checks = "failing"
		}),
		entry("owner/repo", 9, "Someone else's", nil),
		entry("other/repo", 3, "Another repository", func(e *reviewindex.Entry) { e.PullRequest.ReviewRequestedFromMe = true }),
		entry("owner/repo", 5, "Merged long ago", func(e *reviewindex.Entry) {
			e.Historical = true
			e.PullRequest.State = "MERGED"
		}),
	}}
	snapshot := func(context.Context) (reviewindex.Snapshot, error) { return index, nil }

	// The saved inboxes and the Inbox queue over them.
	saved := inboxes.Open(filepath.Join(dir, "inboxes.sqlite"))
	state, err := saved.Read()
	if err != nil {
		t.Fatal(err)
	}
	state, err = saved.Replace(state.Revision, []inboxes.Inbox{
		{Name: "Reviews", Color: "blue", What: "review", Owners: []string{}, Risk: "any", Repositories: []string{"owner/repo"}, Badge: true},
		{Name: "Mine", Color: "green", What: "mine", Owners: []string{}, Risk: "any", Repositories: []string{}, NoBots: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &readFixture{home: t.TempDir()}
	for _, inbox := range state.Inboxes {
		f.inboxIDs = append(f.inboxIDs, inbox.ID)
	}
	queue := &inboxqueue.Service{Index: snapshot, Jobs: func() []jobs.Job { return nil },
		Groups:  func(context.Context) ([]reviewgroups.Group, error) { return nil, nil },
		Snoozes: func() (json.RawMessage, error) { return nil, nil },
		Inboxes: func() ([]inboxes.Inbox, error) {
			s, err := saved.Read()
			return s.Inboxes, err
		}}

	// The saved filters: the Pull requests page's draft and one tab.
	f.tabID = strings.Repeat("a1", 16)
	ui := &uistate.Store{Path: filepath.Join(dir, "ui.sqlite")}
	filters := `{"pages":{"pulls":"author=ada","tab:` + f.tabID + `":"merged"},"tabs":[{"id":"` + f.tabID + `","name":"Closed ones","view":"history","query":"long"}]}`
	if _, err := ui.Replace("filters", "", 0, "clientclientclient:1", json.RawMessage(filters)); err != nil {
		t.Fatal(err)
	}

	// Sessions: two Claude Code sessions, one per repository, the first with
	// a transcript of two turns, an image and a secret.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.home = home
	path := filepath.Join(home, ".claude", "projects", "-work-demo", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := claudeTurn("s1", 1, "Connect to "+plantedDSN, "Connected.", true) + claudeTurn("s1", 2, "Now search", "Searched.", false)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := fixtureTime.Add(10 * time.Minute)
	f.tracker = &livesessions.Tracker{Now: func() time.Time { return now }, Linker: tableLinker{
		"/work/demo":  {Repository: "owner/repo", Branch: "search", PR: 7, PRState: "OPEN", HeadSHA: "deadbeef", Upstream: "origin/search"},
		"/work/other": {Repository: "other/repo", Branch: "main"},
	}}
	for id, cwd := range map[string]string{"s1": "/work/demo", "s2": "/work/other"} {
		e := hookevents.New(hookevents.KindAgent, "claude-code", "user-prompt-submit", cwd, "test")
		e.Time = fixtureTime
		e.Session = &hookevents.Session{ID: id, Cwd: cwd, TranscriptPath: path}
		f.tracker.Apply(ctx, e)
		f.tracker.ResolvePending(ctx)
	}
	renderer := transcript.NewRenderer(home)
	transcripts := &livesessions.Transcripts{Tracker: f.tracker, Home: home, Now: func() time.Time { return now },
		Render: func(ctx context.Context, graph *session.Graph, meta transcript.Meta) ([]byte, error) {
			return renderer.Render(ctx, graph, meta)
		}}

	// The agents' requests: one pending permission request of s1, and a
	// message queued for it.
	f.requests = &agentrequests.Store{Sessions: f.tracker, Database: filepath.Join(dir, "requests.sqlite")}
	askCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.requests.Ask(askCtx, agentrequests.Ask{Agent: "claude-code", SessionID: "s1", Kind: agentrequests.KindPermission, Tool: "Bash",
			StartedAt: time.Now().Add(-time.Second), Input: []agentrequests.Row{{Label: "Command", Value: "go test ./...", Code: true}}})
	}()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if pending, _ := f.requests.List(); len(pending) > 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("the request was not asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := f.requests.Queue("claude-code", "s1", "Also run vet"); err != nil {
		t.Fatal(err)
	}

	// The Timeline: the decisions and features of both repositories.
	store, err := decisionlog.Open(filepath.Join(dir, "decisions.sqlite"), decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	save := func(repository, session, id, summary string, who decisions.Who) {
		e := decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "claude-code", ID: session, Repository: repository, Branch: "search", Path: path, Fingerprint: "f"},
			ContentHash: session,
			Decisions:   []decisions.Decision{{ID: id, Title: summary, Summary: summary, Category: decisions.CategoryArchitecture, Who: who, Turns: []int{1}, Files: []string{"search/index.go"}}},
			TurnTimes:   map[int]time.Time{1: fixtureTime}}
		if err := store.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	f.ownDecision, f.foreignDecision = "dec-own", "dec-foreign"
	save("owner/repo", "s1", f.ownDecision, "Search stays on this computer", decisions.WhoHuman)
	save("other/repo", "s2", f.foreignDecision, "Another repository's secret search decision", decisions.WhoHuman)
	for repository, feature := range map[string]string{"owner/repo": "feat-own", "other/repo": "feat-foreign"} {
		title := map[string]string{"feat-own": "Local search", "feat-foreign": "Another repository's feature"}[feature]
		pulls := []github.ListedPull{{Number: 7, Title: title, State: "open", Author: "ada", HeadRef: "search", HeadSHA: "00000007", CreatedAt: fixtureTime, UpdatedAt: fixtureTime}}
		err := store.SeedSample(ctx, repository, pulls, map[int]string{7: "A summary"}, []decisionlog.SampleFeature{{ID: feature, Title: title, Description: "It searches.", Digest: "Digest", Pulls: []int{7}, Updated: fixtureTime}})
		if err != nil {
			t.Fatal(err)
		}
	}
	timeline := &decisionlog.Timeline{Store: store, Registry: registry, Home: home, Transcripts: transcripts}

	f.api = uiserver.LocalAPI{Registry: registry, Config: config.Default(), Index: readJSONHandler(index), Inbox: queue.Handler(),
		Inboxes: (&inboxes.API{Store: saved}).Handler(), UIState: ui.Handler(), Live: transcripts.Handler(),
		AgentRequests: f.requests.Handler(), Timeline: timeline.Handler()}.Handler()
	t.Cleanup(f.api.Close)
	f.reads = &requestLog{next: f.api}
	f.mcp = agentcontext.New(agentcontext.Options{API: f.reads, Version: "test"})
	return f
}

func (f *readFixture) route(t *testing.T, path string) json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	return bytes.TrimSpace(w.Body.Bytes())
}

// call is a tool's data, from the fixture's server.
func (f *readFixture) call(t *testing.T, token, tool string, args any) (string, bool) {
	t.Helper()
	return f.callTool(t, token, tool, args)
}

// without is raw with the keys at any depth removed: what the tools leave out
// of the Timeline.
func without(t *testing.T, raw json.RawMessage, keys ...string) json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, key := range keys {
				delete(x, key)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	out, _ := json.Marshal(v)
	return out
}

func (f *readFixture) tokens(t *testing.T) (chat, action string) {
	t.Helper()
	chat, _, revokeChat, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revokeChat)
	action, _, revokeAction, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: "owner/repo", PullRequest: 7})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revokeAction)
	return chat, action
}

func keysOf(t *testing.T, raw json.RawMessage, field string, key string) []string {
	t.Helper()
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(all[field], &rows); err != nil {
		t.Fatal(err, string(raw))
	}
	var out []string
	for _, row := range rows {
		var s string
		_ = json.Unmarshal(row[key], &s)
		out = append(out, s)
	}
	return out
}

func TestInboxToolsReadTheRealRoutes(t *testing.T) {
	f := newReadFixture(t)
	chat, action := f.tokens(t)

	// The queue as the route has it: what waits on the person.
	var queue struct {
		Count int `json:"count"`
		Items []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(f.route(t, "/api/inbox"), &queue); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, item := range queue.Items {
		if item.Kind != "" && item.Kind != "other" {
			want = append(want, item.Key+"/"+item.Kind)
		}
	}
	text, failed := f.call(t, chat, "inbox", nil)
	if failed {
		t.Fatalf("inbox: %s", text)
	}
	var got struct {
		Waiting int `json:"waiting"`
		Items   []struct {
			Repository string          `json:"repository"`
			Number     int             `json:"number"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			Risk       json.RawMessage `json:"risk"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, item := range got.Items {
		have = append(have, fmt.Sprintf("%s#%d/%s", item.Repository, item.Number, item.Kind))
	}
	slices.Sort(want)
	slices.Sort(have)
	if got.Waiting != queue.Count || !slices.Equal(have, want) || len(have) != 3 {
		t.Fatalf("the queue %v (%d), the tool's %v (%d)", want, queue.Count, have, got.Waiting)
	}
	for _, item := range got.Items {
		if item.Number == 7 && (item.Title != "Add search" || string(item.Risk) != `{"score":4,"status":"ready"}`) {
			t.Errorf("row: %+v", item)
		}
	}
	// An inbox holds what the route says it holds.
	var saved struct {
		Revision int `json:"revision"`
		Inboxes  []struct {
			ID string `json:"id"`
		} `json:"inboxes"`
	}
	_ = json.Unmarshal(f.route(t, "/api/inboxes"), &saved)
	text, _ = f.call(t, chat, "inbox", map[string]any{"inbox": f.inboxIDs[0]})
	var inbox struct {
		Inbox string `json:"inbox"`
		Total int    `json:"total"`
	}
	_ = json.Unmarshal([]byte(text), &inbox)
	var counts struct {
		Inboxes []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"inboxes"`
	}
	_ = json.Unmarshal(f.route(t, "/api/inbox"), &counts)
	if inbox.Inbox != f.inboxIDs[0] || inbox.Total != counts.Inboxes[0].Count || inbox.Total == 0 {
		t.Errorf("inbox %s: %s, the route counts %+v", f.inboxIDs[0], text, counts.Inboxes)
	}

	// inboxes: the route's inboxes with the queue's counts.
	text, failed = f.call(t, chat, "inboxes", nil)
	if failed {
		t.Fatalf("inboxes: %s", text)
	}
	var listed struct {
		Revision int                          `json:"revision"`
		Inboxes  []map[string]json.RawMessage `json:"inboxes"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil || listed.Revision != saved.Revision || len(listed.Inboxes) != len(saved.Inboxes) {
		t.Fatalf("inboxes: %v %s", err, text)
	}
	var routeInboxes []map[string]json.RawMessage
	var routeBody map[string]json.RawMessage
	_ = json.Unmarshal(f.route(t, "/api/inboxes"), &routeBody)
	_ = json.Unmarshal(routeBody["inboxes"], &routeInboxes)
	for i, one := range listed.Inboxes {
		for name, value := range routeInboxes[i] {
			readSameJSON(t, "inbox "+name, one[name], value)
		}
		var count int
		_ = json.Unmarshal(one["count"], &count)
		if count != counts.Inboxes[i].Count {
			t.Errorf("inbox %d count %d, route %d", i, count, counts.Inboxes[i].Count)
		}
	}

	// test_query: the route's matches, or its problem.
	var draft struct {
		Problem string   `json:"problem"`
		Matches []string `json:"matches"`
	}
	_ = json.Unmarshal(f.route(t, "/api/inbox?query=waiting%3Dreview"), &draft)
	text, _ = f.call(t, chat, "test_query", map[string]any{"query": "waiting=review"})
	var tried struct {
		Valid   bool `json:"valid"`
		Matches []struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
		} `json:"matches"`
	}
	_ = json.Unmarshal([]byte(text), &tried)
	var matched []string
	for _, m := range tried.Matches {
		matched = append(matched, fmt.Sprintf("%s#%d", m.Repository, m.Number))
	}
	if !tried.Valid || len(draft.Matches) != 2 || !slices.Equal(matched, draft.Matches) {
		t.Errorf("test_query: %s, the route's %v", text, draft.Matches)
	}
	_ = json.Unmarshal(f.route(t, "/api/inbox?query=%28"), &draft)
	text, _ = f.call(t, chat, "test_query", map[string]any{"query": "("})
	if draft.Problem == "" || !strings.Contains(text, `"valid":false`) || !strings.Contains(text, jsonText(draft.Problem)) {
		t.Errorf("an invalid query: %s, the route's problem %q", text, draft.Problem)
	}

	// pull_requests: the index's open pull requests, each once, and a tab.
	var index struct {
		Entries []struct {
			PullRequest struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
			} `json:"pull_request"`
			Historical bool `json:"historical"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(f.route(t, "/api/index?history=1"), &index)
	var open, closed []string
	for _, e := range index.Entries {
		name := fmt.Sprintf("%s#%d", e.PullRequest.Repository, e.PullRequest.Number)
		if e.Historical {
			closed = append(closed, name)
		} else {
			open = append(open, name)
		}
	}
	pulls := func(args any) (view string, names []string, query string) {
		text, failed := f.call(t, chat, "pull_requests", args)
		if failed {
			t.Fatalf("pull_requests %v: %s", args, text)
		}
		var out struct {
			View         string `json:"view"`
			Query        string `json:"query"`
			PullRequests []struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
			} `json:"pull_requests"`
		}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatal(err)
		}
		for _, p := range out.PullRequests {
			names = append(names, fmt.Sprintf("%s#%d", p.Repository, p.Number))
		}
		return out.View, names, out.Query
	}
	if view, names, _ := pulls(nil); view != "open" || !slices.Equal(names, open) {
		t.Errorf("open %v, the index's %v", names, open)
	}
	if view, names, _ := pulls(map[string]any{"view": "closed"}); view != "closed" || !slices.Equal(names, closed) {
		t.Errorf("closed %v, the index's %v", names, closed)
	}
	// The saved tab (its view and its filter) as the saved filters hold it.
	if view, names, query := pulls(map[string]any{"tab": f.tabID}); view != "closed" || query != "long" || !slices.Equal(names, closed) {
		t.Errorf("tab: %s %v %q", view, names, query)
	}
	if _, names, query := pulls(map[string]any{"query": "repo=owner/repo risk>=4"}); query == "" || !slices.Equal(names, []string{"owner/repo#7"}) {
		t.Errorf("a query: %v", names)
	}

	// None of it is an action's.
	for _, tool := range []string{"inbox", "inboxes", "test_query", "pull_requests"} {
		r := rawToolCall(t, f, action, tool)
		if !strings.Contains(r, "unknown tool: "+tool) {
			t.Errorf("an action called %s: %s", tool, r)
		}
	}
}

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

// rawToolCall is a tools/call of name with no arguments, answered raw.
func rawToolCall(t *testing.T, f *readFixture, token, name string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"query":"x"}}}`
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	return w.Body.String()
}

func TestSessionToolsReadTheRealRoutes(t *testing.T) {
	f := newReadFixture(t)
	chat, action := f.tokens(t)

	// live_sessions: the route's sessions, as the page shows them.
	routeSessions := f.route(t, "/api/live-sessions")
	text, failed := f.call(t, chat, "live_sessions", nil)
	if failed {
		t.Fatalf("live_sessions: %s", text)
	}
	routeIDs := keysOf(t, routeSessions, "sessions", "session_id")
	toolIDs := keysOf(t, json.RawMessage(text), "sessions", "session_id")
	slices.Sort(routeIDs)
	slices.Sort(toolIDs)
	if !slices.Equal(routeIDs, []string{"s1", "s2"}) || !slices.Equal(toolIDs, routeIDs) {
		t.Fatalf("sessions %v, the route's %v", toolIDs, routeIDs)
	}
	var tool, route struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	_ = json.Unmarshal([]byte(text), &tool)
	_ = json.Unmarshal(routeSessions, &route)
	byID := func(rows []map[string]json.RawMessage, id string) map[string]json.RawMessage {
		for _, row := range rows {
			if string(row["session_id"]) == `"`+id+`"` {
				return row
			}
		}
		return nil
	}
	own, direct := byID(tool.Sessions, "s1"), byID(route.Sessions, "s1")
	for toolField, routeField := range map[string]string{"agent": "agent", "state": "state", "repository": "repository", "branch": "branch", "pull_request": "pr", "pull_request_state": "pr_state", "activity": "activity"} {
		if string(direct[routeField]) != "" {
			readSameJSON(t, "session "+toolField, own[toolField], direct[routeField])
		}
	}
	// The title is the session's first prompt, which holds a secret the
	// envelope masks.
	if title := string(own["title"]); !strings.Contains(title, "Connect to postgres://") || strings.Contains(title, "Tr0ub4dor") || !strings.Contains(string(direct["title"]), "Tr0ub4dor") {
		t.Errorf("title %s, the route's %s", title, direct["title"])
	}
	if string(own["repository"]) != `"owner/repo"` || string(own["pull_request"]) != "7" || string(own["state"]) == "" {
		t.Errorf("session: %v", own)
	}
	if strings.Contains(text, "deadbeef") || strings.Contains(text, "origin/search") {
		t.Errorf("fields the page does not show: %s", text)
	}
	// An action reads its repository's sessions, and no other's.
	text, failed = f.call(t, action, "live_sessions", nil)
	if failed || !strings.Contains(text, `"session_id":"s1"`) || strings.Contains(text, "s2") || strings.Contains(text, "other/repo") {
		t.Errorf("an action's sessions: %v %s", failed, text)
	}
	text, failed = f.call(t, action, "live_sessions", map[string]any{"repository": "other/repo"})
	if !failed || !strings.Contains(text, "own pull request's repository") || strings.Contains(text, "s2") {
		t.Errorf("an action asking another repository's sessions: %v %s", failed, text)
	}

	// The transcript: the route's prompts, without images, paths or secrets.
	routeTranscript := f.route(t, "/api/live-sessions/claude-code/s1/transcript")
	var answer struct {
		Available  bool                `json:"available"`
		Images     map[string][]string `json:"images"`
		Transcript struct {
			Session struct {
				WorkingDir string `json:"working_dir"`
				Prompts    []struct {
					Ordinal int    `json:"ordinal"`
					Text    string `json:"text"`
				} `json:"prompts"`
			} `json:"session"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal(routeTranscript, &answer); err != nil || !answer.Available || len(answer.Transcript.Session.Prompts) != 2 || len(answer.Images) == 0 {
		t.Fatalf("the route's transcript (with an image): %v %s", err, routeTranscript)
	}
	f.reads.mu.Lock()
	f.reads.seen = nil
	f.reads.mu.Unlock()
	text, failed = f.call(t, chat, "live_session_transcript", map[string]any{"session": "claude-code:s1"})
	if failed {
		t.Fatalf("live_session_transcript: %s", text)
	}
	var seen struct {
		Images     map[string][]string `json:"images"`
		Transcript struct {
			Session struct {
				WorkingDir string `json:"working_dir"`
				Prompts    []struct {
					Ordinal int    `json:"ordinal"`
					Text    string `json:"text"`
				} `json:"prompts"`
			} `json:"session"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(text), &seen); err != nil {
		t.Fatal(err)
	}
	if len(seen.Transcript.Session.Prompts) != 2 || seen.Transcript.Session.WorkingDir != "" || len(seen.Images) != 0 {
		t.Errorf("transcript: %s", text)
	}
	for i, p := range seen.Transcript.Session.Prompts {
		if p.Ordinal != answer.Transcript.Session.Prompts[i].Ordinal {
			t.Errorf("prompt %d: %+v, the route's %+v", i, p, answer.Transcript.Session.Prompts[i])
		}
	}
	for _, leak := range []string{plantedDSN, "Tr0ub4dor", "/work/demo", "image/png", "bm90IHJlYWxseSBhIHBuZw", "/images/"} {
		if strings.Contains(text, leak) {
			t.Errorf("transcript carries %q: %s", leak, text)
		}
	}
	for _, r := range f.reads.requests() {
		if strings.Contains(r, "/images/") || strings.Contains(r, "image") {
			t.Errorf("an image route was read: %s", r)
		}
	}
	// By turn.
	text, _ = f.call(t, chat, "live_session_transcript", map[string]any{"session": "claude-code:s1", "limit": 1})
	var page struct {
		Transcript struct {
			Session struct {
				Prompts  []json.RawMessage `json:"prompts"`
				NextTurn int               `json:"next_turn"`
			} `json:"session"`
		} `json:"transcript"`
	}
	_ = json.Unmarshal([]byte(text), &page)
	if len(page.Transcript.Session.Prompts) != 1 || page.Transcript.Session.NextTurn != 2 {
		t.Errorf("paging: %s", text)
	}
	if text, failed := f.call(t, chat, "live_session_transcript", map[string]any{"session": "claude-code:nope"}); !failed {
		t.Errorf("a session that is not listed: %s", text)
	}
	r := rawToolCall(t, f, action, "live_session_transcript")
	if !strings.Contains(r, "unknown tool") {
		t.Errorf("an action called live_session_transcript: %s", r)
	}

	// The requests and queued messages, without the settings.
	var requests struct {
		Requests []json.RawMessage `json:"requests"`
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(f.route(t, "/api/agent-requests"), &requests)
	if len(requests.Requests) != 1 || len(requests.Messages) != 1 {
		t.Fatalf("the route's requests %d, messages %d", len(requests.Requests), len(requests.Messages))
	}
	text, failed = f.call(t, chat, "agent_requests", nil)
	if failed || strings.Contains(text, "settings") || strings.Contains(text, "codex_approvals") || strings.Contains(text, "Also run vet") {
		t.Fatalf("agent_requests: %v %s", failed, text)
	}
	readSameJSON(t, "requests", readField(t, text, "requests"), jsonOf(requests.Requests))
	text, failed = f.call(t, chat, "agent_messages", nil)
	if failed || strings.Contains(text, "settings") || strings.Contains(text, "codex_approvals") || strings.Contains(text, "go test") {
		t.Fatalf("agent_messages: %v %s", failed, text)
	}
	readSameJSON(t, "messages", readField(t, text, "messages"), jsonOf(requests.Messages))
	for _, tool := range []string{"agent_requests", "agent_messages"} {
		if r := rawToolCall(t, f, action, tool); !strings.Contains(r, "unknown tool") {
			t.Errorf("an action called %s: %s", tool, r)
		}
	}
}

func jsonOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestTimelineToolsReadTheRealRoutes(t *testing.T) {
	f := newReadFixture(t)
	chat, action := f.tokens(t)
	// What the tools leave out of the people in the Timeline.
	drop := func(raw json.RawMessage) json.RawMessage { return without(t, raw, "avatar_url", "email") }

	// decisions: the route's list, for one repository and for all.
	text, failed := f.call(t, chat, "decisions", map[string]any{"repository": "owner/repo"})
	if failed {
		t.Fatalf("decisions: %s", text)
	}
	readSameJSON(t, "decisions", json.RawMessage(text), drop(f.route(t, "/api/timeline/decisions?repository=owner%2Frepo&limit=25")))
	if !strings.Contains(text, "Search stays on this computer") || strings.Contains(text, "Another repository") {
		t.Errorf("decisions: %s", text)
	}
	text, _ = f.call(t, chat, "decisions", map[string]any{"who": []string{"human"}, "q": "search", "file": "search", "limit": 10})
	readSameJSON(t, "all decisions", json.RawMessage(text), drop(f.route(t, "/api/timeline/decisions?who=human&q=search&file=search&limit=10")))
	if !strings.Contains(text, "Another repository's secret search decision") {
		// A chat reads every repository the Timeline serves.
		t.Errorf("all decisions: %s", text)
	}
	text, _ = f.call(t, chat, "decisions", map[string]any{"who": []string{"agent"}})
	if len(keysOf(t, json.RawMessage(text), "decisions", "id")) != 0 {
		t.Errorf("agent decisions: %s", text)
	}
	// decision: the route's detail.
	text, failed = f.call(t, chat, "decision", map[string]any{"id": f.ownDecision})
	if failed {
		t.Fatalf("decision: %s", text)
	}
	readSameJSON(t, "decision", json.RawMessage(text), drop(f.route(t, "/api/timeline/decisions/"+f.ownDecision)))
	if text, failed := f.call(t, chat, "decision", map[string]any{"id": "missing"}); !failed || !strings.Contains(text, "not found") {
		t.Errorf("a missing decision: %v %s", failed, text)
	}
	// features and a feature.
	text, failed = f.call(t, chat, "features", map[string]any{"repository": "owner/repo"})
	if failed {
		t.Fatalf("features: %s", text)
	}
	readSameJSON(t, "features", json.RawMessage(text), drop(f.route(t, "/api/timeline/features?repository=owner%2Frepo&limit=25")))
	if !strings.Contains(text, "Local search") {
		t.Errorf("features: %s", text)
	}
	text, failed = f.call(t, chat, "feature", map[string]any{"id": "feat-own"})
	if failed {
		t.Fatalf("feature: %s", text)
	}
	readSameJSON(t, "feature", json.RawMessage(text), drop(f.route(t, "/api/timeline/features/feat-own")))
	// feature_build and file_history.
	text, failed = f.call(t, chat, "feature_build", map[string]any{"repository": "owner/repo"})
	if failed {
		t.Fatalf("feature_build: %s", text)
	}
	readSameJSON(t, "feature build", json.RawMessage(text), f.route(t, "/api/timeline/build?repository=owner%2Frepo"))
	text, failed = f.call(t, chat, "file_history", map[string]any{"repository": "owner/repo", "prefix": "search"})
	if failed {
		t.Fatalf("file_history: %s", text)
	}
	readSameJSON(t, "files", json.RawMessage(text), f.route(t, "/api/timeline/files?repository=owner%2Frepo&prefix=search&limit=20"))
	if !strings.Contains(text, "search/index.go") {
		t.Errorf("files: %s", text)
	}
	// The Timeline's transcript of a captured session, masked of what the
	// session's files hold, without images or paths.
	text, failed = f.call(t, chat, "timeline_transcript", map[string]any{"session": "claude-code:s1"})
	if failed || !strings.Contains(text, "Now search") {
		t.Fatalf("timeline_transcript: %v %s", failed, text)
	}
	for _, leak := range []string{plantedDSN, "/work/demo", "image/png", "/images/", "working_dir"} {
		if strings.Contains(text, leak) {
			t.Errorf("timeline transcript carries %q", leak)
		}
	}

	// An action reads its own repository's Timeline and no other.
	refused := func(what, text string, failed bool) {
		t.Helper()
		if !failed || !strings.Contains(text, "own pull request's repository") || strings.Contains(text, "Another repository") ||
			strings.Contains(text, "secret search decision") || strings.Contains(text, "input schema") {
			t.Errorf("%s: %v %s", what, failed, text)
		}
	}
	text, failed = f.call(t, action, "decisions", nil)
	if failed || !strings.Contains(text, "Search stays on this computer") || strings.Contains(text, "secret search decision") {
		t.Errorf("an action's decisions: %v %s", failed, text)
	}
	readSameJSON(t, "an action's decisions", json.RawMessage(text), drop(f.route(t, "/api/timeline/decisions?repository=owner%2Frepo&limit=25")))
	text, failed = f.call(t, action, "features", nil)
	if failed || !strings.Contains(text, "Local search") || strings.Contains(text, "Another repository's feature") {
		t.Errorf("an action's features: %v %s", failed, text)
	}
	for _, tool := range []string{"decisions", "features", "file_history", "feature_build"} {
		text, failed := f.call(t, action, tool, map[string]any{"repository": "other/repo"})
		refused(tool+" of another repository", text, failed)
	}
	// The details name their repository in the response, which the binding
	// reads: another repository's are refused once the daemon has answered.
	if text, failed := f.call(t, action, "decision", map[string]any{"id": f.ownDecision}); failed || !strings.Contains(text, "Search stays on this computer") {
		t.Errorf("an action's decision: %v %s", failed, text)
	}
	text, failed = f.call(t, action, "decision", map[string]any{"id": f.foreignDecision})
	refused("another repository's decision", text, failed)
	if text, failed := f.call(t, action, "feature", map[string]any{"id": "feat-own"}); failed || !strings.Contains(text, "It searches.") {
		t.Errorf("an action's feature: %v %s", failed, text)
	}
	text, failed = f.call(t, action, "feature", map[string]any{"id": "feat-foreign"})
	refused("another repository's feature", text, failed)
	// What an action never sees.
	if r := rawToolCall(t, f, action, "timeline_transcript"); !strings.Contains(r, "unknown tool") {
		t.Errorf("an action called timeline_transcript: %s", r)
	}
	// The foreign detail was asked of the daemon (the binding reads the
	// response) and the foreign list never was.
	for _, r := range f.reads.requests() {
		if strings.Contains(r, "other%2Frepo") || strings.Contains(r, "other/repo") {
			t.Errorf("a read named another repository: %s", r)
		}
	}
}

// readRegistry is a registry with one clone of owner/repo.
func readRegistry(t *testing.T) (*repos.Registry, repos.Clone) {
	t.Helper()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	clone, err := registry.Add(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry, clone
}

func readWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readJSONHandler serves v to a GET, as the index route serves its snapshot.
func readJSONHandler(v any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		readWriteJSON(w, v)
	})
}

var readFence = regexp.MustCompile(`(?s)^BEGIN UNTRUSTED DATA ([0-9a-f]+)\n(.*)\nEND UNTRUSTED DATA ([0-9a-f]+)\n`)

// callTool calls an MCP tool and returns the data between its fence.
func (f *readFixture) callTool(t *testing.T, token, name string, args any) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	var reply struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool `json:"isError"`
		}
		Error *struct{ Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Error != nil || len(reply.Result.Content) != 1 {
		t.Fatalf("%s: %v %s", name, err, w.Body.String())
	}
	m := readFence.FindStringSubmatch(reply.Result.Content[0].Text)
	if m == nil || m[1] != m[3] {
		t.Fatalf("%s: not fenced: %.200q", name, reply.Result.Content[0].Text)
	}
	return m[2], reply.Result.IsError
}

// readField is one top-level field of a tool's JSON, as JSON.
func readField(t *testing.T, text, name string) json.RawMessage {
	t.Helper()
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &all); err != nil {
		t.Fatalf("not an object: %v %.200s", err, text)
	}
	return all[name]
}

// readSameJSON fails unless a and b are the same JSON value.
func readSameJSON(t *testing.T, what string, a, b json.RawMessage) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%s: %v: %s", what, err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%s: %v: %s", what, err, b)
	}
	if !reflect.DeepEqual(x, y) {
		t.Errorf("%s differs:\n tool  %s\n route %s", what, a, b)
	}
}

var readWriteRoute = regexp.MustCompile(`"(POST|PUT|PATCH|DELETE) (/api/[^"]*)"`)

// No read tool reaches a route that writes. The guarantee is Reader.fetch,
// which builds only GET requests; this scans the sources of the handlers
// under the new tools' prefixes for write routes and sends a GET to each
// through the real handlers (the live sessions, agent requests, saved
// inboxes and filters, Timeline), then checks that nothing they hold changed.
func TestNoReadToolReachesAWriteRoute(t *testing.T) {
	f := newReadFixture(t)
	var prefixes []string
	for _, tp := range f.mcp.ToolReads() {
		prefixes = append(prefixes, tp...)
	}
	under := func(path string) bool {
		return slices.ContainsFunc(prefixes, func(prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") })
	}
	revision := func(path string) string {
		var v struct {
			Revision int `json:"revision"`
		}
		_ = json.Unmarshal(f.route(t, path), &v)
		return fmt.Sprint(v.Revision)
	}
	before := revision("/api/inboxes") + revision("/api/ui-state/filters")
	pending := func() int { requests, messages := f.requests.List(); return len(requests)*10 + len(messages) }
	found := 0
	for _, pattern := range []string{"../livesessions/*.go", "../agentrequests/*.go", "../inboxes/*.go", "../uistate/*.go", "../decisionlog/*.go", "../inboxqueue/*.go", "../reviewindex/*.go"} {
		sources, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range sources {
			if strings.HasSuffix(source, "_test.go") {
				continue
			}
			body, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range readWriteRoute.FindAllStringSubmatch(string(body), -1) {
				path := regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllString(m[2], "x")
				if !under(path) {
					continue
				}
				found++
				w := httptest.NewRecorder()
				f.api.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
				if w.Code >= 400 && w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
					t.Errorf("GET %s (a %s route in %s): %d", path, m[1], source, w.Code)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no write route found under the tools' prefixes: the scan is broken")
	}
	if got := revision("/api/inboxes") + revision("/api/ui-state/filters"); got != before || pending() != 11 {
		t.Errorf("a GET changed what the daemon holds: revisions %s then %s, requests and messages %d", before, got, pending())
	}
	if len(f.tracker.Sessions()) != 2 {
		t.Errorf("sessions after the GETs: %d", len(f.tracker.Sessions()))
	}
}
