//go:build unix

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// settingsProposer is a page's proposer of one string value, recording what
// the person added when saving.
type settingsProposer struct {
	mu       sync.Mutex
	value    string
	revision int
	input    *string
}

func (p *settingsProposer) Current(context.Context, agentcontext.PageContext, string) (json.RawMessage, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, _ := json.Marshal(p.value)
	return value, strconv.Itoa(p.revision), nil
}

func (p *settingsProposer) Validate(context.Context, agentcontext.PageContext, agentcontext.Proposal) error {
	return nil
}

func (p *settingsProposer) Apply(_ context.Context, _ agentcontext.PageContext, proposal agentcontext.Proposal, revision string, input agentcontext.SaveInput) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if revision != strconv.Itoa(p.revision) {
		return agentcontext.ErrStale
	}
	if err := json.Unmarshal(proposal.After, &p.value); err != nil {
		return err
	}
	p.revision++
	p.input = input.Value
	return nil
}

// routes is a service behind its handler, with an MCP server whose settings
// page takes proposals and whose inbox page offers one operation.
type routes struct {
	*fixture
	handler  http.Handler
	mcp      *agentcontext.Server
	settings *settingsProposer
	// title is what the operation's summary says; ran counts its runs.
	mu    sync.Mutex
	title string
	ran   int
}

func newRoutes(t *testing.T) *routes {
	t.Helper()
	r := &routes{settings: &settingsProposer{value: "old"}, title: "first"}
	api := http.NewServeMux()
	api.HandleFunc("POST /api/test/do/{id}", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		r.ran++
		r.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.mcp = agentcontext.New(agentcontext.Options{API: api})
	if err := r.mcp.RegisterPage(agentcontext.PageSpec{Name: agentcontext.PageSettings, Proposer: r.settings}); err != nil {
		t.Fatal(err)
	}
	err := r.mcp.RegisterOperation(agentcontext.Operation{
		Name: "do_thing", Description: "Does a thing.", Pages: []string{agentcontext.PageInbox}, Method: http.MethodPost, Route: "/api/test/do/{id}",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		Paid:        true,
		Build: func(_ context.Context, _ agentcontext.OperationContext, args json.RawMessage) (agentcontext.OperationRequest, error) {
			var in struct{ ID string }
			if err := json.Unmarshal(args, &in); err != nil {
				return agentcontext.OperationRequest{}, err
			}
			return agentcontext.OperationRequest{Target: in.ID, Vars: map[string]string{"id": in.ID}}, nil
		},
		Describe: func(context.Context, agentcontext.OperationContext, agentcontext.OperationRequest) (agentcontext.OperationSummary, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return agentcontext.OperationSummary{Action: "Do " + r.title, Details: []agentcontext.SummaryField{{Label: "Note", Value: r.title, ByAgent: true, Quoted: true}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.fixture = newFixture(t, func(o *Options) {
		o.Registry, o.Proposals = r.mcp.Registry, r.mcp.Proposals
	})
	r.handler = r.service.Handler()
	return r
}

// call sends a request to the handler and decodes its JSON answer.
func (r *routes) call(t *testing.T, method, path string, body any, want int) map[string]any {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: %v: %s", method, path, err, response.Body.String())
	}
	return out
}

func code(answer map[string]any) string {
	c, _ := answer["code"].(string)
	return c
}

// The conversation routes start, list, read, send to, stop and close a
// conversation, set the page, and answer each refusal with its status.
func TestConversationRoutes(t *testing.T) {
	r := newRoutes(t)
	inbox := map[string]any{"page": agentcontext.PageInbox}
	created := r.call(t, "POST", "/api/console/conversations", map[string]any{"page": inbox}, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" || created["agent"] != AgentClaudeCode || created["mode"] != ModeAuto || created["state"] != StateIdle || created["progress"] != nil {
		t.Fatalf("created %v", created)
	}
	if answer := r.call(t, "POST", "/api/console/conversations", map[string]any{"page": map[string]any{"page": "nowhere"}}, http.StatusBadRequest); code(answer) != CodeInvalidPage {
		t.Fatalf("invalid page: %v", answer)
	}
	if answer := r.call(t, "POST", "/api/console/conversations", map[string]any{"agent": "someone"}, http.StatusBadRequest); code(answer) != CodeInvalidRequest {
		t.Fatalf("unknown agent: %v", answer)
	}
	r.call(t, "POST", "/api/console/conversations", map[string]any{"surprise": true}, http.StatusBadRequest)

	sent := r.call(t, "POST", "/api/console/conversations/"+id+"/messages", map[string]any{"text": "hello", "page": inbox}, http.StatusAccepted)
	if sent["state"] != StateRunning {
		t.Fatalf("sent %v", sent)
	}
	r.finished(id)
	one := r.call(t, "GET", "/api/console/conversations/"+id, nil, http.StatusOK)
	progress, _ := one["progress"].(map[string]any)
	if one["state"] != StateIdle || one["title"] != "hello" || one["session_key"] != "claude-code:"+testSession || progress["text"] != "Hello" {
		t.Fatalf("read %v", one)
	}
	list := r.call(t, "GET", "/api/console/conversations", nil, http.StatusOK)
	items, _ := list["conversations"].([]any)
	if len(items) != 1 {
		t.Fatalf("list %v", list)
	}
	item := items[0].(map[string]any)
	for _, field := range []string{"id", "title", "agent", "model", "mode", "state", "directory", "updated_at", "session"} {
		if _, ok := item[field]; !ok {
			t.Errorf("the list item has no %s: %v", field, item)
		}
	}

	r.call(t, "POST", "/api/console/conversations/"+id+"/messages", map[string]any{"text": "wait for it"}, http.StatusAccepted)
	if answer := r.call(t, "POST", "/api/console/conversations/"+id+"/messages", map[string]any{"text": "again"}, http.StatusConflict); code(answer) != "busy" {
		t.Fatalf("busy: %v", answer)
	}
	r.waitFor("child.pid")
	stopped := r.call(t, "POST", "/api/console/conversations/"+id+"/cancel", nil, http.StatusOK)
	if stopped["state"] != StateIdle || stopped["error"] != ReasonStopped {
		t.Fatalf("stopped %v", stopped)
	}
	if answer := r.call(t, "PUT", "/api/console/page", map[string]any{"page": "nowhere"}, http.StatusBadRequest); code(answer) != CodeInvalidPage {
		t.Fatalf("invalid page: %v", answer)
	}
	r.call(t, "PUT", "/api/console/page", map[string]any{"page": agentcontext.PageJobs}, http.StatusOK)
	if page, _ := r.mcp.Registry.Page(r.liveOf(id).consumer); page.Page != agentcontext.PageJobs {
		t.Fatalf("the token's page is %+v", page)
	}
	closed := r.call(t, "POST", "/api/console/conversations/"+id+"/close", map[string]any{}, http.StatusOK)
	if closed["state"] != StateClosed {
		t.Fatalf("closed %v", closed)
	}
	if answer := r.call(t, "POST", "/api/console/conversations/"+id+"/messages", map[string]any{"text": "hello"}, http.StatusConflict); code(answer) != "closed" {
		t.Fatalf("closed: %v", answer)
	}
	if answer := r.call(t, "GET", "/api/console/conversations/missing", nil, http.StatusNotFound); code(answer) != "conversation_not_found" {
		t.Fatalf("missing: %v", answer)
	}
	r.service.Shutdown()
	if answer := r.call(t, "POST", "/api/console/conversations", map[string]any{}, http.StatusServiceUnavailable); code(answer) != "shutting_down" {
		t.Fatalf("shutting down: %v", answer)
	}
}

// The proposal routes list what the agent proposed, save a change with what
// the person typed, run an operation only at the summary the person read,
// refuse a proposal made on another page, and dismiss; every change is a
// console event.
func TestProposalRoutes(t *testing.T) {
	r := newRoutes(t)
	ctx := context.Background()
	created := r.call(t, "POST", "/api/console/conversations", map[string]any{"page": map[string]any{"page": agentcontext.PageInbox}}, http.StatusCreated)
	id := created["id"].(string)
	consumer := r.liveOf(id).consumer
	base := "/api/console/conversations/" + id + "/proposals"
	if list := r.call(t, "GET", base, nil, http.StatusOK); len(list["proposals"].([]any)) != 0 {
		t.Fatalf("proposals before any: %v", list)
	}

	// An operation, confirmed at a summary that changed meanwhile.
	op, err := r.mcp.Proposals.ProposeOperation(ctx, consumer, "do_thing", json.RawMessage(`{"id":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.proposalsEvent(t, id)
	list := r.call(t, "GET", base, nil, http.StatusOK)["proposals"].([]any)
	shown := list[0].(map[string]any)
	operation := shown["operation"].(map[string]any)
	summary := operation["summary"].(map[string]any)
	field := summary["details"].([]any)[0].(map[string]any)
	if shown["kind"] != "operation" || shown["page"] != agentcontext.PageInbox || summary["paid"] != true || summary["irreversible"] != false ||
		field["by_agent"] != true || field["quoted"] != true || operation["revision"] == "" {
		t.Fatalf("listed %v", shown)
	}
	r.mu.Lock()
	r.title = "second"
	r.mu.Unlock()
	changed := r.call(t, "POST", base+"/"+op.ID+"/confirm", map[string]any{"revision": op.Operation.Revision}, http.StatusConflict)
	fresh := changed["proposal"].(map[string]any)["operation"].(map[string]any)
	if code(changed) != "operation_changed" || fresh["summary"].(map[string]any)["action"] != "Do second" || fresh["revision"] == op.Operation.Revision {
		t.Fatalf("a stale confirm: %v", changed)
	}
	if r.ran != 0 {
		t.Fatal("a stale confirm ran the operation")
	}
	done := r.call(t, "POST", base+"/"+op.ID+"/confirm", map[string]any{"revision": fresh["revision"]}, http.StatusOK)
	if done["outcome"].(map[string]any)["status"] != float64(200) || r.ran != 1 {
		t.Fatalf("confirmed %v, ran %d", done, r.ran)
	}

	// A change, saved with the value the person typed.
	r.call(t, "PUT", "/api/console/page", map[string]any{"page": agentcontext.PageSettings}, http.StatusOK)
	diff, err := r.mcp.Proposals.Propose(ctx, consumer, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: "x", After: json.RawMessage(`"new"`)})
	if err != nil {
		t.Fatal(err)
	}
	listed := r.call(t, "GET", base, nil, http.StatusOK)["proposals"].([]any)[0].(map[string]any)
	if listed["kind"] != "diff" || listed["before"] != "old" || listed["after"] != "new" || listed["revision"] != "0" {
		t.Fatalf("listed %v", listed)
	}
	if answer := r.call(t, "POST", base+"/"+diff.ID+"/confirm", map[string]any{"revision": "0"}, http.StatusBadRequest); code(answer) != "wrong_kind" {
		t.Fatalf("confirming a change: %v", answer)
	}
	r.call(t, "POST", base+"/"+diff.ID+"/save", map[string]any{"value": "typed"}, http.StatusOK)
	if r.settings.value != "new" || r.settings.input == nil || *r.settings.input != "typed" {
		t.Fatalf("saved %+v", r.settings)
	}

	// A proposal made on another page than the one the person has open is
	// refused, as the MCP server rules.
	other, err := r.mcp.Proposals.Propose(ctx, consumer, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: "x", After: json.RawMessage(`"newer"`)})
	if err != nil {
		t.Fatal(err)
	}
	r.call(t, "PUT", "/api/console/page", map[string]any{"page": agentcontext.PageInbox}, http.StatusOK)
	if answer := r.call(t, "POST", base+"/"+other.ID+"/save", map[string]any{}, http.StatusConflict); code(answer) != "not_current_page" {
		t.Fatalf("another page: %v", answer)
	}
	if r.settings.value != "new" {
		t.Fatal("a proposal for another page was saved")
	}
	r.call(t, "POST", base+"/"+other.ID+"/dismiss", nil, http.StatusOK)
	if answer := r.call(t, "POST", base+"/"+other.ID+"/dismiss", nil, http.StatusNotFound); code(answer) != "proposal_not_found" {
		t.Fatalf("dismissed twice: %v", answer)
	}
	if answer := r.call(t, "GET", "/api/console/conversations/missing/proposals", nil, http.StatusNotFound); code(answer) != "conversation_not_found" {
		t.Fatalf("missing conversation: %v", answer)
	}

	// Closing drops the proposals with the token, and says so.
	if _, err := r.mcp.Proposals.ProposeOperation(ctx, consumer, "do_thing", json.RawMessage(`{"id":"y"}`)); err != nil {
		t.Fatal(err)
	}
	r.drain()
	r.call(t, "POST", "/api/console/conversations/"+id+"/close", nil, http.StatusOK)
	r.proposalsEvent(t, id)
	if list := r.call(t, "GET", base, nil, http.StatusOK); len(list["proposals"].([]any)) != 0 {
		t.Fatalf("proposals after closing: %v", list)
	}
}

// proposalsEvent waits for the conversation's proposals event.
func (r *routes) proposalsEvent(t *testing.T, id string) {
	t.Helper()
	for {
		select {
		case e := <-r.events:
			if e.Conversation == id && e.Kind == EventProposals {
				return
			}
		default:
			t.Fatal("no proposals event")
		}
	}
}

// drain forgets the events so far.
func (r *routes) drain() {
	for {
		select {
		case <-r.events:
		default:
			return
		}
	}
}

// The panel's defaults choose the agent, model and mode of a conversation
// whose request names none.
func TestCreateTakesTheDefaults(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Defaults = func(context.Context) (Defaults, error) {
			return Defaults{Agent: AgentCodex, Model: "gpt-5", Mode: ModeWorkspace}, nil
		}
	})
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{})
	if err != nil || c.Agent != AgentCodex || c.Model != "gpt-5" || c.Mode != ModeWorkspace {
		t.Fatalf("defaults %+v %v", c, err)
	}
	c, err = f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Mode: ModeFull})
	if err != nil || c.Agent != AgentClaudeCode || c.Model != "" || c.Mode != ModeFull {
		t.Fatalf("another agent %+v %v", c, err)
	}
	c, err = f.service.Create(ctx, CreateRequest{Agent: AgentCodex, Model: new("")})
	if err != nil || c.Model != "" || c.Mode != ModeWorkspace {
		t.Fatalf("the agent's own model %+v %v", c, err)
	}
}

// Every turn asks for a usable SSH agent socket first.
func TestTurnsRefreshTheSSHAgent(t *testing.T) {
	var mu sync.Mutex
	asked := 0
	f := newFixture(t, func(o *Options) {
		o.SSHAgent = func(context.Context) string {
			mu.Lock()
			asked++
			mu.Unlock()
			return ""
		}
	})
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "agent.sock"))
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.service.Send(ctx, c.ID, "hello", nil); err != nil {
			t.Fatal(err)
		}
		f.finished(c.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 2 {
		t.Fatalf("asked %d times, want once a turn", asked)
	}
	if env := f.read("environment.txt"); !bytes.Contains([]byte(env), []byte("SSH_AUTH_SOCK="+os.Getenv("SSH_AUTH_SOCK"))) {
		t.Fatal("the agent did not get the SSH agent socket")
	}
}
