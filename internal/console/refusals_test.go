//go:build unix

package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// Each refusal of the routes: another conversation's proposal, a body over
// the bound, a change to a value that moved on, a confirm without the
// summary's revision, and defaults that cannot be read.
func TestRouteRefusals(t *testing.T) {
	r := newRoutes(t)
	ctx := context.Background()
	inbox := map[string]any{"page": map[string]any{"page": agentcontext.PageInbox}}
	a := r.call(t, "POST", "/api/console/conversations", inbox, http.StatusCreated)["id"].(string)
	b := r.call(t, "POST", "/api/console/conversations", inbox, http.StatusCreated)["id"].(string)
	theirs, err := r.mcp.Proposals.ProposeOperation(ctx, r.liveOf(b).consumer, "do_thing", json.RawMessage(`{"id":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	// One conversation's routes never reach another's proposals.
	for _, action := range []struct{ name, body string }{
		{"confirm", `{"revision":"` + theirs.Operation.Revision + `"}`},
		{"save", `{}`},
		{"dismiss", `{}`},
	} {
		var body map[string]any
		_ = json.Unmarshal([]byte(action.body), &body)
		if answer := r.call(t, "POST", "/api/console/conversations/"+a+"/proposals/"+theirs.ID+"/"+action.name, body, http.StatusNotFound); code(answer) != "proposal_not_found" {
			t.Fatalf("%s: %v", action.name, answer)
		}
	}
	if r.ran != 0 || len(r.mcp.Proposals.List(r.liveOf(b).consumer)) != 1 {
		t.Fatal("another conversation's proposal was acted on")
	}

	// A confirm without the revision the panel showed runs nothing.
	empty := r.call(t, "POST", "/api/console/conversations/"+b+"/proposals/"+theirs.ID+"/confirm", map[string]any{"revision": ""}, http.StatusConflict)
	if code(empty) != "operation_changed" || empty["proposal"] == nil || r.ran != 0 {
		t.Fatalf("an empty revision: %v", empty)
	}

	// A change whose value moved on since it was proposed is stale.
	r.call(t, "PUT", "/api/console/page", map[string]any{"page": agentcontext.PageSettings}, http.StatusOK)
	diff, err := r.mcp.Proposals.Propose(ctx, r.liveOf(b).consumer, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: "x", After: json.RawMessage(`"new"`)})
	if err != nil {
		t.Fatal(err)
	}
	r.settings.mu.Lock()
	r.settings.value, r.settings.revision = "edited", r.settings.revision+1
	r.settings.mu.Unlock()
	if answer := r.call(t, "POST", "/api/console/conversations/"+b+"/proposals/"+diff.ID+"/save", map[string]any{}, http.StatusConflict); code(answer) != "stale" {
		t.Fatalf("a stale save: %v", answer)
	}
	if r.settings.value != "edited" {
		t.Fatal("a stale save changed the value")
	}

	// A body over the bound is refused as invalid, as the other routes do.
	request := httptest.NewRequest("POST", "/api/console/conversations/"+a+"/messages", strings.NewReader(`{"text":"`+strings.Repeat("a", maxBody)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), CodeInvalidRequest) {
		t.Fatalf("an oversized body: %d %s", response.Code, response.Body.String())
	}
	if got, _ := r.service.Get(ctx, a); got.State != StateIdle {
		t.Fatalf("an oversized message ran: %+v", got)
	}
}

// Defaults that cannot be read refuse the conversation rather than open it
// with an agent nobody chose.
func TestCreateFailsWithoutItsDefaults(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Defaults = func(context.Context) (Defaults, error) { return Defaults{}, errors.New("preferences unreadable") }
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/console/conversations", strings.NewReader(`{}`))
	f.service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "internal_error") {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	if list, _ := f.service.List(context.Background()); len(list) != 0 {
		t.Fatalf("a conversation was opened: %+v", list)
	}
}

// Without a service the routes say so: a read as the local API reports a
// capability it lacks, a change with 503 and the routes' error shape.
func TestUnavailable(t *testing.T) {
	get := httptest.NewRecorder()
	Unavailable().ServeHTTP(get, httptest.NewRequest("GET", "/api/console/conversations", nil))
	if get.Code != http.StatusOK || strings.TrimSpace(get.Body.String()) != `{"available":false,"code":"console_unavailable"}` {
		t.Fatalf("GET: %d %s", get.Code, get.Body.String())
	}
	post := httptest.NewRecorder()
	Unavailable().ServeHTTP(post, httptest.NewRequest("POST", "/api/console/conversations", strings.NewReader(`{}`)))
	var answer map[string]string
	if err := json.Unmarshal(post.Body.Bytes(), &answer); err != nil || post.Code != http.StatusServiceUnavailable ||
		answer["code"] != "console_unavailable" || answer["message"] == "" {
		t.Fatalf("POST: %d %s", post.Code, post.Body.String())
	}
}
