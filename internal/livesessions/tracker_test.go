package livesessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

var t0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// linker links the directories it knows.
type linker struct {
	mu    sync.Mutex
	links map[string]Link
	calls int
}

func (l *linker) Link(_ context.Context, cwd string) (Link, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	link, ok := l.links[cwd]
	return link, ok
}

var repoLink = Link{Repository: "acme/demo", Branch: "feature", PR: 7}

func newLinker() *linker {
	return &linker{links: map[string]Link{"/work/demo": repoLink, "/work/demo/sub": repoLink}}
}

type source struct {
	records []hookevents.Record
	ch      chan hookevents.Record
}

func (s *source) Recent(int) ([]hookevents.Record, error) { return s.records, nil }
func (s *source) Subscribe(buffer int, _ func(hookevents.Event) bool) (<-chan hookevents.Record, func()) {
	s.ch = make(chan hookevents.Record, buffer)
	return s.ch, func() {}
}

func event(id, name string, at time.Time, cwd string) hookevents.Event {
	e := hookevents.New(hookevents.KindAgent, "claude-code", name, cwd, "test")
	e.Time = at
	e.Session = &hookevents.Session{ID: id, Cwd: cwd, TranscriptPath: "/var/transcripts/" + id + ".jsonl"}
	return e
}

func newTracker(c *clock, l Linker) *Tracker {
	return &Tracker{Source: &source{}, Linker: l, Now: c.Now}
}

func TestSessionLifecycle(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	ctx := context.Background()
	steps := []struct {
		name  string
		after time.Duration
		state string
		last  string
	}{
		{"session-start", 0, StateRunning, EventStart},
		{"user-prompt-submit", time.Minute, StateRunning, EventPrompt},
		{"post-task", time.Minute, StateRunning, EventTool},
		{"stop", time.Minute, StateIdle, EventStop},
		{"before-submit-prompt", time.Minute, StateRunning, EventPrompt},
		{"session-end", time.Minute, StateEnded, EventEnd},
	}
	for _, step := range steps {
		c.advance(step.after)
		if !tr.Apply(ctx, event("s1", step.name, c.now, "/work/demo")) {
			t.Fatalf("%s did not change the sessions", step.name)
		}
		got := tr.Sessions()
		if len(got) != 1 || got[0].State != step.state || got[0].LastEvent != step.last {
			t.Fatalf("after %s: %+v", step.name, got)
		}
	}
	got := tr.Sessions()[0]
	if !got.StartedAt.Equal(t0) || !got.LastActivityAt.Equal(c.now) || got.Repository != "acme/demo" || got.Branch != "feature" || got.PR != 7 || got.Agent != "claude-code" || got.ID != "s1" {
		t.Fatalf("session = %+v", got)
	}
	// An ended session is kept for EndedFor, then dropped.
	c.advance(EndedFor + time.Second)
	if !tr.Sweep() || len(tr.Sessions()) != 0 {
		t.Fatalf("ended session kept: %+v", tr.Sessions())
	}
}

func TestIdleAndDropTimeouts(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	ctx := context.Background()
	tr.Apply(ctx, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(IdleAfter)
	if tr.Sweep() || tr.Sessions()[0].State != StateRunning {
		t.Fatal("a session silent for exactly IdleAfter is still running")
	}
	c.advance(time.Second)
	if !tr.Sweep() || tr.Sessions()[0].State != StateIdle {
		t.Fatalf("silent session is not idle: %+v", tr.Sessions())
	}
	if tr.Sweep() {
		t.Fatal("a second sweep reported a change")
	}
	c.advance(DropAfter)
	if !tr.Sweep() || len(tr.Sessions()) != 0 {
		t.Fatalf("silent session not dropped: %+v", tr.Sessions())
	}
}

func TestResumedSessionStartsANewRun(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	ctx := context.Background()
	tr.Apply(ctx, event("s1", "session-start", c.now, "/work/demo"))
	c.advance(time.Minute)
	tr.Apply(ctx, event("s1", "session-end", c.now, "/work/demo"))
	// A late copy of an event from before the end changes nothing.
	if tr.Apply(ctx, event("s1", "post-task", t0.Add(30*time.Second), "/work/demo")) {
		t.Fatal("an event older than the end revived the session")
	}
	c.advance(time.Minute)
	resumed := c.now
	tr.Apply(ctx, event("s1", "session-start", resumed, "/work/demo"))
	got := tr.Sessions()
	if len(got) != 1 || got[0].State != StateRunning || !got[0].StartedAt.Equal(resumed) {
		t.Fatalf("resumed session = %+v", got)
	}
}

func TestUnregisteredDirectoryAndIncompleteEventsAreIgnored(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	ctx := context.Background()
	other := event("s1", "session-start", c.now, "/elsewhere")
	noID := event("", "session-start", c.now, "/work/demo")
	noCwd := event("s2", "session-start", c.now, "")
	git := hookevents.New(hookevents.KindGit, "", "post-commit", "/work/demo", "test")
	unknown := event("s3", "notification", c.now, "/work/demo")
	endOnly := event("s4", "session-end", c.now, "/work/demo")
	for name, e := range map[string]hookevents.Event{"unregistered": other, "no id": noID, "no cwd": noCwd, "git": git, "unknown name": unknown, "end of unseen": endOnly} {
		if tr.Apply(ctx, e) {
			t.Errorf("%s changed the sessions", name)
		}
	}
	if len(tr.Sessions()) != 0 {
		t.Fatalf("sessions = %+v", tr.Sessions())
	}
}

func TestEventDirectoryServesWhenThePayloadHasNone(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	e := event("s1", "session-start", c.now, "")
	e.Dir = "/work/demo/sub"
	if !tr.Apply(context.Background(), e) || len(tr.Sessions()) != 1 {
		t.Fatalf("sessions = %+v", tr.Sessions())
	}
}

func TestBranchFollowsTheSessionButLookupsAreThrottled(t *testing.T) {
	c := &clock{t0}
	l := newLinker()
	tr := newTracker(c, l)
	ctx := context.Background()
	tr.Apply(ctx, event("s1", "session-start", c.now, "/work/demo"))
	c.advance(time.Second)
	tr.Apply(ctx, event("s1", "post-task", c.now, "/work/demo"))
	if l.calls != 1 {
		t.Fatalf("lookups = %d, want 1 within RelinkEvery", l.calls)
	}
	l.mu.Lock()
	l.links["/work/demo"] = Link{Repository: "acme/demo", Branch: "other", PR: 9}
	l.mu.Unlock()
	c.advance(RelinkEvery)
	if !tr.Apply(ctx, event("s1", "post-task", c.now, "/work/demo")) {
		t.Fatal("a new branch is a change")
	}
	if got := tr.Sessions()[0]; got.Branch != "other" || got.PR != 9 {
		t.Fatalf("session = %+v", got)
	}
	// A directory that no longer resolves keeps the last link.
	l.mu.Lock()
	delete(l.links, "/work/demo")
	l.mu.Unlock()
	c.advance(RelinkEvery)
	tr.Apply(ctx, event("s1", "post-task", c.now, "/work/demo"))
	if got := tr.Sessions()[0]; got.Branch != "other" {
		t.Fatalf("session = %+v", got)
	}
}

func TestSessionWithoutPullRequestIsStillTracked(t *testing.T) {
	c := &clock{t0}
	l := &linker{links: map[string]Link{"/work/demo": {Repository: "acme/demo", Branch: "wip"}}}
	tr := newTracker(c, l)
	tr.Apply(context.Background(), event("s1", "session-start", c.now, "/work/demo"))
	got := tr.Sessions()
	if len(got) != 1 || got[0].PR != 0 || got[0].Branch != "wip" {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestBound(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	ctx := context.Background()
	for i := range MaxSessions + 8 {
		c.advance(time.Second)
		tr.Apply(ctx, event(fmt.Sprintf("s%02d", i), "user-prompt-submit", c.now, "/work/demo"))
	}
	got := tr.Sessions()
	if len(got) != MaxSessions {
		t.Fatalf("tracked %d sessions, want %d", len(got), MaxSessions)
	}
	// The least recently active made room: the newest remain.
	if got[0].ID != fmt.Sprintf("s%02d", MaxSessions+7) || got[len(got)-1].ID != "s08" {
		t.Fatalf("kept %s..%s", got[0].ID, got[len(got)-1].ID)
	}
	// An ended session is evicted before a running one.
	c.advance(time.Second)
	tr.Apply(ctx, event("s39", "session-end", c.now, "/work/demo"))
	c.advance(time.Second)
	tr.Apply(ctx, event("new", "session-start", c.now, "/work/demo"))
	for _, s := range tr.Sessions() {
		if s.ID == "s39" {
			t.Fatal("the ended session was kept over older running ones")
		}
	}
	if len(tr.Sessions()) != MaxSessions {
		t.Fatalf("tracked %d sessions", len(tr.Sessions()))
	}
}

func record(e hookevents.Event) hookevents.Record {
	return hookevents.Record{Received: e.Time, Via: hookevents.ViaSocket, Event: e}
}

func TestRebuildFromTheRecord(t *testing.T) {
	c := &clock{t0.Add(3 * time.Hour)}
	src := &source{}
	at := func(d time.Duration) time.Time { return c.now.Add(-d) }
	src.records = []hookevents.Record{
		// Recorded out of order, as a spooled event is: the stop is older
		// than the prompt after it.
		record(event("running", "session-start", at(30*time.Minute), "/work/demo")),
		record(event("running", "post-task", at(2*time.Minute), "/work/demo")),
		record(event("running", "stop", at(5*time.Minute), "/work/demo")),
		record(event("ended", "session-start", at(20*time.Minute), "/work/demo")),
		record(event("ended", "session-end", at(5*time.Minute), "/work/demo")),
		record(event("old", "user-prompt-submit", at(DropAfter+time.Minute), "/work/demo")),
		record(event("elsewhere", "session-start", at(time.Minute), "/elsewhere")),
		record(hookevents.Event{V: 1, ID: "g", Time: at(time.Minute), Kind: hookevents.KindGit, Name: "post-commit", Dir: "/work/demo"}),
	}
	tr := &Tracker{Source: src, Linker: newLinker(), Now: c.Now}
	tr.Rebuild(context.Background())
	got := map[string]Session{}
	for _, s := range tr.Sessions() {
		got[s.ID] = s
	}
	if len(got) != 2 {
		t.Fatalf("sessions = %+v", got)
	}
	if s := got["running"]; s.State != StateRunning || s.LastEvent != EventTool || !s.StartedAt.Equal(at(30*time.Minute)) || s.PR != 7 {
		t.Fatalf("running = %+v", s)
	}
	if s := got["ended"]; s.State != StateEnded {
		t.Fatalf("ended = %+v", s)
	}
}

func TestRunFollowsEventsAndNotifies(t *testing.T) {
	src := &source{}
	notified := make(chan struct{}, 16)
	tr := &Tracker{Source: src, Linker: newLinker(), Notify: func() { notified <- struct{}{} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification after the rebuild")
	}
	src.ch <- record(event("s1", "session-start", time.Now(), "/work/demo"))
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification for a new session")
	}
	if got := tr.Sessions(); len(got) != 1 || got[0].State != StateRunning {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestAPIPayloadHasNoPathsOrTranscript(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	e := event("s1", "user-prompt-submit", c.now, "/work/demo")
	e.Session.Reason, e.Session.Source = "private reason", "private source"
	tr.Apply(context.Background(), e)
	h := tr.Handler()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", "/api/live-sessions", nil))
	body := response.Body.String()
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", response.Code, body)
	}
	for _, leak := range []string{"/work", "/var/transcripts", "transcript", "jsonl", "cwd", "private"} {
		if strings.Contains(body, leak) {
			t.Fatalf("payload leaks %q: %s", leak, body)
		}
	}
	var decoded map[string][]map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	fields := decoded["sessions"][0]
	want := []string{"agent", "session_id", "state", "started_at", "last_activity_at", "last_event", "repository", "branch", "pr"}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v", fields)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing %s in %v", name, fields)
		}
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest("POST", "/api/live-sessions", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", post.Code)
	}
	empty := httptest.NewRecorder()
	newTracker(c, newLinker()).Handler().ServeHTTP(empty, httptest.NewRequest("GET", "/api/live-sessions", nil))
	if strings.TrimSpace(empty.Body.String()) != `{"sessions":[]}` {
		t.Fatalf("empty = %s", empty.Body.String())
	}
}
