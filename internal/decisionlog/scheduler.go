package decisionlog

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/livesessions"
)

// Trigger timings (README.md, "When a session is captured").
const (
	// SettleAfter is how long a session that is not running must have been
	// quiet: its turn ended, it waits on its user, or it went silent.
	SettleAfter = 2 * time.Minute
	// RunningEvery captures a session that keeps running.
	RunningEvery = 20 * time.Minute
	// BackstopEvery looks again at every settled session.
	BackstopEvery = 5 * time.Minute
	// TickEvery is how often the triggers are evaluated.
	TickEvery = 30 * time.Second
	// PruneEvery is how often the store is pruned.
	PruneEvery = 24 * time.Hour
)

// Tracker is what the scheduler reads of the live sessions;
// *livesessions.Tracker is one.
type Tracker interface {
	Sessions() []livesessions.Session
	Lookup(agent, id string) (livesessions.Session, string, bool)
}

// Source delivers hook events; *hookevents.Store is one.
type Source interface {
	SubscribeCounted(buffer int, match func(hookevents.Event) bool) (events <-chan hookevents.Record, cancel func(), dropped func() uint64)
}

// Worker captures one session and prunes the store; *Capturer is one.
type Worker interface {
	Capture(context.Context, Target, Options) (Report, error)
	Prune(context.Context) error
}

// memo is what the scheduler remembers of a session between ticks.
type memo struct {
	// fired is the LastActivityAt a settled session was queued for.
	fired time.Time
	// running is when the session was first seen running after it last
	// settled, zero until then: a brief stop between prompts leaves it as it
	// is. lastRun is when it was last queued for running long.
	running, lastRun time.Time
}

// settled reports whether the session is not running and has been quiet for
// SettleAfter, or ended.
func settled(s livesessions.Session, now time.Time) bool {
	return s.State == livesessions.StateEnded || (s.State != livesessions.StateRunning && now.Sub(s.LastActivityAt) >= SettleAfter)
}

// due reports why the session is to be captured now, empty when it is not:
// it settled since it was last queued, or it has been running RunningEvery
// since it started running after it last settled, or since it was last
// queued for that. A brief wait between prompts does not restart the count.
func due(s livesessions.Session, m memo, now time.Time) (string, memo) {
	if s.State != livesessions.StateRunning {
		if settled(s, now) {
			m.running = time.Time{}
			if !s.LastActivityAt.Equal(m.fired) {
				m.fired = s.LastActivityAt
				return "settled", m
			}
		}
		return "", m
	}
	if m.running.IsZero() {
		m.running = now
	}
	since := m.running
	if m.lastRun.After(since) {
		since = m.lastRun
	}
	if now.Sub(since) >= RunningEvery {
		m.lastRun = now
		return "running", m
	}
	return "", m
}

// Scheduler decides when the tracked sessions are captured and hands them,
// one at a time, to the worker. Run it once in the daemon.
type Scheduler struct {
	Tracker Tracker
	Source  Source
	Worker  Worker
	Now     func() time.Time
	// Tick is how often the triggers are evaluated; zero is TickEvery.
	Tick time.Duration
	// Changed is called after a capture saved a session's decisions.
	Changed func()
	Log     *slog.Logger

	once   sync.Once
	mu     sync.Mutex
	queued map[key]bool
	order  []key
	wake   chan struct{}
}

type key struct{ agent, id string }

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) init() {
	s.once.Do(func() { s.queued, s.wake = map[key]bool{}, make(chan struct{}, 1) })
}

// Enqueue asks for the session to be captured; a session already waiting is
// not queued twice.
func (s *Scheduler) Enqueue(agent, id string) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{agent, id}
	if s.queued[k] {
		return
	}
	s.queued[k] = true
	s.order = append(s.order, k)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) next() (key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return key{}, false
	}
	k := s.order[0]
	s.order = s.order[1:]
	delete(s.queued, k)
	return k, true
}

// tick evaluates the triggers; backstop also queues every settled session,
// which the worker leaves alone when its transcript did not change.
func (s *Scheduler) tick(memos map[key]memo, backstop bool) {
	now := s.now()
	seen := map[key]bool{}
	for _, session := range s.Tracker.Sessions() {
		k := key{session.Agent, session.ID}
		seen[k] = true
		reason, m := due(session, memos[k], now)
		memos[k] = m
		if reason != "" || (backstop && settled(session, now)) {
			s.Enqueue(k.agent, k.id)
		}
	}
	for k := range memos {
		if !seen[k] {
			delete(memos, k)
		}
	}
}

// Run evaluates the triggers every Tick, queues a session that is about to
// compact at once, and captures the queued sessions one at a time, until ctx
// ends. It returns once the capture in progress has.
func (s *Scheduler) Run(ctx context.Context) {
	s.init()
	var compacting <-chan hookevents.Record
	if s.Source != nil {
		events, cancel, _ := s.Source.SubscribeCounted(16, func(e hookevents.Event) bool {
			return e.Kind == hookevents.KindAgent && e.Name == "pre-compact" && e.Session != nil && e.Session.ID != ""
		})
		defer cancel()
		compacting = events
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { s.work(ctx) })
	s.prune(ctx)
	memos := map[key]memo{}
	tick := s.Tick
	if tick <= 0 {
		tick = TickEvery
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	lastBackstop, lastPrune := s.now(), s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-compacting:
			if !ok {
				compacting = nil
				continue
			}
			s.Enqueue(record.Event.Agent, record.Event.Session.ID)
		case <-ticker.C:
			now := s.now()
			backstop := now.Sub(lastBackstop) >= BackstopEvery
			if backstop {
				lastBackstop = now
			}
			s.tick(memos, backstop)
			if now.Sub(lastPrune) >= PruneEvery {
				lastPrune = now
				s.prune(ctx)
			}
		}
	}
}

func (s *Scheduler) prune(ctx context.Context) {
	if err := s.Worker.Prune(ctx); err != nil && ctx.Err() == nil {
		s.logger().Warn("decision store not pruned", "error", err)
	}
}

// work captures the queued sessions one at a time.
func (s *Scheduler) work(ctx context.Context) {
	for {
		k, ok := s.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		session, path, tracked := s.Tracker.Lookup(k.agent, k.id)
		if !tracked {
			continue
		}
		report, err := s.Worker.Capture(ctx, Target{Agent: k.agent, ID: k.id, Path: path, Repository: session.Repository,
			Branch: session.Branch, Ended: session.State == livesessions.StateEnded}, Options{})
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			s.logger().Warn("session decisions not captured", "agent", k.agent, "outcome", report.Outcome, "reason", report.Reason, "error", err)
		case report.Outcome == OutcomeExtracted || report.Outcome == OutcomeSkipped:
			s.logger().Info("session decisions captured", "agent", k.agent, "outcome", report.Outcome, "reason", report.Reason,
				"mode", report.Mode, "turns", report.Turns, "decisions", report.Decisions)
			if report.Outcome == OutcomeExtracted && s.Changed != nil {
				s.Changed()
			}
		}
	}
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
