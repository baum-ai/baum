//go:build !windows

package livesessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A session the panel runs is offered no end, message or focus, and their
// routes refuse it with panel_session, whatever state it is in; the agent
// is never reached.
func TestPanelSessionsOfferNoActions(t *testing.T) {
	f := newTranscriptFixture(t)
	reached := 0
	f.live.Senders = map[string]Sender{AgentClaudeCode: func(context.Context, string, string) error { reached++; return nil }}
	f.live.Enders = map[string]Ender{AgentClaudeCode: func(context.Context, string) error { reached++; return nil }}
	stop(f)
	// Before the panel claims it, the person's own session at its prompt
	// is offered both.
	if l := listedS1(t, f); !l.Endable || !l.Messageable {
		t.Fatalf("listed %+v", l)
	}
	f.tracker.Panel = func(agent, id, _ string) (string, bool) { return "c1", agent == AgentClaudeCode && id == "s1" }
	l := listedS1(t, f)
	if l.Origin != OriginPanel || l.Conversation != "c1" || l.Endable || l.Messageable || l.Focus != "" || l.EndBlocked != "" {
		t.Fatalf("listed %+v", l)
	}
	for _, action := range []struct{ path, body string }{
		{"/api/live-sessions/claude-code/s1/end", ""},
		{"/api/live-sessions/claude-code/s1/message", `{"text":"Ship it"}`},
	} {
		w := httptest.NewRecorder()
		f.live.Handler().ServeHTTP(w, httptest.NewRequest("POST", action.path, strings.NewReader(action.body)))
		var answer map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &answer)
		if w.Code != http.StatusConflict || answer["code"] != "panel_session" {
			t.Errorf("%s: %d %s", action.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/live-sessions/claude-code/s1/focus", nil)
	request.SetPathValue("agent", "claude-code")
	request.SetPathValue("id", "s1")
	f.live.focusHandler(w, request)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "panel_session") {
		t.Errorf("focus: %d %s", w.Code, w.Body.String())
	}
	if reached != 0 {
		t.Fatalf("the agent was reached %d times", reached)
	}
}
