package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingAPI is a local API whose one write route counts its runs, and
// whose GET routes answer what the operation reads.
type countingAPI struct {
	runs   atomic.Int32
	status atomic.Int32
	mu     sync.Mutex
	// bodies are the bodies the route was sent, in order.
	bodies []string
	// jobs are the jobs GET /api/jobs/{id} knows.
	jobs map[string]string
}

func (a *countingAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		id, ok := strings.CutPrefix(r.URL.Path, "/api/jobs/")
		a.mu.Lock()
		status, known := a.jobs[id]
		a.mu.Unlock()
		if !ok || !known {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"status":%q,"secret":"ROUTE-ONLY"}`, id, status)
		return
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/jobs/") || !strings.HasSuffix(r.URL.Path, "/cancel") {
		http.Error(w, `{"error":"no such route"}`, http.StatusNotFound)
		return
	}
	var body strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := r.Body.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			break
		}
	}
	a.mu.Lock()
	a.bodies = append(a.bodies, body.String())
	a.mu.Unlock()
	a.runs.Add(1)
	if status := a.status.Load(); status != 0 {
		w.WriteHeader(int(status))
		_, _ = w.Write([]byte(`{"error":"the job already finished"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"secret":"ROUTE-ONLY"}`))
}

// cancelOperation cancels the job the page shows, after checking it is
// running.
func cancelOperation() Operation {
	return Operation{
		Name: "test_cancel", Description: "Cancels the job the page shows.",
		Pages: []string{PageJob}, Method: http.MethodPost, Route: "/api/jobs/{id}/cancel",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string"}},"additionalProperties":false}`),
		Reads:       []string{"/api/jobs"}, Irreversible: true,
		Build: func(_ context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			if c.Page.Job == "" {
				return OperationRequest{}, ErrNotCurrentPage
			}
			return OperationRequest{Target: "job:" + c.Page.Job, Vars: map[string]string{"id": c.Page.Job}, Body: args}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			if req.Vars["id"] != c.Page.Job {
				return OperationSummary{}, ErrNotCurrentPage
			}
			r, err := c.Reader.Get(ctx, "/api/jobs/"+req.Vars["id"])
			if err != nil || r.Status != http.StatusOK {
				return OperationSummary{}, errors.New("the job is not on the page")
			}
			var job struct{ Status string }
			if err := json.Unmarshal(r.Body, &job); err != nil || job.Status != "running" {
				return OperationSummary{}, errors.New("the job is not running")
			}
			return OperationSummary{Action: "Cancel job " + req.Vars["id"]}, nil
		},
	}
}

func operationServer(t *testing.T) (*Server, *countingAPI) {
	t.Helper()
	api := &countingAPI{jobs: map[string]string{"j1": "running", "j2": "running"}}
	s := New(Options{API: api, Version: "test"})
	if err := s.RegisterOperation(cancelOperation()); err != nil {
		t.Fatal(err)
	}
	return s, api
}

func TestOperationsRunOnlyOnConfirmAndOnce(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	token, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	text, failed := data(t, s, token, "propose_test_cancel", map[string]string{"reason": "stuck"})
	if failed {
		t.Fatal(text)
	}
	// The agent learns the receipt alone: no summary, no request, nothing
	// the routes answered.
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil || len(got) != 5 || got["kind"] != string(ProposalOperation) ||
		got["status"] != operationStatus || got["page"] != PageJob || got["target"] != "job:j1" {
		t.Fatalf("the tool answered %s", text)
	}
	if strings.Contains(text, "Cancel job") || strings.Contains(text, "ROUTE-ONLY") || strings.Contains(text, "running") {
		t.Fatalf("the tool answered more than a receipt: %s", text)
	}
	if api.runs.Load() != 0 {
		t.Fatal("proposing ran the operation")
	}
	list := s.Proposals.List(id)
	if len(list) != 1 || list[0].Kind != ProposalOperation || list[0].Operation == nil ||
		list[0].Operation.Summary.Action != "Cancel job j1" || !list[0].Operation.Summary.Irreversible || list[0].Operation.Summary.Paid {
		t.Fatalf("%+v", list)
	}
	p := list[0]
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, ErrNotADiff) || api.runs.Load() != 0 {
		t.Fatal("an operation was saved as a diff:", err)
	}
	if _, err := s.Proposals.Confirm(ctx, "someone-else", p.ID); !errors.Is(err, ErrUnknownProposal) || api.runs.Load() != 0 {
		t.Fatal("another consumer confirmed the operation:", err)
	}
	outcome, err := s.Proposals.Confirm(ctx, id, p.ID)
	if err != nil || !outcome.OK() || outcome.Message != "OK" || api.runs.Load() != 1 || api.bodies[0] != `{"reason":"stuck"}` {
		t.Fatal(outcome, err, api.bodies)
	}
	if _, err := s.Proposals.Confirm(ctx, id, p.ID); !errors.Is(err, ErrUnknownProposal) || api.runs.Load() != 1 {
		t.Fatal("a confirmed operation ran twice:", err)
	}
	if len(s.Proposals.List(id)) != 0 {
		t.Fatal("a confirmed operation stayed open")
	}
}

func TestConcurrentConfirmsRunOnce(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	p, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ran atomic.Int32
	for range 16 {
		wg.Go(func() {
			if _, err := s.Proposals.Confirm(ctx, id, p.ID); err == nil {
				ran.Add(1)
			} else if !errors.Is(err, ErrUnknownProposal) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ran.Load() != 1 || api.runs.Load() != 1 {
		t.Fatalf("%d confirms ran, the route ran %d times", ran.Load(), api.runs.Load())
	}
}

func TestDismissedOperationsNeverRun(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	p, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Proposals.Dismiss(id, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Confirm(ctx, id, p.ID); !errors.Is(err, ErrUnknownProposal) || api.runs.Load() != 0 {
		t.Fatal("a dismissed operation ran:", err)
	}
}

func TestConfirmNeedsTheSamePageAndALiveToken(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	_, id, revoke := mint(t, s, Consumer{Kind: Chat})
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	p, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Another job's page, then the Jobs list: neither is the page it was
	// proposed on.
	for _, page := range []PageContext{{Page: PageJob, Job: "j2"}, {Page: PageJobs}} {
		setPage(t, s, id, page)
		if _, err := s.Proposals.Confirm(ctx, id, p.ID); !errors.Is(err, ErrNotCurrentPage) || api.runs.Load() != 0 {
			t.Fatalf("%+v: %v", page, err)
		}
	}
	// Back on the page, the target must still be what the page shows: a
	// job that finished meanwhile is refused, and the proposal stays open.
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	api.mu.Lock()
	api.jobs["j1"] = "done"
	api.mu.Unlock()
	if _, err := s.Proposals.Confirm(ctx, id, p.ID); err == nil || api.runs.Load() != 0 || len(s.Proposals.List(id)) != 1 {
		t.Fatal("an operation on a target that left the page ran:", err)
	}
	api.mu.Lock()
	api.jobs["j1"] = "running"
	api.mu.Unlock()
	revoke()
	if _, err := s.Proposals.Confirm(ctx, id, p.ID); err == nil || api.runs.Load() != 0 {
		t.Fatal("a revoked consumer's operation ran:", err)
	}
}

// A revoke that lands while Confirm checks the page leaves nothing to run.
func TestConfirmRacingARevoke(t *testing.T) {
	ctx := context.Background()
	api := &countingAPI{jobs: map[string]string{"j1": "running"}}
	s := New(Options{API: api, Version: "test"})
	op := cancelOperation()
	var revoke func()
	var confirming atomic.Bool
	describe := op.Describe
	op.Describe = func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
		if confirming.Load() {
			revoke()
		}
		return describe(ctx, c, req)
	}
	if err := s.RegisterOperation(op); err != nil {
		t.Fatal(err)
	}
	var id string
	_, id, revoke = mint(t, s, Consumer{Kind: Chat})
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	p, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	confirming.Store(true)
	if _, err := s.Proposals.Confirm(ctx, id, p.ID); !errors.Is(err, ErrUnknownConsumer) || api.runs.Load() != 0 {
		t.Fatal("an operation ran after its consumer was revoked:", err)
	}
}

func TestOperationsOnlyOnTheirPagesAndOnlyForChats(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	token, id := chat(t, s)
	if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil); !errors.Is(err, ErrNotCurrentPage) {
		t.Fatal("proposed with no page:", err)
	}
	setPage(t, s, id, PageContext{Page: PageJobs})
	if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil); !errors.Is(err, ErrNotCurrentPage) {
		t.Fatal("proposed on a page that does not offer it:", err)
	}
	setPage(t, s, id, PageContext{Page: PageJob, Job: "unknown"})
	if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil); err == nil {
		t.Fatal("proposed for a job the page does not show")
	}
	if _, err := s.Proposals.ProposeOperation(ctx, id, "nothing", nil); !errors.Is(err, ErrUnknownOperation) {
		t.Fatal(err)
	}
	// An operation is never recorded through Propose, and its arguments are
	// checked against its schema whoever proposes it.
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", json.RawMessage(`{"reason":5}`)); err == nil {
		t.Fatal("arguments the schema refuses were proposed")
	}
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageJob, Kind: ProposalOperation, Target: "job:j1", After: json.RawMessage(`{}`)}); !errors.Is(err, ErrNotADiff) {
		t.Fatal(err)
	}
	// An action neither lists nor calls the tool, and cannot propose.
	actionToken, actionID, revokeAction := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 1})
	defer revokeAction()
	if r := rpc(t, s, actionToken, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"propose_test_cancel","arguments":{}}}`); r.Error == nil {
		t.Fatal("an action called an operation's tool")
	}
	if _, err := s.Proposals.ProposeOperation(ctx, actionID, "test_cancel", nil); !errors.Is(err, ErrReadOnly) {
		t.Fatal("an action proposed an operation:", err)
	}
	if api.runs.Load() != 0 {
		t.Fatal("something ran")
	}
	// The chat's page lists the tool.
	if text, _ := data(t, s, token, "current_page", nil); !strings.Contains(text, "propose_test_cancel") {
		t.Fatal(text)
	}
	// A diff is not confirmed.
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: newFake("review", "old")}); err != nil {
		t.Fatal(err)
	}
	setPage(t, s, id, PageContext{Page: PageSettings})
	diff, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"new"`)})
	if err != nil || diff.Kind != ProposalDiff {
		t.Fatal(diff, err)
	}
	if _, err := s.Proposals.Confirm(ctx, id, diff.ID); !errors.Is(err, ErrNotAnOperation) {
		t.Fatal(err)
	}
}

func TestOperationsShareTheProposalLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := operationServer(t)
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	for range maxProposals {
		if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil); !errors.Is(err, errTooManyProposals) {
		t.Fatal(err)
	}
}

func TestARefusedOperationReturnsTheRoutesAnswer(t *testing.T) {
	ctx := context.Background()
	s, api := operationServer(t)
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageJob, Job: "j1"})
	p, err := s.Proposals.ProposeOperation(ctx, id, "test_cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	api.status.Store(http.StatusConflict)
	outcome, err := s.Proposals.Confirm(ctx, id, p.ID)
	if err != nil || outcome.OK() || outcome.Status != http.StatusConflict || outcome.Message != "the job already finished" {
		t.Fatal(outcome, err)
	}
}

func TestOperationPathsStayOnTheirRoute(t *testing.T) {
	op := Operation{Name: "x", Route: "/api/repositories/{owner}/{name}/clone"}
	for _, vars := range []map[string]string{
		{"owner": "..", "name": "x"},
		{"owner": ".", "name": "x"},
		{"owner": "a/b", "name": "x"},
		{"owner": "a%2Fb", "name": "x"},
		{"owner": "a", "name": "x?y=1"},
		{"owner": "a", "name": "x#y"},
		{"owner": "a", "name": `x\y`},
		{"owner": "a", "name": ""},
		{"owner": "a", "name": "x y"},
		{"owner": "a", "name": "é"},
		{"owner": "a"},
		{"owner": "a", "name": "b", "extra": "c"},
		{"owner": "a", "name": ".."},
		{"owner": strings.Repeat("a", 300), "name": "b"},
	} {
		if p, err := op.Path(vars); err == nil {
			t.Errorf("%v made %s", vars, p)
		}
	}
	for name, want := range map[string]string{"web.js": "/api/repositories/acme/web.js/clone", ".github": "/api/repositories/acme/.github/clone"} {
		if p, err := op.Path(map[string]string{"owner": "acme", "name": name}); err != nil || p != want {
			t.Fatal(p, err)
		}
	}
}

func TestOperationDefinitionsAreChecked(t *testing.T) {
	base := cancelOperation()
	for name, change := range map[string]func(*Operation){
		"a GET":                  func(op *Operation) { op.Method = http.MethodGet },
		"a PUT":                  func(op *Operation) { op.Method = http.MethodPut },
		"a settings route":       func(op *Operation) { op.Route = "/api/settings/agent-env" },
		"the MCP route":          func(op *Operation) { op.Route = "/api/mcp" },
		"a partial wildcard":     func(op *Operation) { op.Route = "/api/jobs/x{id}/cancel" },
		"an unclean route":       func(op *Operation) { op.Route = "/api/jobs/../settings" },
		"a route with a query":   func(op *Operation) { op.Route = "/api/jobs/{id}/cancel?x=1" },
		"no page":                func(op *Operation) { op.Pages = nil },
		"an unknown page":        func(op *Operation) { op.Pages = []string{"nowhere"} },
		"an unreadable read":     func(op *Operation) { op.Reads = []string{"/api/settings"} },
		"no describe":            func(op *Operation) { op.Describe = nil },
		"a schema not an object": func(op *Operation) { op.InputSchema = json.RawMessage(`{"type":"string"}`) },
	} {
		op := base
		change(&op)
		s := New(Options{API: http.NotFoundHandler()})
		if err := s.RegisterOperation(op); err == nil {
			t.Errorf("%s was registered", name)
		}
	}
}

func TestAChatsSessionIsChecked(t *testing.T) {
	r := NewRegistry()
	for _, c := range []Consumer{
		{Kind: Chat, Session: "claude"},
		{Kind: Chat, Session: ":id"},
		{Kind: Chat, Session: "claude:"},
		{Kind: Action, Repository: "acme/web", PullRequest: 1, Session: "claude:s1"},
	} {
		if _, _, _, err := r.Mint(c); err == nil {
			t.Errorf("%+v was minted", c)
		}
	}
	if _, _, revoke, err := r.Mint(Consumer{Kind: Chat, Session: SessionKey("claude", "s1")}); err != nil {
		t.Fatal(err)
	} else {
		revoke()
	}
	// A session set later: well formed, a chat's, and once.
	_, chat, revokeChat, _ := r.Mint(Consumer{Kind: Chat})
	defer revokeChat()
	_, action, revokeAction, _ := r.Mint(Consumer{Kind: Action, Repository: "acme/web", PullRequest: 1})
	defer revokeAction()
	for _, bad := range []string{"", "claude", "claude:"} {
		if err := r.SetSession(chat, bad); err == nil {
			t.Errorf("session %q was set", bad)
		}
	}
	if err := r.SetSession(action, "claude:s1"); err == nil {
		t.Error("an action's session was set")
	}
	if err := r.SetSession(chat, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSession(chat, "claude:s1"); err != nil {
		t.Fatal("setting the same session again:", err)
	}
	if err := r.SetSession(chat, "claude:s2"); err == nil {
		t.Error("the session was swapped")
	}
	if c, _ := r.Consumer(chat); c.Session != "claude:s1" {
		t.Errorf("session %q", c.Session)
	}
}
