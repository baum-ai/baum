package livesessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	// ready, when set, is closed once something subscribed.
	ready   chan struct{}
	drops   atomic.Int64
	recents atomic.Int64
}

// add records e and hands it to the subscriber as the store does: a full
// buffer drops it.
func (s *source) add(e hookevents.Event) {
	r := record(e)
	s.records = append(s.records, r)
	select {
	case s.ch <- r:
	default:
		s.drops.Add(1)
	}
}

func (s *source) Recent(int) ([]hookevents.Record, error) {
	s.recents.Add(1)
	return s.records, nil
}
func (s *source) SubscribeCounted(buffer int, _ func(hookevents.Event) bool) (<-chan hookevents.Record, func(), func() uint64) {
	s.ch = make(chan hookevents.Record, buffer)
	if s.ready != nil {
		close(s.ready)
	}
	return s.ch, func() {}, func() uint64 { return uint64(s.drops.Load()) }
}

func event(id, name string, at time.Time, cwd string) hookevents.Event {
	e := hookevents.New(hookevents.KindAgent, "claude-code", name, cwd, "test")
	e.Time = at
	e.Session = &hookevents.Session{ID: id, Cwd: cwd, TranscriptPath: "/var/transcripts/" + id + ".jsonl"}
	return e
}

// apply applies the event and looks up the link it needs, as Run does in the
// background, and reports whether either changed what the app sees.
func apply(tr *Tracker, e hookevents.Event) bool {
	applied := tr.Apply(context.Background(), e)
	resolved := tr.ResolvePending(context.Background())
	return applied || resolved
}

func newTracker(c *clock, l Linker) *Tracker {
	return &Tracker{Source: &source{}, Linker: l, Now: c.Now}
}

func TestSessionLifecycle(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
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
		if !apply(tr, event("s1", step.name, c.now, "/work/demo")) {
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
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
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
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	c.advance(time.Minute)
	apply(tr, event("s1", "session-end", c.now, "/work/demo"))
	// A late copy of an event from before the end changes nothing.
	if apply(tr, event("s1", "post-task", t0.Add(30*time.Second), "/work/demo")) {
		t.Fatal("an event older than the end revived the session")
	}
	c.advance(time.Minute)
	resumed := c.now
	apply(tr, event("s1", "session-start", resumed, "/work/demo"))
	got := tr.Sessions()
	if len(got) != 1 || got[0].State != StateRunning || !got[0].StartedAt.Equal(resumed) {
		t.Fatalf("resumed session = %+v", got)
	}
}

func TestUnregisteredDirectoryAndIncompleteEventsAreIgnored(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	other := event("s1", "session-start", c.now, "/elsewhere")
	noID := event("", "session-start", c.now, "/work/demo")
	noCwd := event("s2", "session-start", c.now, "")
	git := hookevents.New(hookevents.KindGit, "", "post-commit", "/work/demo", "test")
	unknown := event("s3", "notification", c.now, "/work/demo")
	endOnly := event("s4", "session-end", c.now, "/work/demo")
	for name, e := range map[string]hookevents.Event{"unregistered": other, "no id": noID, "no cwd": noCwd, "git": git, "unknown name": unknown, "end of unseen": endOnly} {
		if apply(tr, e) {
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
	if !apply(tr, e) || len(tr.Sessions()) != 1 {
		t.Fatalf("sessions = %+v", tr.Sessions())
	}
}

func TestBranchFollowsTheSessionButLookupsAreThrottled(t *testing.T) {
	c := &clock{t0}
	l := newLinker()
	tr := newTracker(c, l)
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	c.advance(time.Second)
	apply(tr, event("s1", "post-task", c.now, "/work/demo"))
	if l.calls != 1 {
		t.Fatalf("lookups = %d, want 1 within RelinkEvery", l.calls)
	}
	l.mu.Lock()
	l.links["/work/demo"] = Link{Repository: "acme/demo", Branch: "other", PR: 9}
	l.mu.Unlock()
	c.advance(RelinkEvery)
	if !apply(tr, event("s1", "post-task", c.now, "/work/demo")) {
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
	apply(tr, event("s1", "post-task", c.now, "/work/demo"))
	if got := tr.Sessions()[0]; got.Branch != "other" {
		t.Fatalf("session = %+v", got)
	}
}

func TestSessionWithoutPullRequestIsStillTracked(t *testing.T) {
	c := &clock{t0}
	l := &linker{links: map[string]Link{"/work/demo": {Repository: "acme/demo", Branch: "wip"}}}
	tr := newTracker(c, l)
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	got := tr.Sessions()
	if len(got) != 1 || got[0].PR != 0 || got[0].Branch != "wip" {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestBound(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	for i := range MaxSessions + 8 {
		c.advance(time.Second)
		apply(tr, event(fmt.Sprintf("s%02d", i), "user-prompt-submit", c.now, "/work/demo"))
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
	apply(tr, event("s39", "session-end", c.now, "/work/demo"))
	c.advance(time.Second)
	apply(tr, event("new", "session-start", c.now, "/work/demo"))
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
	tr.ResolvePending(context.Background())
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
	src := &source{ready: make(chan struct{})}
	notified := make(chan struct{}, 16)
	tr := &Tracker{Source: src, Linker: newLinker(), Notify: func() { notified <- struct{}{} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	<-src.ready
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
	apply(tr, e)
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

func TestApplyNeverWaitsForTheLinker(t *testing.T) {
	c := &clock{t0}
	l := newLinker()
	tr := newTracker(c, l)
	if tr.Apply(context.Background(), event("s1", "session-start", c.now, "/work/demo")) || l.calls != 0 || len(tr.Sessions()) != 0 {
		t.Fatalf("a session shows before it is linked (lookups %d)", l.calls)
	}
	if !tr.ResolvePending(context.Background()) || l.calls != 1 || len(tr.Sessions()) != 1 {
		t.Fatalf("the link was not looked up: %d %+v", l.calls, tr.Sessions())
	}
}

func TestBusySessionsDoNotNotifyForActivityAlone(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	apply(tr, event("s1", "user-prompt-submit", c.now, "/work/demo"))
	for range 5 {
		c.advance(time.Second)
		if apply(tr, event("s1", "post-task", c.now, "/work/demo")) {
			t.Fatal("tool activity alone is a notification")
		}
	}
	if got := tr.Sessions()[0]; got.LastActivityAt.Sub(t0) != 5*time.Second || got.LastEvent != EventTool {
		t.Fatalf("the endpoint is stale: %+v", got)
	}
	c.advance(ActivityNotifyEvery)
	if !apply(tr, event("s1", "post-task", c.now, "/work/demo")) {
		t.Fatal("a long gap is a change")
	}
	c.advance(time.Second)
	if !apply(tr, event("s1", "stop", c.now, "/work/demo")) {
		t.Fatal("a state change is a change")
	}
}

func TestOversizeIdentifiersAreIgnored(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newLinker())
	if apply(tr, event(strings.Repeat("x", maxID+1), "session-start", c.now, "/work/demo")) {
		t.Fatal("a long session id was tracked")
	}
	e := event("s1", "session-start", c.now, "/work/demo")
	e.Agent = strings.Repeat("a", maxAgent+1)
	if apply(tr, e) || len(tr.Sessions()) != 0 {
		t.Fatal("a long agent was tracked")
	}
	if !apply(tr, event(strings.Repeat("x", maxID), "session-start", c.now, "/work/demo")) {
		t.Fatal("an id of the largest size is refused")
	}
}

func TestReconcileAppliesEventsTheSubscriptionDropped(t *testing.T) {
	c := &clock{t0}
	src := &source{}
	tr := &Tracker{Source: src, Linker: newLinker(), Now: c.Now}
	ch, _, dropped := src.SubscribeCounted(1, nil)
	tr.dropped = dropped
	src.add(event("s1", "session-start", c.now, "/work/demo"))
	c.advance(time.Minute)
	src.add(event("s1", "user-prompt-submit", c.now, "/work/demo"))
	c.advance(time.Minute)
	src.add(event("s1", "session-end", c.now, "/work/demo"))
	if len(ch) != 1 {
		t.Fatalf("buffered %d records, want the buffer's one", len(ch))
	}
	// The subscriber handles what it was given; the prompt and the end were dropped.
	apply(tr, (<-ch).Event)
	if got := tr.Sessions(); len(got) != 1 || got[0].State != StateRunning || got[0].LastEvent != EventStart {
		t.Fatalf("before reconciling: %+v", got)
	}
	if !tr.reconcileIfDropped(context.Background()) {
		t.Fatal("reconciling found nothing")
	}
	if got := tr.Sessions(); len(got) != 1 || got[0].State != StateEnded {
		t.Fatalf("after reconciling: %+v", got)
	}
	if tr.Reconcile(context.Background()) {
		t.Fatal("reconciling twice changed something")
	}
}

func TestSweepReadsTheRecordOnlyAfterADrop(t *testing.T) {
	c := &clock{t0}
	src := &source{}
	tr := &Tracker{Source: src, Linker: newLinker(), Now: c.Now}
	_, _, tr.dropped = src.SubscribeCounted(8, nil)
	// An event in the record the subscription delivered nothing of, but
	// nothing was dropped: the record is not read.
	src.records = append(src.records, record(event("s1", "session-start", c.now, "/work/demo")))
	if tr.reconcileIfDropped(context.Background()) || src.recents.Load() != 0 {
		t.Fatalf("reconciled without a drop (reads %d)", src.recents.Load())
	}
	src.drops.Add(1)
	if !tr.reconcileIfDropped(context.Background()) && !tr.ResolvePending(context.Background()) {
		t.Fatal("a drop did not reconcile")
	}
	if src.recents.Load() != 1 || len(tr.Sessions()) != 1 {
		t.Fatalf("reads %d, sessions %+v", src.recents.Load(), tr.Sessions())
	}
	// The drop is covered: later sweeps read nothing.
	tr.reconcileIfDropped(context.Background())
	if src.recents.Load() != 1 {
		t.Fatalf("read the record again without a new drop (%d)", src.recents.Load())
	}
}
