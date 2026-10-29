package decisionlog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/livesessions"
)

func TestDue(t *testing.T) {
	now := t0.Add(time.Hour)
	cases := []struct {
		name    string
		session livesessions.Session
		memo    memo
		want    string
	}{
		{"just entered running", livesessions.Session{State: livesessions.StateRunning, StartedAt: t0}, memo{}, ""},
		{"running long", livesessions.Session{State: livesessions.StateRunning}, memo{running: now.Add(-RunningEvery)}, "running"},
		{"running long across a brief stop", livesessions.Session{State: livesessions.StateRunning}, memo{running: now.Add(-RunningEvery), fired: t0}, "running"},
		{"running long, queued lately", livesessions.Session{State: livesessions.StateRunning}, memo{running: t0, lastRun: now.Add(-time.Minute)}, ""},
		{"quiet briefly", livesessions.Session{State: livesessions.StateIdle, WaitingReason: livesessions.WaitTurnFinished, LastActivityAt: now.Add(-time.Minute)}, memo{}, ""},
		{"settled", livesessions.Session{State: livesessions.StateIdle, WaitingReason: livesessions.WaitTurnFinished, LastActivityAt: now.Add(-SettleAfter)}, memo{}, "settled"},
		{"settled, already queued", livesessions.Session{State: livesessions.StateIdle, LastActivityAt: now.Add(-SettleAfter)}, memo{fired: now.Add(-SettleAfter)}, ""},
		{"asks permission", livesessions.Session{State: livesessions.StateIdle, WaitingReason: livesessions.WaitPermission, LastActivityAt: now.Add(-3 * time.Minute)}, memo{}, "settled"},
		{"ended just now", livesessions.Session{State: livesessions.StateEnded, LastActivityAt: now}, memo{}, "settled"},
		{"ended after settling", livesessions.Session{State: livesessions.StateEnded, LastActivityAt: now}, memo{fired: now.Add(-time.Minute)}, "settled"},
		{"ended, already queued", livesessions.Session{State: livesessions.StateEnded, LastActivityAt: now}, memo{fired: now}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := due(tc.session, tc.memo, now); got != tc.want {
				t.Fatalf("due = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDueFollowsTheTracker drives a real tracker through Claude Code's events:
// a stop, then the idle_prompt notification a minute later, a new prompt, a
// long run and the end.
func TestDueFollowsTheTracker(t *testing.T) {
	c := &clock{t0}
	tracker := &livesessions.Tracker{Source: &fakeSource{}, Linker: demoLinker{}, Now: c.Now}
	send := func(name, notification string) {
		e := hookevents.New(hookevents.KindAgent, "claude-code", name, "/work/demo", "test")
		e.Time = c.now
		e.Session = &hookevents.Session{ID: "s1", Cwd: "/work/demo", NotificationType: notification}
		tracker.Apply(context.Background(), e)
		tracker.ResolvePending(context.Background())
	}
	m := memo{}
	check := func(want string) {
		t.Helper()
		sessions := tracker.Sessions()
		if len(sessions) != 1 {
			t.Fatalf("%d sessions", len(sessions))
		}
		var got string
		got, m = due(sessions[0], m, c.now)
		if got != want {
			t.Fatalf("at %s: due = %q, want %q (%+v)", c.now.Sub(t0), got, want, sessions[0])
		}
	}
	send("session-start", "")
	send("user-prompt-submit", "")
	check("")
	c.now = c.now.Add(time.Minute)
	send("stop", "")
	check("")
	c.now = c.now.Add(time.Minute)
	send("notification", "idle_prompt")
	check("")
	c.now = c.now.Add(SettleAfter)
	check("settled")
	c.now = c.now.Add(time.Minute)
	check("")
	send("user-prompt-submit", "")
	check("")
	for range 4 {
		c.now = c.now.Add(5 * time.Minute)
		send("post-tool-use", "")
		if c.now.Sub(t0) < 25*time.Minute {
			check("")
		}
	}
	check("running")
	c.now = c.now.Add(time.Minute)
	send("session-end", "")
	check("settled")
}

func TestDueKeepsCountingAcrossBriefStops(t *testing.T) {
	m := memo{}
	start := t0
	running := livesessions.Session{State: livesessions.StateRunning, LastActivityAt: start}
	_, m = due(running, m, start)
	// A stop answered within a minute is not a settle.
	stopped := livesessions.Session{State: livesessions.StateIdle, WaitingReason: livesessions.WaitTurnFinished, LastActivityAt: start.Add(15 * time.Minute)}
	if got, next := due(stopped, m, start.Add(16*time.Minute)); got != "" || next.running.IsZero() {
		t.Fatalf("%q %+v", got, next)
	} else {
		m = next
	}
	if got, _ := due(running, m, start.Add(RunningEvery)); got != "running" {
		t.Fatalf("due = %q", got)
	}
	// A settle restarts the count.
	_, m = due(livesessions.Session{State: livesessions.StateIdle, LastActivityAt: start}, m, start.Add(time.Hour))
	if got, _ := due(running, m, start.Add(time.Hour+time.Minute)); got != "" {
		t.Fatalf("due = %q", got)
	}
}

type demoLinker struct{}

func (demoLinker) Link(context.Context, string) (livesessions.Link, bool) {
	return livesessions.Link{Repository: "o/r", Branch: "feature"}, true
}

type fakeSource struct {
	mu       sync.Mutex
	ch       chan hookevents.Record
	canceled bool
}

func (s *fakeSource) Recent(int) ([]hookevents.Record, error) { return nil, nil }

func (s *fakeSource) SubscribeCounted(buffer int, _ func(hookevents.Event) bool) (<-chan hookevents.Record, func(), func() uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ch = make(chan hookevents.Record, buffer)
	return s.ch, func() {
		s.mu.Lock()
		s.canceled = true
		s.mu.Unlock()
	}, func() uint64 { return 0 }
}

func (s *fakeSource) send(agent, id string) {
	e := hookevents.New(hookevents.KindAgent, agent, "pre-compact", "/work/demo", "test")
	e.Session = &hookevents.Session{ID: id}
	s.mu.Lock()
	ch := s.ch
	s.mu.Unlock()
	ch <- hookevents.Record{Event: e}
}

// fakeTracker lists fixed sessions.
type fakeTracker struct {
	mu       sync.Mutex
	sessions []livesessions.Session
}

func (f *fakeTracker) Sessions() []livesessions.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]livesessions.Session(nil), f.sessions...)
}

func (f *fakeTracker) Lookup(agent, id string) (livesessions.Session, string, bool) {
	for _, s := range f.Sessions() {
		if s.Agent == agent && s.ID == id {
			return s, "/path/" + id, true
		}
	}
	return livesessions.Session{}, "", false
}

// fakeWorker records captures; block, when set, holds each until ctx ends.
type fakeWorker struct {
	mu       sync.Mutex
	captured []Target
	block    bool
	started  chan struct{}
	finished chan struct{}
}

func (w *fakeWorker) Capture(ctx context.Context, t Target, _ Options) (Report, error) {
	w.mu.Lock()
	w.captured = append(w.captured, t)
	w.mu.Unlock()
	if w.block {
		close(w.started)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		close(w.finished)
	}
	return Report{Outcome: OutcomeExtracted}, nil
}

func (w *fakeWorker) targets() []Target {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Target(nil), w.captured...)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSchedulerQueuesDedupsAndBackstops(t *testing.T) {
	c := &clock{t0}
	var clockMu sync.Mutex
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return c.now
	}
	settledSession := livesessions.Session{Agent: "claude-code", ID: "quiet", State: livesessions.StateIdle, LastActivityAt: t0.Add(-time.Hour), Repository: "o/r"}
	running := livesessions.Session{Agent: "claude-code", ID: "busy", State: livesessions.StateRunning, LastActivityAt: t0, Repository: "o/r"}
	tracker := &fakeTracker{sessions: []livesessions.Session{settledSession, running}}
	source := &fakeSource{}
	worker := &fakeWorker{}
	s := &Scheduler{Tracker: tracker, Source: source, Worker: worker, Now: now, Tick: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor(t, "the settled session", func() bool { return len(worker.targets()) == 1 })
	if got := worker.targets()[0]; got.ID != "quiet" || got.Path != "/path/quiet" || got.Repository != "o/r" {
		t.Fatalf("%+v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(worker.targets()); n != 1 {
		t.Fatalf("queued again without a change: %d captures", n)
	}
	// The backstop looks at the settled session again, never the running one.
	clockMu.Lock()
	c.now = c.now.Add(BackstopEvery)
	clockMu.Unlock()
	waitFor(t, "the backstop", func() bool { return len(worker.targets()) == 2 })
	if got := worker.targets()[1]; got.ID != "quiet" {
		t.Fatalf("%+v", got)
	}
	// A compaction queues the running session at once.
	waitFor(t, "the subscription", func() bool { source.mu.Lock(); defer source.mu.Unlock(); return source.ch != nil })
	source.send("claude-code", "busy")
	waitFor(t, "the compaction", func() bool { return len(worker.targets()) == 3 })
	if got := worker.targets()[2]; got.ID != "busy" {
		t.Fatalf("%+v", got)
	}
	cancel()
	<-done
	if !source.canceled {
		t.Fatal("not unsubscribed")
	}
}

func TestSchedulerEnqueueDedups(t *testing.T) {
	s := &Scheduler{}
	s.Enqueue("a", "1")
	s.Enqueue("a", "1")
	s.Enqueue("a", "2")
	var got []key
	for {
		k, ok := s.next()
		if !ok {
			break
		}
		got = append(got, k)
	}
	if len(got) != 2 || got[0].id != "1" || got[1].id != "2" {
		t.Fatalf("%v", got)
	}
}

func TestSchedulerRunWaitsForTheCapture(t *testing.T) {
	tracker := &fakeTracker{sessions: []livesessions.Session{{Agent: "claude-code", ID: "s1", State: livesessions.StateEnded, LastActivityAt: t0}}}
	worker := &fakeWorker{block: true, started: make(chan struct{}), finished: make(chan struct{})}
	s := &Scheduler{Tracker: tracker, Worker: worker, Now: (&clock{t0}).Now, Tick: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	<-worker.started
	cancel()
	<-done
	select {
	case <-worker.finished:
	default:
		t.Fatal("Run returned before the capture in progress")
	}
}

func TestThrottled(t *testing.T) {
	now := t0
	cases := []struct {
		name  string
		stats Stats
		ended bool
		want  string
	}{
		{"never", Stats{}, false, ""},
		{"recent attempt", Stats{Count: 1, LastStart: now.Add(-time.Minute)}, false, "attempted_recently"},
		{"older attempt", Stats{Count: 1, LastStart: now.Add(-MinInterval)}, false, ""},
		{"recent failure", Stats{Count: 1, LastStart: now.Add(-6 * time.Minute), LastFail: now.Add(-6 * time.Minute)}, false, "failed_recently"},
		{"older failure", Stats{Count: 1, LastStart: now.Add(-FailureDelay), LastFail: now.Add(-FailureDelay)}, false, ""},
		{"daily limit", Stats{Count: MaxPerDay, LastStart: now.Add(-time.Hour)}, false, "daily_attempts"},
		{"daily limit, ended", Stats{Count: MaxPerDay, LastStart: now.Add(-time.Hour)}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := throttled(tc.stats, tc.ended, now); got != tc.want {
				t.Fatalf("throttled = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCovered(t *testing.T) {
	day := startOfDay(t0)
	cases := []struct {
		row  Session
		want bool
	}{
		{Session{Fingerprint: "f", Coverage: CoverageExtracted}, true},
		{Session{Fingerprint: "g", Coverage: CoverageExtracted}, false},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonOverTokenCap, UpdatedAt: day.Add(-time.Hour)}, true},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonUnavailable, UpdatedAt: day.Add(time.Hour)}, true},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonUnavailable, UpdatedAt: day.Add(-time.Hour)}, false},
	}
	for i, tc := range cases {
		if got := covered(tc.row, "f", day); got != tc.want {
			t.Errorf("%d: covered = %v", i, got)
		}
	}
}
