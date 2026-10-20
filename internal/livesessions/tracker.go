// Package livesessions tracks the coding-agent sessions running on this
// machine from the hook events the daemon records, and serves them to the
// local application so a pull request page and the inbox can show work in
// progress before any session file is complete.
//
// It reads identifiers only. A hook event never carries prompts or tool
// input, and the tracker keeps from it the agent, the session id, when the
// session started and was last active, a normalised event kind, and the
// repository, branch and pull request the session's working directory links
// to. The working directory itself and the transcript path stay inside the
// tracker and are never served. Reading session files from disk is
// unchanged and remains the full history.
package livesessions

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// States of a tracked session.
const (
	// StateRunning is a session whose agent is working: its last event was
	// a start, a prompt or tool activity, and not longer ago than IdleAfter.
	StateRunning = "running"
	// StateIdle is a session waiting for its user (its last event was a
	// stop, which agents send at the end of every turn) or silent for
	// IdleAfter: an agent that crashed sends nothing.
	StateIdle = "idle"
	// StateEnded is a session that sent its end event.
	StateEnded = "ended"
)

// Normalised kinds of the last event, the same for every agent.
const (
	EventStart  = "start"
	EventPrompt = "prompt"
	EventTool   = "tool"
	EventStop   = "stop"
	EventEnd    = "end"
)

// Timings and bounds. A session is dropped when it has been silent for
// DropAfter or ended for EndedFor; at most MaxSessions are tracked, the
// least recently active (an ended one first) making room for a new one.
const (
	IdleAfter   = 10 * time.Minute
	DropAfter   = 2 * time.Hour
	EndedFor    = 10 * time.Minute
	MaxSessions = 32
	// SweepEvery is how often Run applies the timings, which events do not
	// trigger.
	SweepEvery = 30 * time.Second
	// RelinkEvery is the least time between two lookups of a session's
	// repository, branch and pull request: the branch changes while a
	// session runs, and a lookup runs git.
	RelinkEvery = 30 * time.Second
	// NotifyEvery coalesces the notifications of a busy session.
	NotifyEvery = time.Second
)

// Session is what the application is told of a tracked session.
type Session struct {
	Agent string `json:"agent"`
	ID    string `json:"session_id"`
	State string `json:"state"`
	// StartedAt is the session's start event, or its first event the
	// daemon saw when it did not see the start.
	StartedAt      time.Time `json:"started_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	// LastEvent is the normalised kind of the latest event (EventStart,
	// EventPrompt, EventTool, EventStop or EventEnd).
	LastEvent  string `json:"last_event"`
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	// PR is the pull request whose head is Branch, 0 when there is none
	// (yet) or it cannot be told.
	PR int `json:"pr,omitempty"`
}

// Link is where a working directory belongs.
type Link struct {
	// Repository is the registered clone's repository, "owner/name" in
	// lower case.
	Repository string
	// Branch is the branch checked out, empty when detached.
	Branch string
	// PR is the pull request of Branch, 0 when none is known.
	PR int
}

// Linker links a hook's working directory to a repository, branch and pull
// request, and reports false for a directory in no registered clone.
type Linker interface {
	Link(ctx context.Context, cwd string) (Link, bool)
}

// Source is the daemon's record of hook events; *hookevents.Store is one.
type Source interface {
	Recent(limit int) ([]hookevents.Record, error)
	Subscribe(buffer int, match func(hookevents.Event) bool) (<-chan hookevents.Record, func())
}

// Tracker follows the sessions of the agents whose hooks report to the daemon.
type Tracker struct {
	Source Source
	Linker Linker
	// Notify is called, coalesced to at most one call per NotifyEvery, after
	// the sessions changed. It must not block.
	Notify func()
	Log    *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu       sync.Mutex
	sessions map[key]*entry
}

type key struct{ agent, id string }

// entry is a tracked session. cwd stays here: it is never served.
type entry struct {
	Session
	cwd string
	// waiting is set by a stop, until the next event of work.
	waiting  bool
	ended    bool
	endedAt  time.Time
	linkedAt time.Time
	// unlinked is a session recreated by rebuild whose link is yet to be
	// looked up.
	unlinked bool
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now().UTC()
	}
	return time.Now().UTC()
}

// normalise maps an agent's hook event name (Claude Code, Codex and Cursor
// share the names baum installs) to an event kind. Events outside it do not
// say anything about a session's state and are ignored.
func normalise(name string) (string, bool) {
	switch name {
	case "session-start":
		return EventStart, true
	case "user-prompt-submit", "before-submit-prompt":
		return EventPrompt, true
	case "pre-task", "post-task", "post-todo", "post-tool-use", "pre-compact", "subagent-start", "subagent-stop":
		return EventTool, true
	case "stop":
		return EventStop, true
	case "session-end":
		return EventEnd, true
	}
	return "", false
}

// state is the session's state at now.
func (e *entry) state(now time.Time) string {
	switch {
	case e.ended:
		return StateEnded
	case e.waiting || now.Sub(e.LastActivityAt) > IdleAfter:
		return StateIdle
	}
	return StateRunning
}

// expired reports whether the session is dropped at now.
func (e *entry) expired(now time.Time) bool {
	if e.ended {
		return now.Sub(e.endedAt) > EndedFor
	}
	return now.Sub(e.LastActivityAt) > DropAfter
}

func matchAgent(e hookevents.Event) bool {
	_, ok := normalise(e.Name)
	return e.Kind == hookevents.KindAgent && ok
}

// Run rebuilds the sessions from the record, then follows new events until
// ctx ends.
func (t *Tracker) Run(ctx context.Context) {
	events, cancel := t.Source.Subscribe(256, matchAgent)
	defer cancel()
	t.Rebuild(ctx)
	t.notify()
	var flush <-chan time.Time
	changed := func() {
		if flush == nil {
			flush = time.After(NotifyEvery)
		}
	}
	sweep := time.NewTicker(SweepEvery)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-events:
			if !ok {
				return
			}
			if t.Apply(ctx, record.Event) {
				changed()
			}
		case <-sweep.C:
			if t.Sweep() {
				changed()
			}
		case <-flush:
			flush = nil
			t.notify()
		}
	}
}

func (t *Tracker) notify() {
	if t.Notify != nil {
		t.Notify()
	}
}

func (t *Tracker) logger() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// Rebuild replaces the sessions with those the record's events within
// DropAfter show: a daemon that restarts keeps showing the sessions that
// were running. Events are applied in the order of their time, since a
// spooled event is recorded after later ones. Run is the only caller, so no
// event is applied while the directories are looked up.
func (t *Tracker) Rebuild(ctx context.Context) {
	records, err := t.Source.Recent(0)
	if err != nil {
		t.logger().Warn("live sessions not rebuilt from the hook event record", "error", err)
		return
	}
	events := make([]hookevents.Event, 0, len(records))
	for _, record := range records {
		if matchAgent(record.Event) {
			events = append(events, record.Event)
		}
	}
	slices.SortStableFunc(events, func(a, b hookevents.Event) int { return a.Time.Compare(b.Time) })
	now := t.now()
	t.mu.Lock()
	t.sessions = map[key]*entry{}
	for _, e := range events {
		t.applyEvent(e, now, Link{}, false, false)
	}
	pending := map[key]string{}
	for k, s := range t.sessions {
		pending[k] = s.cwd
	}
	t.mu.Unlock()
	links := map[key]Link{}
	for k, cwd := range pending {
		if link, ok := t.Linker.Link(ctx, cwd); ok {
			links[k] = link
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, s := range t.sessions {
		link, ok := links[k]
		if !ok {
			delete(t.sessions, k)
			continue
		}
		s.setLink(link, now)
		s.unlinked = false
	}
}

func (e *entry) setLink(link Link, now time.Time) {
	e.Repository, e.Branch, e.PR = link.Repository, link.Branch, link.PR
	e.linkedAt = now
}

func cwdOf(e hookevents.Event) string {
	if e.Session != nil && e.Session.Cwd != "" {
		return e.Session.Cwd
	}
	return e.Dir
}

// Apply updates the sessions with a hook event and reports whether what the
// application sees changed. Events of a directory in no registered clone, and
// ones that carry no session id, change nothing.
func (t *Tracker) Apply(ctx context.Context, e hookevents.Event) bool {
	if !matchAgent(e) || e.Session == nil || e.Session.ID == "" {
		return false
	}
	now := t.now()
	k := key{e.Agent, e.Session.ID}
	// Linking runs git, so it runs without the lock, and only for a new
	// session or one not looked up for RelinkEvery.
	var link Link
	var linked bool
	t.mu.Lock()
	s := t.sessions[k]
	need := s == nil || s.ended || now.Sub(s.linkedAt) >= RelinkEvery
	t.mu.Unlock()
	if cwd := cwdOf(e); need && cwd != "" {
		link, linked = t.Linker.Link(ctx, cwd)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[key]*entry{}
	}
	before, existed := t.view(k, now)
	t.applyEvent(e, now, link, linked, need)
	after, exists := t.view(k, now)
	return existed != exists || before != after
}

// view is the session as the application sees it.
func (t *Tracker) view(k key, now time.Time) (Session, bool) {
	s := t.sessions[k]
	if s == nil {
		return Session{}, false
	}
	out := s.Session
	out.State = s.state(now)
	return out, true
}

// applyEvent is the state machine. lookedUp says the caller resolved e's
// directory and linked is its result; Rebuild passes false and links later.
func (t *Tracker) applyEvent(e hookevents.Event, now time.Time, link Link, linked, lookedUp bool) {
	if !matchAgent(e) || e.Session == nil || e.Session.ID == "" || e.Agent == "" || e.Time.IsZero() {
		return
	}
	kind, _ := normalise(e.Name)
	at := e.Time.UTC()
	if at.After(now) {
		at = now
	}
	if now.Sub(at) > DropAfter {
		return
	}
	k := key{e.Agent, e.Session.ID}
	s := t.sessions[k]
	defer func() {
		if s != nil {
			s.State = s.state(now)
		}
	}()
	if s != nil && s.ended {
		if !at.After(s.endedAt) {
			return
		}
		// An ended session that sends events again (a resumed one) is a
		// new run of it.
		delete(t.sessions, k)
		s = nil
	}
	if s == nil {
		cwd := cwdOf(e)
		if kind == EventEnd || cwd == "" || (lookedUp && !linked) {
			return
		}
		t.makeRoom()
		s = &entry{Session: Session{Agent: e.Agent, ID: e.Session.ID, StartedAt: at, LastActivityAt: at}, cwd: cwd, unlinked: !lookedUp}
		t.sessions[k] = s
	}
	if lookedUp && linked {
		s.setLink(link, now)
	}
	if cwd := cwdOf(e); cwd != "" {
		s.cwd = cwd
	}
	if kind == EventStart && at.Before(s.StartedAt) {
		s.StartedAt = at
	}
	if at.Before(s.LastActivityAt) {
		// A late delivery of an older event tells nothing newer.
		return
	}
	s.LastActivityAt, s.LastEvent = at, kind
	switch kind {
	case EventStop:
		s.waiting = true
	case EventEnd:
		s.ended, s.endedAt = true, at
	default:
		s.waiting = false
	}
}

// makeRoom evicts one session when the bound is reached: an ended one, else
// the least recently active.
func (t *Tracker) makeRoom() {
	if len(t.sessions) < MaxSessions {
		return
	}
	var victim *entry
	for _, s := range t.sessions {
		if victim == nil || (s.ended && !victim.ended) || (s.ended == victim.ended && s.LastActivityAt.Before(victim.LastActivityAt)) {
			victim = s
		}
	}
	delete(t.sessions, key{victim.Agent, victim.ID})
}

// Sweep drops the sessions that expired and reports whether what the
// application sees changed, by that or by a running session going idle.
func (t *Tracker) Sweep() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	changed := false
	for k, s := range t.sessions {
		switch {
		case s.expired(now):
			delete(t.sessions, k)
			changed = true
		case s.State != s.state(now):
			s.State = s.state(now)
			changed = true
		}
	}
	return changed
}

// Sessions returns the tracked sessions, the most recently active first.
func (t *Tracker) Sessions() []Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make([]Session, 0, len(t.sessions))
	for k, s := range t.sessions {
		if s.expired(now) || s.unlinked {
			continue
		}
		session, _ := t.view(k, now)
		out = append(out, session)
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := b.LastActivityAt.Compare(a.LastActivityAt); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.ID, b.ID))
	})
	return out
}
