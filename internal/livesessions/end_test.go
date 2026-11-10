package livesessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postEnd(f *transcriptFixture, agent, id string) int {
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/live-sessions/"+agent+"/"+id+"/end", nil))
	return w.Code
}

func TestOnlyAWaitingSessionOfAnAgentBaumCanStopEnds(t *testing.T) {
	f := newTranscriptFixture(t)
	var stopped []string
	f.live.Enders = map[string]Ender{AgentClaudeCode: func(_ context.Context, id string) error {
		stopped = append(stopped, id)
		return nil
	}}
	// Working: not offered, and refused.
	if s := served(t, f)["claude-code/s1"]; s.State != StateRunning {
		t.Fatalf("served %+v", s)
	}
	if listedEndable(t, f) || postEnd(f, "claude-code", "s1") != http.StatusConflict || len(stopped) != 0 {
		t.Fatalf("a working session was ended: %v", stopped)
	}
	// Its turn finished: offered, and stopped.
	f.clock.advance(1)
	stop(f)
	if !listedEndable(t, f) {
		t.Fatal("a waiting session is not endable")
	}
	if code := postEnd(f, "claude-code", "s1"); code != http.StatusAccepted || len(stopped) != 1 || stopped[0] != "s1" {
		t.Fatalf("answered %d, stopped %v", code, stopped)
	}
	if postEnd(f, "claude-code", "nobody") != http.StatusNotFound {
		t.Fatal("an unknown session was not 404")
	}
	// An agent with no Ender is never offered.
	f.live.Enders = nil
	if listedEndable(t, f) || postEnd(f, "claude-code", "s1") != http.StatusConflict {
		t.Fatal("ended without an Ender")
	}
}

// A waiting session its agent's EndCheck refuses, such as a Claude Code one
// still running a background command, is not offered End, and one ended all
// the same says why.
func TestASessionTheEndCheckRefusesIsNotOfferedEnd(t *testing.T) {
	f := newTranscriptFixture(t)
	var stopped int
	refusal := ErrRunningCommand
	f.live.Enders = map[string]Ender{AgentClaudeCode: func(context.Context, string) error {
		if refusal != nil {
			return refusal
		}
		stopped++
		return nil
	}}
	f.live.EndChecks = map[string]EndCheck{AgentClaudeCode: func(id string) error {
		if id != "s1" {
			t.Errorf("checked %q", id)
		}
		return refusal
	}}
	f.clock.advance(1)
	stop(f)
	if listedEndable(t, f) {
		t.Fatal("a session running a background command is offered End")
	}
	// A request sent from a list served before the command started.
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/live-sessions/claude-code/s1/end", nil))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"session_running_command"`) || stopped != 0 {
		t.Fatalf("answered %d %s, stopped %d", w.Code, w.Body, stopped)
	}
	// A busy process is not offered End either.
	refusal = errNotEndable
	if listedEndable(t, f) {
		t.Fatal("a busy session is offered End")
	}
	// A process that is gone is left for the Ender to record ended.
	f.live.EndChecks[AgentClaudeCode] = func(string) error { return ErrAgentGone }
	if !listedEndable(t, f) {
		t.Fatal("a session whose agent is gone is not offered End")
	}
	refusal = nil
	f.live.EndChecks[AgentClaudeCode] = func(string) error { return nil }
	if !listedEndable(t, f) || postEnd(f, "claude-code", "s1") != http.StatusAccepted || stopped != 1 {
		t.Fatalf("an idle session was not ended: stopped %d", stopped)
	}
}

func TestASessionWhoseAgentIsGoneIsRecordedEnded(t *testing.T) {
	f := newTranscriptFixture(t)
	f.live.Enders = map[string]Ender{AgentClaudeCode: func(context.Context, string) error { return ErrAgentGone }}
	stop(f)
	if code := postEnd(f, "claude-code", "s1"); code != http.StatusAccepted {
		t.Fatalf("answered %d", code)
	}
	if s := served(t, f)["claude-code/s1"]; s.State != StateEnded || s.WaitingReason != "" {
		t.Fatalf("served %+v", s)
	}
}

// stop ends the fixture session's turn.
func stop(f *transcriptFixture) {
	e := event("s1", "stop", f.clock.now, "/work/demo")
	e.Session.TranscriptPath = f.path
	apply(f.tracker, e)
}

// listedEndable is whether the list offers to end the fixture's session.
func listedEndable(t *testing.T, f *transcriptFixture) bool {
	t.Helper()
	for _, l := range f.live.listed() {
		if l.ID == "s1" {
			return l.Endable
		}
	}
	t.Fatal("s1 not listed")
	return false
}
