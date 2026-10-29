package livesessions

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/hookevents"
)

// notification is a Claude Code Notification hook's event.
func notification(id, kind, message string, at time.Time) hookevents.Event {
	e := event(id, "notification", at, "/work/demo")
	e.Session.NotificationType, e.Session.Message = kind, message
	return e
}

func codexEvent(id, name string, at time.Time) hookevents.Event {
	e := event(id, name, at, "/work/demo")
	e.Agent = "codex"
	return e
}

func only(t *testing.T, tr *Tracker) Session {
	t.Helper()
	got := tr.Sessions()
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	return got[0]
}

func TestEachWaitingReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wait    func(at time.Time) hookevents.Event
		reason  string
		message string
	}{
		{"permission prompt", func(at time.Time) hookevents.Event {
			return notification("s1", "permission_prompt", "Claude needs your permission to use Bash", at)
		}, WaitPermission, "Claude needs your permission to use Bash"},
		{"elicitation", func(at time.Time) hookevents.Event {
			return notification("s1", "elicitation_dialog", "An MCP server asks for input", at)
		}, WaitInput, "An MCP server asks for input"},
		{"background agent", func(at time.Time) hookevents.Event {
			return notification("s1", "agent_needs_input", "", at)
		}, WaitInput, ""},
		{"a type baum does not know", func(at time.Time) hookevents.Event {
			return notification("s1", "something_new", "Claude wants you", at)
		}, WaitInput, "Claude wants you"},
		{"no type", func(at time.Time) hookevents.Event { return notification("s1", "", "Claude is waiting", at) }, WaitInput, "Claude is waiting"},
		{"idle prompt", func(at time.Time) hookevents.Event {
			return notification("s1", "idle_prompt", "Claude is waiting for your input", at)
		}, WaitTurnFinished, "Claude is waiting for your input"},
		{"stop", func(at time.Time) hookevents.Event { return event("s1", "stop", at, "/work/demo") }, WaitTurnFinished, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &clock{t0}
			tr := newTracker(c, newLinker())
			apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
			if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning {
				t.Fatalf("working = %+v", s)
			}
			c.advance(time.Minute)
			if !apply(tr, tc.wait(c.now)) {
				t.Fatal("the wait did not change the sessions")
			}
			s := only(t, tr)
			if s.WaitingReason != tc.reason || !s.WaitingSince.Equal(c.now) || s.WaitingMessage != tc.message || s.State != StateIdle {
				t.Fatalf("waiting = %+v", s)
			}
		})
	}
	t.Run("codex permission request", func(t *testing.T) {
		c := &clock{t0}
		tr := newTracker(c, newLinker())
		apply(tr, codexEvent("c1", "post-tool-use", c.now))
		c.advance(time.Minute)
		apply(tr, codexEvent("c1", "permission-request", c.now))
		if s := only(t, tr); s.WaitingReason != WaitPermission || !s.WaitingSince.Equal(c.now) || s.LastEvent != EventWait || s.Agent != "codex" {
			t.Fatalf("waiting = %+v", s)
		}
	})
}

func TestAnInterruptedWaitWaitsForTheNextPrompt(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	if tr.Interrupted("claude-code", "s1", c.now) {
		t.Fatal("a running session waits for nothing to turn down")
	}
	c.advance(time.Minute)
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now))
	if tr.Interrupted("claude-code", "s1", c.now.Add(-time.Second)) {
		t.Fatal("an interrupt older than the wait")
	}
	c.advance(time.Minute)
	if !tr.Interrupted("claude-code", "s1", c.now) {
		t.Fatal("the interrupt changed nothing")
	}
	if s := only(t, tr); s.WaitingReason != WaitTurnFinished || !s.WaitingSince.Equal(c.now) || s.WaitingMessage != "" || s.State != StateIdle {
		t.Fatalf("after the interrupt: %+v", s)
	}
	if tr.Interrupted("claude-code", "s2", c.now) {
		t.Fatal("an unknown session")
	}
	c.advance(time.Minute)
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning {
		t.Fatalf("after the next prompt: %+v", s)
	}
}

func TestAnAnsweredWaitWorksAgain(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(time.Minute)
	apply(tr, event("s1", "permission-request", c.now, "/work/demo"))
	c.advance(time.Second)
	if !tr.Answered("claude-code", "s1", c.now) {
		t.Fatal("the answer changed nothing")
	}
	if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning || s.LastEvent != EventTool || !s.LastActivityAt.Equal(c.now) {
		t.Fatalf("after the answer: %+v", s)
	}
	// A finished turn is not a wait the app answers.
	c.advance(time.Minute)
	apply(tr, event("s1", "stop", c.now, "/work/demo"))
	if tr.Answered("claude-code", "s1", c.now) {
		t.Fatal("answered a finished turn")
	}
}

func TestADeliveredMessageResumesTheTurn(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(time.Minute)
	stopped := c.now
	apply(tr, event("s1", "stop", stopped, "/work/demo"))
	c.advance(time.Second)
	if !tr.Resumed("claude-code", "s1", c.now) {
		t.Fatal("the delivery changed nothing")
	}
	if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning || s.LastEvent != EventPrompt || !s.LastActivityAt.Equal(c.now) {
		t.Fatalf("after the delivery: %+v", s)
	}
	// The stop that delivered it, recorded late, is old news.
	apply(tr, event("s1", "stop", stopped, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning {
		t.Fatalf("the delivering stop finished the turn: %+v", s)
	}
	// Delivered before the tracker saw its stop: the stop stays old news.
	c.advance(time.Minute)
	stopped = c.now
	c.advance(time.Second)
	tr.Resumed("claude-code", "s1", c.now)
	apply(tr, event("s1", "stop", stopped, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != "" || s.State != StateRunning {
		t.Fatalf("a stop recorded after its delivery finished the turn: %+v", s)
	}
	// A wait for the user is not one a message ends.
	c.advance(time.Minute)
	apply(tr, event("s1", "permission-request", c.now, "/work/demo"))
	c.advance(time.Second)
	if tr.Resumed("claude-code", "s1", c.now) {
		t.Fatal("a delivery ended a permission wait")
	}
}

func TestWorkClearsAWaitAndAWaitGoesOn(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	for _, work := range []string{"post-tool-use", "user-prompt-submit", "session-start", "pre-task", "subagent-start"} {
		c.advance(time.Minute)
		apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Edit", c.now))
		if s := only(t, tr); s.WaitingReason != WaitPermission {
			t.Fatalf("before %s: %+v", work, s)
		}
		c.advance(time.Minute)
		apply(tr, event("s1", work, c.now, "/work/demo"))
		if s := only(t, tr); s.WaitingReason != "" || !s.WaitingSince.IsZero() || s.WaitingMessage != "" || s.State != StateRunning {
			t.Fatalf("after %s: %+v", work, s)
		}
	}
	// A subagent that finishes while the session waits leaves the wait.
	c.advance(time.Minute)
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Edit", c.now))
	c.advance(time.Minute)
	apply(tr, event("s1", "subagent-stop", c.now, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != WaitPermission || s.State != StateIdle {
		t.Fatalf("after subagent-stop: %+v", s)
	}
	// A stop, then the subagent Claude Code runs after every stop, then its
	// idle_prompt a minute later: the same wait.
	c.advance(time.Minute)
	stopped := c.now
	apply(tr, event("s1", "stop", c.now, "/work/demo"))
	c.advance(2 * time.Second)
	apply(tr, event("s1", "subagent-stop", c.now, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != WaitTurnFinished || s.State != StateIdle {
		t.Fatalf("after the stop's subagent-stop: %+v", s)
	}
	c.advance(time.Minute)
	apply(tr, notification("s1", "idle_prompt", "Claude is waiting for your input", c.now))
	if s := only(t, tr); s.WaitingReason != WaitTurnFinished || !s.WaitingSince.Equal(stopped) {
		t.Fatalf("after idle_prompt: %+v", s)
	}
	// A question asked at the end of a turn is a new wait; an idle prompt
	// after it does not hide it.
	c.advance(time.Minute)
	asked := c.now
	apply(tr, notification("s1", "elicitation_dialog", "Pick a database", c.now))
	c.advance(time.Minute)
	apply(tr, notification("s1", "idle_prompt", "Claude is waiting for your input", c.now))
	if s := only(t, tr); s.WaitingReason != WaitInput || !s.WaitingSince.Equal(asked) || s.WaitingMessage != "Pick a database" {
		t.Fatalf("after the question: %+v", s)
	}
	// The same reason again keeps when the wait began.
	c.advance(time.Minute)
	apply(tr, notification("s1", "elicitation_dialog", "Pick a database, please", c.now))
	if s := only(t, tr); !s.WaitingSince.Equal(asked) || s.WaitingMessage != "Pick a database, please" {
		t.Fatalf("again: %+v", s)
	}
	// The end clears it.
	c.advance(time.Minute)
	apply(tr, event("s1", "session-end", c.now, "/work/demo"))
	if s := only(t, tr); s.WaitingReason != "" || s.WaitingMessage != "" || s.State != StateEnded {
		t.Fatalf("ended: %+v", s)
	}
}

func TestNotificationsThatAskNothingAreIgnored(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	for _, kind := range []string{"auth_success", "elicitation_complete", "elicitation_response", "agent_completed", "quota_auto_resume_fired", "plugin_notification"} {
		if apply(tr, notification("quiet", kind, "Signed in", c.now)) || len(tr.Sessions()) != 0 {
			t.Fatalf("%s started a session: %+v", kind, tr.Sessions())
		}
	}
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(time.Minute)
	if apply(tr, notification("s1", "auth_success", "Signed in", c.now)) {
		t.Fatal("auth_success changed the sessions")
	}
	if s := only(t, tr); s.WaitingReason != "" || s.LastEvent != EventPrompt || !s.LastActivityAt.Equal(t0) {
		t.Fatalf("after auth_success: %+v", s)
	}
}

func TestWaitingMessageIsCleanedMaskedAndBounded(t *testing.T) {
	c := &clock{t0}
	var masked []string
	tr := newTracker(c, newLinker())
	tr.Mask = func(text string) string {
		masked = append(masked, text)
		return strings.ReplaceAll(text, "hunter2", "*******")
	}
	long := "Claude needs\x1b[31m your\r\npermission\u202e to use\u200b Bash  " + strings.Repeat("é", 200) + " hunter2"
	apply(tr, notification("s1", "permission_prompt", long, c.now))
	s := only(t, tr)
	if !strings.HasPrefix(s.WaitingMessage, "Claude needs [31m your permission to use Bash éé") {
		t.Errorf("message = %q", s.WaitingMessage)
	}
	if n := utf8.RuneCountInString(s.WaitingMessage); n != maxWaitingMessage || !strings.HasSuffix(s.WaitingMessage, "…") {
		t.Errorf("message has %d characters: %q", n, s.WaitingMessage)
	}
	for _, r := range s.WaitingMessage {
		if r < ' ' || r == '\u202e' || r == '\u200b' {
			t.Errorf("message keeps %U", r)
		}
	}
	// The mask saw the whole text, before it was cut.
	if len(masked) != 1 || !strings.HasSuffix(masked[0], "hunter2") {
		t.Errorf("masked = %q", masked)
	}
	c.advance(time.Minute)
	apply(tr, notification("s1", "permission_prompt", "use hunter2", c.now))
	if s := only(t, tr); s.WaitingMessage != "use *******" {
		t.Errorf("masked message = %q", s.WaitingMessage)
	}
}

func TestWaitsAreAnnouncedOncePerWait(t *testing.T) {
	c := &clock{t0}
	var told []Session
	tr := newTracker(c, newLinker())
	tr.Waiting = func(s Session) { told = append(told, s) }
	// A new session's first event is a permission prompt: it is announced
	// once the session is linked and shown.
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now))
	if len(told) != 1 || told[0].WaitingReason != WaitPermission || told[0].Repository != "acme/demo" || told[0].WaitingMessage == "" {
		t.Fatalf("told = %+v", told)
	}
	// The same prompt again is the same wait.
	c.advance(10 * time.Second)
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now))
	// A finished turn is never announced.
	c.advance(time.Minute)
	apply(tr, event("s1", "stop", c.now, "/work/demo"))
	c.advance(time.Minute)
	apply(tr, notification("s1", "idle_prompt", "Claude is waiting for your input", c.now))
	if len(told) != 1 {
		t.Fatalf("told = %+v", told)
	}
	// A question at the end of the turn, then after work another prompt.
	c.advance(time.Minute)
	apply(tr, notification("s1", "elicitation_dialog", "Pick one", c.now))
	c.advance(time.Minute)
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(time.Minute)
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Edit", c.now))
	if len(told) != 3 || told[1].WaitingReason != WaitInput || told[2].WaitingMessage != "Claude needs your permission to use Edit" {
		t.Fatalf("told = %+v", told)
	}
	// A wait the spool delivers long after it began is old news.
	c.advance(time.Minute)
	apply(tr, event("s1", "post-tool-use", c.now, "/work/demo"))
	c.advance(time.Minute)
	late := notification("s2", "permission_prompt", "late", c.now.Add(-AnnounceWithin-time.Second))
	apply(tr, late)
	if len(told) != 3 {
		t.Fatalf("a late wait was announced: %+v", told)
	}
	// A session in no registered clone is never shown, so never announced.
	apply(tr, func() hookevents.Event {
		e := notification("s3", "permission_prompt", "elsewhere", c.now)
		e.Session.Cwd = "/elsewhere"
		return e
	}())
	if len(told) != 3 {
		t.Fatalf("an unregistered session was announced: %+v", told)
	}
}

func TestRebuildAnnouncesNoReplayedWait(t *testing.T) {
	c := &clock{t0.Add(time.Hour)}
	src := &source{records: []hookevents.Record{
		record(event("s1", "user-prompt-submit", c.now.Add(-3*time.Minute), "/work/demo")),
		record(notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now.Add(-time.Minute))),
	}}
	var told []Session
	tr := &Tracker{Source: src, Linker: newLinker(), Now: c.Now, Waiting: func(s Session) { told = append(told, s) }}
	tr.Rebuild(context.Background())
	tr.ResolvePending(context.Background())
	s := only(t, tr)
	if s.WaitingReason != WaitPermission || !s.WaitingSince.Equal(c.now.Add(-time.Minute)) || s.WaitingMessage != "Claude needs your permission to use Bash" {
		t.Fatalf("rebuilt = %+v", s)
	}
	if len(told) != 0 {
		t.Fatalf("a replayed wait was announced: %+v", told)
	}
	// The next wait is news.
	apply(tr, event("s1", "post-tool-use", c.now, "/work/demo"))
	c.advance(time.Minute)
	apply(tr, notification("s1", "permission_prompt", "again", c.now))
	if len(told) != 1 {
		t.Fatalf("told = %+v", told)
	}
}

// The record a daemon keeps holds events of older hooks, without the
// notification fields, and of newer ones; both rebuild.
func TestRebuildReadsOldAndNewEventRecords(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Now().UTC().Truncate(time.Second)}
	at := func(d time.Duration) string { return c.now.Add(-d).Format(time.RFC3339) }
	lines := []string{
		`{"received":"` + at(5*time.Minute) + `","via":"socket","event":{"v":1,"id":"e1","time":"` + at(5*time.Minute) + `","kind":"agent","agent":"claude-code","name":"user-prompt-submit","dir":"/work/demo","client":"v0.3.0","session":{"id":"old","cwd":"/work/demo"}}}`,
		`{"received":"` + at(4*time.Minute) + `","via":"socket","event":{"v":1,"id":"e2","time":"` + at(4*time.Minute) + `","kind":"agent","agent":"claude-code","name":"stop","dir":"/work/demo","client":"v0.3.0","session":{"id":"old","cwd":"/work/demo"}}}`,
		`{"received":"` + at(2*time.Minute) + `","via":"socket","event":{"v":1,"id":"e3","time":"` + at(2*time.Minute) + `","kind":"agent","agent":"claude-code","name":"notification","dir":"/work/demo","client":"v0.4.0","session":{"id":"new","cwd":"/work/demo","hook_event":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash","later_field":"x"}}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := hookevents.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	tr := &Tracker{Source: store, Linker: newLinker(), Now: c.Now}
	tr.Rebuild(context.Background())
	tr.ResolvePending(context.Background())
	got := map[string]Session{}
	for _, s := range tr.Sessions() {
		got[s.ID] = s
	}
	if s := got["old"]; s.WaitingReason != WaitTurnFinished || s.State != StateIdle {
		t.Errorf("old = %+v", s)
	}
	if s := got["new"]; s.WaitingReason != WaitPermission || s.WaitingMessage != "Claude needs your permission to use Bash" {
		t.Errorf("new = %+v", s)
	}
}

func TestEndedSessionsFileStaysCompatible(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0}
	tr := &Tracker{Source: &source{}, Linker: newLinker(), Now: c.Now, StateDir: dir}
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now))
	c.advance(time.Minute)
	apply(tr, event("s1", "session-end", c.now, "/work/demo"))
	tr.persist()
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil || strings.Contains(string(data), "waiting") || strings.Contains(string(data), "permission") {
		t.Fatalf("state file = %s, %v", data, err)
	}
	again := &Tracker{Source: &source{}, Linker: newLinker(), Now: c.Now, StateDir: dir}
	again.Rebuild(context.Background())
	if s := only(t, again); s.ID != "s1" || s.State != StateEnded || s.WaitingReason != "" {
		t.Fatalf("restored = %+v", s)
	}
	// A file an older daemon wrote, and one with fields of a newer one, load.
	old := `{"sessions":[{"agent":"codex","session_id":"c1","state":"ended","started_at":"2026-10-20T08:00:00Z","last_activity_at":"2026-10-20T08:30:00Z","last_event":"end","ended_at":"2026-10-20T08:30:00Z","repository":"acme/demo","branch":"main","later":{"x":1}}]}`
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	older := &Tracker{Source: &source{}, Linker: newLinker(), Now: c.Now, StateDir: dir}
	older.Rebuild(context.Background())
	if s := only(t, older); s.ID != "c1" || s.Agent != "codex" || s.State != StateEnded || s.Branch != "main" {
		t.Fatalf("restored old = %+v", s)
	}
}

func TestAPIServesTheWait(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, notification("s1", "permission_prompt", "Claude needs your permission to use Bash", c.now))
	response := httptest.NewRecorder()
	tr.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/live-sessions", nil))
	var decoded struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || len(decoded.Sessions) != 1 {
		t.Fatalf("%s: %v", response.Body, err)
	}
	s := decoded.Sessions[0]
	if s["state"] != StateIdle || s["last_event"] != EventWait || s["waiting_reason"] != WaitPermission ||
		s["waiting_since"] != t0.Format(time.RFC3339) || s["waiting_message"] != "Claude needs your permission to use Bash" {
		t.Fatalf("session = %v", s)
	}
}
