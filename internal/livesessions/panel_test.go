package livesessions

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// panelOwner claims the directory of a conversation's empty directory, and a
// session by id, as the console does.
func panelOwner(dirs map[string]string, ids map[string]string) func(agent, id, cwd string) (string, bool) {
	return func(agent, id, cwd string) (string, bool) {
		if conversation, ok := ids[agent+":"+id]; ok {
			return conversation, true
		}
		conversation, ok := dirs[cwd]
		return conversation, ok
	}
}

// A panel session in the empty directory baum made for its conversation is
// in no clone, yet it is listed, without a repository, marked as the panel's;
// any other session there is still dropped.
func TestPanelSessionInItsOwnDirectoryIsListed(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	tr.Panel = panelOwner(map[string]string{"/cache/console/sessions/c1": "c1"}, nil)
	if !apply(tr, event("s1", "session-start", c.now, "/cache/console/sessions/c1")) {
		t.Fatal("the panel session was not listed")
	}
	apply(tr, event("s2", "session-start", c.now, "/elsewhere"))
	got := tr.Sessions()
	if len(got) != 1 || got[0].ID != "s1" || got[0].Repository != "" || got[0].LinkStatus != LinkPanel || got[0].Origin != OriginPanel || got[0].Conversation != "c1" {
		t.Fatalf("sessions = %+v", got)
	}
	// It stays listed as it works and after a relink, and ends as any session.
	c.advance(RelinkEvery + 1)
	apply(tr, event("s1", "user-prompt-submit", c.now, "/cache/console/sessions/c1"))
	tr.Sweep()
	tr.ResolvePending(t.Context())
	c.advance(1)
	apply(tr, event("s1", "session-end", c.now, "/cache/console/sessions/c1"))
	got = tr.Sessions()
	if len(got) != 1 || got[0].State != StateEnded || got[0].LinkStatus != LinkPanel || got[0].Origin != OriginPanel {
		t.Fatalf("after it ended: %+v", got)
	}

	// The live sessions answer, which the Sessions page reads, carries the
	// marker and the conversation.
	response := httptest.NewRecorder()
	tr.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/live-sessions", nil))
	var decoded struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || len(decoded.Sessions) != 1 ||
		decoded.Sessions[0]["origin"] != OriginPanel || decoded.Sessions[0]["conversation"] != "c1" {
		t.Fatalf("answer %s %v", response.Body.String(), err)
	}
	if strings.Contains(response.Body.String(), "/cache/console") {
		t.Fatalf("the answer names the directory: %s", response.Body.String())
	}
}

// A panel session in a pull request's worktree is linked as any session there
// and marked as the panel's; the person's own session in that worktree is not.
func TestPanelSessionInAWorktreeIsMarked(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	tr.Panel = panelOwner(nil, map[string]string{"claude-code:s1": "c2"})
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	apply(tr, event("s2", "session-start", c.now, "/work/demo"))
	got := tr.Sessions()
	if len(got) != 2 {
		t.Fatalf("sessions = %+v", got)
	}
	for _, s := range got {
		if s.Repository != "acme/demo" || s.PR != 7 {
			t.Errorf("not linked: %+v", s)
		}
		switch s.ID {
		case "s1":
			if s.Origin != OriginPanel || s.Conversation != "c2" {
				t.Errorf("the panel session is not marked: %+v", s)
			}
		default:
			if s.Origin != "" || s.Conversation != "" {
				t.Errorf("the person's own session is marked: %+v", s)
			}
		}
	}
}

// Without Panel, a session in an unregistered directory is dropped as before.
func TestWithoutPanelUnlinkedSessionsAreDropped(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	if apply(tr, event("s1", "session-start", c.now, "/cache/console/sessions/c1")) || len(tr.Sessions()) != 0 {
		t.Fatalf("sessions = %+v", tr.Sessions())
	}
}
