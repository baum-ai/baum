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
// unchanged and remains the full history; Transcripts serves a tracked
// session's transcript, redacted, while it runs.
package livesessions

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
// DropAfter or ended for EndedFor, which is long enough for the Sessions page
// to show what ended recently; at most MaxSessions are tracked, the least
// recently active (an ended one first) making room for a new one.
const (
	IdleAfter   = 10 * time.Minute
	DropAfter   = 2 * time.Hour
	EndedFor    = 24 * time.Hour
	MaxSessions = 200
	// SweepEvery is how often Run applies the timings, which events do not
	// trigger.
	SweepEvery = 30 * time.Second
	// RelinkEvery is the least time between two lookups of a session's
	// repository, branch and pull request: the branch changes while a
	// session runs, and a lookup runs git.
	RelinkEvery = 30 * time.Second
	// NotifyEvery coalesces the notifications of a busy session.
	NotifyEvery = time.Second
	// ActivityNotifyEvery is how far last_activity_at must move, with nothing
	// else changed, for the app to be told: a busy agent's tool events would
	// otherwise make every open window read the list once a second.
	ActivityNotifyEvery = 30 * time.Second
	// Bounds on the identifiers a session is keyed by; an event with longer
	// ones is not a session baum's hooks describe.
	maxID    = 128
	maxAgent = 32
	// StateFile is the name of the file in StateDir holding the ended
	// sessions, since the hook event record rotates by size and may not reach
	// back EndedFor.
	StateFile = "live-sessions.json"
	// rememberedEvents is how many applied event ids are kept, so applying
	// an event twice (the subscription and a reconcile) changes nothing.
	rememberedEvents = 4096
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
	LastEvent string `json:"last_event"`
	// EndedAt is when an ended session sent its end event, absent otherwise.
	EndedAt    time.Time `json:"ended_at,omitzero"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch,omitempty"`
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
	SubscribeCounted(buffer int, match func(hookevents.Event) bool) (events <-chan hookevents.Record, cancel func(), dropped func() uint64)
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
	// StateDir, when set, is a private directory where the ended sessions
	// are kept across restarts (StateFile): identifiers and times only.
	StateDir string
	// Buffer is the subscription's size; zero is 256. A subscriber that falls
	// behind misses events, which Reconcile applies from the record once the
	// subscription reports it missed some.
	Buffer int

	ready    sync.Once
	mu       sync.Mutex
	sessions map[key]*entry
	// pending are the sessions waiting for a link lookup, which runs off the
	// event loop: it runs git, and the loop must keep up with the hooks.
	pending map[key]bool
	seen    map[string]bool
	order   []string
	wake    chan struct{}
	kick    chan struct{}
	// dropped reports the subscription's missed events; reconciled is how
	// many of them the last reconcile covered.
	dropped    func() uint64
	reconciled uint64
	// saved is the ended sessions as last written.
	saved []byte
}

type key struct{ agent, id string }

// entry is a tracked session. cwd and transcript stay here: they are never
// served.
type entry struct {
	Session
	cwd string
	// transcript is the transcript file the session's hooks last named, empty
	// when none did. Transcripts reads it only after checking it is inside the
	// agent's own session store.
	transcript string
	// waiting is set by a stop, until the next event of work.
	waiting  bool
	ended    bool
	endedAt  time.Time
	linkedAt time.Time
	// stale is set when an event arrived within RelinkEvery of the last lookup
	// and so skipped one: the link may be out of date until Sweep looks again.
	stale bool
	// unlinked is a session whose link is yet to be looked up: it is not
	// shown, and is dropped when its directory is in no registered clone.
	unlinked bool
}

func (t *Tracker) init() {
	t.ready.Do(func() {
		t.sessions, t.pending, t.seen = map[key]*entry{}, map[key]bool{}, map[string]bool{}
		t.wake, t.kick = make(chan struct{}, 1), make(chan struct{}, 1)
	})
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
	t.init()
	buffer := t.Buffer
	if buffer <= 0 {
		buffer = 256
	}
	events, cancel, dropped := t.Source.SubscribeCounted(buffer, matchAgent)
	defer cancel()
	t.dropped = dropped
	t.Rebuild(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.wake:
				if t.ResolvePending(ctx) {
					t.signal()
				}
			}
		}
	}()
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
			t.persist()
			return
		case record, ok := <-events:
			if !ok {
				return
			}
			if t.Apply(ctx, record.Event) {
				changed()
			}
		case <-t.kick:
			changed()
		case <-sweep.C:
			swept, reconciled := t.Sweep(), t.reconcileIfDropped(ctx)
			t.persist()
			if swept || reconciled {
				changed()
			}
		case <-flush:
			flush = nil
			t.notify()
		}
	}
}

func (t *Tracker) signal() {
	select {
	case t.kick <- struct{}{}:
	default:
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

// events are the record's agent events of the sessions that can still be
// tracked, in the order of their time (a spooled event is recorded after later
// ones): those within DropAfter, and all of a session that ended within
// EndedFor.
func (t *Tracker) events() ([]hookevents.Event, error) {
	records, err := t.Source.Recent(0)
	if err != nil {
		return nil, err
	}
	now := t.now()
	ended := map[key]bool{}
	for _, record := range records {
		e := record.Event
		if kind, _ := normalise(e.Name); matchAgent(e) && kind == EventEnd && e.Session != nil && now.Sub(e.Time) <= EndedFor {
			ended[key{e.Agent, e.Session.ID}] = true
		}
	}
	events := make([]hookevents.Event, 0, len(records))
	for _, record := range records {
		e := record.Event
		if !matchAgent(e) {
			continue
		}
		if now.Sub(e.Time) <= DropAfter || (e.Session != nil && ended[key{e.Agent, e.Session.ID}] && now.Sub(e.Time) <= EndedFor) {
			events = append(events, e)
		}
	}
	slices.SortStableFunc(events, func(a, b hookevents.Event) int { return a.Time.Compare(b.Time) })
	return events, nil
}

// Rebuild replaces the sessions with those the record's events show, so a
// daemon that restarts keeps showing the sessions that were running. Their
// links are looked up afterwards, off the caller's goroutine when Run is the
// caller; ResolvePending does it for a caller without one.
func (t *Tracker) Rebuild(ctx context.Context) {
	t.init()
	events, err := t.events()
	if err != nil {
		t.logger().Warn("live sessions not rebuilt from the hook event record", "error", err)
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions, t.pending, t.seen, t.order = map[key]*entry{}, map[key]bool{}, map[string]bool{}, nil
	t.loadEndedLocked(now)
	for _, e := range events {
		t.applyLocked(e, now)
	}
}

// reconcileIfDropped reconciles when the subscription missed events since
// the last reconcile: rereading the record takes the store's lock, which
// every hook's event also needs, so it does not run for nothing.
func (t *Tracker) reconcileIfDropped(ctx context.Context) bool {
	if t.dropped == nil {
		return false
	}
	missed := t.dropped()
	if missed == t.reconciled {
		return false
	}
	t.reconciled = missed
	return t.Reconcile(ctx)
}

// Reconcile applies the events of the record that the subscription did not
// deliver (a full buffer drops them) and reports whether what the application
// sees changed. Applying an event twice changes nothing, so it can run at any
// time.
func (t *Tracker) Reconcile(ctx context.Context) bool {
	t.init()
	events, err := t.events()
	if err != nil {
		t.logger().Warn("live sessions not reconciled with the hook event record", "error", err)
		return false
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	for _, e := range events {
		if t.applyLocked(e, now) {
			changed = true
		}
	}
	return changed
}

func (e *entry) setLink(link Link, now time.Time) {
	e.Repository, e.Branch, e.PR = link.Repository, link.Branch, link.PR
	e.linkedAt = now
	e.stale = false
}

func cwdOf(e hookevents.Event) string {
	if e.Session != nil && e.Session.Cwd != "" {
		return e.Session.Cwd
	}
	return e.Dir
}

// Apply updates the sessions with a hook event and reports whether what the
// application sees changed enough to tell it. It never waits for git: a new
// session is hidden until ResolvePending links it.
func (t *Tracker) Apply(_ context.Context, e hookevents.Event) bool {
	t.init()
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.applyLocked(e, now)
}

// ResolvePending looks up the link of each session waiting for one and
// reports whether what the application sees changed. Run does it in the
// background after every event that needs it.
func (t *Tracker) ResolvePending(ctx context.Context) bool {
	t.init()
	type request struct {
		k   key
		s   *entry
		cwd string
	}
	t.mu.Lock()
	var todo []request
	for k := range t.pending {
		if s := t.sessions[k]; s != nil {
			todo = append(todo, request{k, s, s.cwd})
		} else {
			delete(t.pending, k)
		}
	}
	t.mu.Unlock()
	changed := false
	for _, r := range todo {
		link, ok := t.Linker.Link(ctx, r.cwd)
		now := t.now()
		t.mu.Lock()
		delete(t.pending, r.k)
		if t.sessions[r.k] == r.s {
			before, existed := t.view(r.k, now)
			switch {
			case ok:
				r.s.setLink(link, now)
				r.s.unlinked = false
			case r.s.unlinked:
				delete(t.sessions, r.k)
			default:
				// A directory that no longer resolves keeps the last link.
				r.s.linkedAt = now
				r.s.stale = false
			}
			after, exists := t.view(r.k, now)
			if significant(before, after, existed, exists) {
				changed = true
			}
		}
		t.mu.Unlock()
	}
	return changed
}

// queue asks for a link lookup of the session.
func (t *Tracker) queue(k key) {
	t.pending[k] = true
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// view is the session as the application sees it; a session not yet linked
// is not seen.
func (t *Tracker) view(k key, now time.Time) (Session, bool) {
	s := t.sessions[k]
	if s == nil || s.unlinked {
		return Session{}, false
	}
	out := s.Session
	out.State = s.state(now)
	return out, true
}

// significant reports whether the application should be told of the change
// from before to after: not one that only moves last_activity_at by less than
// ActivityNotifyEvery. The endpoint always serves the fresh values.
func significant(before, after Session, existed, exists bool) bool {
	if existed != exists {
		return true
	}
	if !exists {
		return false
	}
	if after.LastActivityAt.Sub(before.LastActivityAt) >= ActivityNotifyEvery {
		return true
	}
	before.LastActivityAt, before.LastEvent = after.LastActivityAt, after.LastEvent
	return before != after
}

// applyLocked applies e once, and reports whether the change is worth a
// notification.
func (t *Tracker) applyLocked(e hookevents.Event, now time.Time) bool {
	if !matchAgent(e) || e.Session == nil || e.Session.ID == "" || e.Agent == "" || e.Time.IsZero() {
		return false
	}
	if e.ID != "" {
		if t.seen[e.ID] {
			return false
		}
		t.seen[e.ID] = true
		t.order = append(t.order, e.ID)
		if len(t.order) > rememberedEvents {
			delete(t.seen, t.order[0])
			t.order = t.order[1:]
		}
	}
	k := key{e.Agent, e.Session.ID}
	before, existed := t.view(k, now)
	t.applyEvent(e, now)
	after, exists := t.view(k, now)
	return significant(before, after, existed, exists)
}

// applyEvent is the state machine.
func (t *Tracker) applyEvent(e hookevents.Event, now time.Time) {
	if len(e.Session.ID) > maxID || len(e.Agent) > maxAgent {
		return
	}
	kind, _ := normalise(e.Name)
	at := e.Time.UTC()
	if at.After(now) {
		at = now
	}
	if now.Sub(at) > EndedFor {
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
		delete(t.pending, k)
		s = nil
	}
	cwd := cwdOf(e)
	if s == nil {
		if kind == EventEnd || cwd == "" {
			return
		}
		t.makeRoom()
		s = &entry{Session: Session{Agent: e.Agent, ID: e.Session.ID, StartedAt: at, LastActivityAt: at}, cwd: cwd, unlinked: true}
		t.sessions[k] = s
		t.queue(k)
	} else if cwd != "" && !s.unlinked && !t.pending[k] {
		if now.Sub(s.linkedAt) >= RelinkEvery {
			t.queue(k)
		} else {
			// Skipped by the throttle: Sweep looks again after the window.
			s.stale = true
		}
	}
	if cwd != "" {
		s.cwd = cwd
	}
	if e.Session.TranscriptPath != "" {
		s.transcript = e.Session.TranscriptPath
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
		s.ended, s.endedAt, s.EndedAt = true, at, at
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
	delete(t.pending, key{victim.Agent, victim.ID})
}

// Sweep drops the sessions that expired and reports whether what the
// application sees changed, by that or by a running session going idle.
func (t *Tracker) Sweep() bool {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	changed := false
	for k, s := range t.sessions {
		switch {
		case s.expired(now):
			delete(t.sessions, k)
			delete(t.pending, k)
			changed = changed || !s.unlinked
		case s.State != s.state(now):
			s.State = s.state(now)
			changed = changed || !s.unlinked
		}
		// A throttled lookup is retried once the window has passed, and a
		// session with no pull request is looked at again, as discovery may
		// have learned of one since.
		if t.sessions[k] == s && !s.ended && !s.unlinked && !t.pending[k] && s.cwd != "" &&
			now.Sub(s.linkedAt) >= RelinkEvery && (s.stale || s.PR == 0) {
			t.queue(k)
		}
	}
	return changed
}

// Sessions returns the tracked sessions, the most recently active first.
func (t *Tracker) Sessions() []Session {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make([]Session, 0, len(t.sessions))
	for k, s := range t.sessions {
		if s.expired(now) {
			continue
		}
		if session, ok := t.view(k, now); ok {
			out = append(out, session)
		}
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := b.LastActivityAt.Compare(a.LastActivityAt); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Lookup is a session the application is shown, with the transcript path its
// hooks last reported (empty when none did, or for an ended session read back
// from StateFile, which keeps no paths); false when the tracker does not list
// it.
func (t *Tracker) Lookup(agent, id string) (Session, string, bool) {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	k := key{agent, id}
	s := t.sessions[k]
	if s == nil || s.expired(now) {
		return Session{}, "", false
	}
	session, ok := t.view(k, now)
	if !ok {
		return Session{}, "", false
	}
	return session, s.transcript, true
}

// persisted is the ended sessions' file: identifiers and times, no paths.
type persisted struct {
	Sessions []Session `json:"sessions"`
}

// persist writes the ended sessions to StateDir when they changed since the
// last write, so a restart keeps them even once the record has rotated past
// them. A failure is logged and retried at the next sweep.
func (t *Tracker) persist() {
	if t.StateDir == "" {
		return
	}
	t.init()
	t.mu.Lock()
	now := t.now()
	var ended []Session
	for _, s := range t.sessions {
		if s.ended && !s.unlinked && !s.expired(now) {
			ended = append(ended, s.Session)
		}
	}
	t.mu.Unlock()
	slices.SortFunc(ended, func(a, b Session) int {
		return cmp.Or(a.EndedAt.Compare(b.EndedAt), cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.ID, b.ID))
	})
	data, err := json.Marshal(persisted{ended})
	if err != nil || bytes.Equal(data, t.saved) {
		return
	}
	if err := writeAtomic(filepath.Join(t.StateDir, StateFile), data); err != nil {
		t.logger().Warn("ended live sessions not saved", "error", err)
		return
	}
	t.saved = data
}

// loadEndedLocked adds the ended sessions of the last run that have not
// expired; the record's events then apply over them.
func (t *Tracker) loadEndedLocked(now time.Time) {
	if t.StateDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(t.StateDir, StateFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.logger().Warn("ended live sessions not read", "error", err)
		}
		return
	}
	var saved persisted
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	for _, s := range saved.Sessions {
		if s.EndedAt.IsZero() || s.Repository == "" || s.ID == "" || len(s.ID) > maxID || len(s.Agent) > maxAgent || now.Sub(s.EndedAt) > EndedFor {
			continue
		}
		s.State = StateEnded
		t.sessions[key{s.Agent, s.ID}] = &entry{Session: s, ended: true, endedAt: s.EndedAt, linkedAt: now}
	}
}

// writeAtomic replaces path with data through a private temporary file.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".live-sessions-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
