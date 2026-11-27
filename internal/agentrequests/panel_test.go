package agentrequests

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A session the session panel runs is never asked about: its requests are
// decided at once by its own permission mode and never shown, it takes no
// queued message, and a request it had pending cannot be answered.
func TestPanelSessionsTakeNoAnswersOrMessages(t *testing.T) {
	s, _ := newStore(t)
	var panel atomic.Bool
	s.Panel = func(agent, id string) bool { return panel.Load() && agent == AgentClaudeCode && id == "s1" }

	// Pending before the panel claimed the session (its id became known).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, id := ask(t, ctx, s, bash("make"))
	panel.Store(true)
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow}); !errors.Is(err, ErrPanelSession) {
		t.Fatalf("answering: %v", err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/agent-requests/"+id+"/answer", strings.NewReader(`{"behavior":"allow"}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "panel_session") {
		t.Fatalf("answer route: %d %s", w.Code, w.Body.String())
	}

	d, err := s.Ask(context.Background(), bash("ls"))
	if err != nil || d.Behavior != "" {
		t.Fatalf("a panel session's request was held: %+v %v", d, err)
	}
	if requests, _ := s.List(); len(requests) != 1 {
		t.Fatalf("a panel session's request was shown: %+v", requests)
	}

	if err := s.Queue(AgentClaudeCode, "s1", "Ship it"); !errors.Is(err, ErrPanelSession) {
		t.Fatalf("queueing: %v", err)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/agent-messages", strings.NewReader(`{"agent":"claude-code","session_id":"s1","text":"Ship it"}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "panel_session") {
		t.Fatalf("message route: %d %s", w.Code, w.Body.String())
	}
	// Another session is unaffected.
	if err := s.Queue(AgentClaudeCode, "s2", "Ship it"); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("another session: %v", err)
	}
}
