package livesessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/focus"
	"github.com/baum-ai/baum/internal/hookevents"
)

// focuser brings forward what it is handed and records it.
type focuser struct {
	result  focus.Result
	focused []*hookevents.Terminal
}

func (f *focuser) Predict(_ string, terminal *hookevents.Terminal) string {
	return "pane " + terminal.Env["TMUX_PANE"]
}

func (f *focuser) Focus(_ context.Context, _, _ string, terminal *hookevents.Terminal) focus.Result {
	f.focused = append(f.focused, terminal)
	return f.result
}

func located(e hookevents.Event, pane string) hookevents.Event {
	e.Terminal = &hookevents.Terminal{Env: map[string]string{"TMUX_PANE": pane}, TTY: "/dev/pts/" + pane}
	return e
}

func post(h http.Handler, path string) (int, map[string]string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// The latest terminal a session's hooks recorded is the one brought forward;
// a late delivery of an older one does not replace it. The list says how the
// session can be brought forward but never where it runs.
func TestFocusUsesTheLatestTerminal(t *testing.T) {
	fx := newTranscriptFixture(t)
	f := &focuser{result: focus.Result{Precision: focus.Session}}
	fx.live.Focus = f
	h := fx.live.Handler()
	fx.clock.advance(time.Minute)
	at := func(e hookevents.Event) hookevents.Event {
		e.Session.TranscriptPath = fx.path
		return e
	}
	apply(fx.tracker, at(located(event("s1", "session-start", t0, "/work/demo"), "1")))
	apply(fx.tracker, at(located(event("s1", "user-prompt-submit", t0.Add(2*time.Second), "/work/demo"), "2")))
	apply(fx.tracker, at(located(event("s1", "user-prompt-submit", t0.Add(time.Second), "/work/demo"), "late")))
	apply(fx.tracker, at(event("s1", "post-tool-use", t0.Add(3*time.Second), "/work/demo")))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	if body := w.Body.String(); !strings.Contains(body, `"focus":"pane 2"`) || strings.Contains(body, "/dev/pts") {
		t.Fatalf("list = %s", body)
	}
	code, body := post(h, "/api/live-sessions/claude-code/s1/focus")
	if code != http.StatusOK || body["focused"] != "session" || len(f.focused) != 1 || f.focused[0].TTY != "/dev/pts/2" {
		t.Fatalf("%d %v, focused %+v", code, body, f.focused)
	}

	f.result = focus.Result{Precision: focus.Window, Message: "Turn on remote control."}
	if code, body := post(h, "/api/live-sessions/claude-code/s1/focus"); code != http.StatusOK || body["focused"] != "window" || body["message"] != "Turn on remote control." {
		t.Fatalf("window: %d %v", code, body)
	}
	f.result = focus.Result{Message: "The session's process has exited."}
	if code, body := post(h, "/api/live-sessions/claude-code/s1/focus"); code != http.StatusConflict || body["code"] != "focus_failed" || body["message"] != f.result.Message {
		t.Fatalf("failed: %d %v", code, body)
	}
	if code, body := post(h, "/api/live-sessions/claude-code/other/focus"); code != http.StatusNotFound || body["code"] != "live_session_not_found" {
		t.Fatalf("unknown: %d %v", code, body)
	}
}

// A session that ended, or whose hooks never said where it runs, is listed
// without focus and is not brought forward.
func TestFocusNeedsARunningLocatedSession(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	f := &focuser{result: focus.Result{Precision: focus.Session}}
	h := (&Transcripts{Tracker: tr, Focus: f}).Handler()
	apply(tr, event("unlocated", "session-start", c.now, "/work/demo"))
	apply(tr, located(event("ended", "session-start", c.now, "/work/demo"), "1"))
	apply(tr, event("ended", "session-end", c.now.Add(time.Second), "/work/demo"))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	if strings.Contains(w.Body.String(), `"focus"`) {
		t.Fatalf("list = %s", w.Body.String())
	}
	for _, id := range []string{"unlocated", "ended"} {
		if code, body := post(h, "/api/live-sessions/claude-code/"+id+"/focus"); code != http.StatusConflict || body["code"] != "focus_failed" {
			t.Fatalf("%s: %d %v", id, code, body)
		}
	}
	if len(f.focused) != 0 {
		t.Fatalf("focused %v", f.focused)
	}
	// Without a focuser there is no route.
	if code, _ := post((&Transcripts{Tracker: tr}).Handler(), "/api/live-sessions/claude-code/unlocated/focus"); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Fatalf("no focuser: %d", code)
	}
}
