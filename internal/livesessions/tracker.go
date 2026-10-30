// Package livesessions tracks the coding-agent sessions running on this
// machine from the hook events the daemon records, and serves them to the
// local application so a pull request page and the inbox can show work in
// progress before any session file is complete.
//
// It reads identifiers only, and one line of text. A hook event never carries
// prompts or tool input, and the tracker keeps from it the agent, the session
// id, when the session started and was last active, a normalised event kind,
// why and since when it waits on its user, with the short message a Claude
// Code notification showed (masked like a transcript), and the repository,
// branch and pull request the session's working directory links to. The
// working directory itself, the transcript path and the terminal the hooks
// say the session runs in (processes, terminal device and terminal
// variables, which Focus uses to bring it forward) stay inside the tracker
// and are never served. Reading session files from disk is unchanged and
// remains the full history; Transcripts serves a tracked session's
// transcript, redacted, while it runs, and its list adds each session's title,
// compaction summary and activity (latest tool calls, files written, latest
// assistant message), read from the transcript and masked.
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
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/hookevents"
)

// States of a tracked session.
const (
	// StateRunning is a session whose agent is working: its last event was
	// a start, a prompt or tool activity, and not longer ago than IdleAfter.
	StateRunning = "running"
	// StateIdle is a session waiting for its user (it has a WaitingReason:
	// its turn ended, or it asked for a permission or an answer) or silent
	// for IdleAfter: an agent that crashed sends nothing.
	StateIdle = "idle"
	// StateEnded is a session that sent its end event.
	StateEnded = "ended"
)

// Normalised kinds of the last event, the same for every agent.
const (
	EventStart  = "start"
	EventPrompt = "prompt"
	EventTool   = "tool"
	// EventWait is the agent asking its user for something: a Claude Code
	// notification or a permission request of either agent.
	EventWait = "wait"
	EventStop = "stop"
	EventEnd  = "end"
)

// Why a session waits on its user.
const (
	// WaitPermission is a session asking to be allowed to run a tool: a
	// Claude Code permission_prompt notification or a permission request of
	// either agent.
	WaitPermission = "permission"
	// WaitInput is a session asking a question (an elicitation dialog, a
	// background agent waiting on input) or any notification of a type baum
	// does not know, which is taken to want the user.
	WaitInput = "input"
	// WaitTurnFinished is a session whose turn ended (a stop, or Claude
	// Code's idle_prompt, which follows one a minute later).
	WaitTurnFinished = "turn_finished"
	// WaitQuiet is a session silent for IdleAfter with no tool call of its
	// own still running: it stopped without a stop (an interrupted turn
	// sends none) or its agent died. The list sets it (settle), since only
	// the transcript says whether a tool call still runs.
	WaitQuiet = "quiet"
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
	// AnnounceWithin is how recent a wait must be for Waiting to be told of
	// it: an event the spool delivers late is old news.
	AnnounceWithin = 5 * time.Minute
	// maxWaitingMessage bounds the message served, in characters: the app
	// shows one line of it.
	maxWaitingMessage = 160
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
	// EventPrompt, EventTool, EventWait, EventStop or EventEnd).
	LastEvent string `json:"last_event"`
	// WaitingReason is why the session waits on its user (WaitPermission,
	// WaitInput or WaitTurnFinished), absent while it works, once it ended,
	// and for a session that is only silent.
	WaitingReason string `json:"waiting_reason,omitempty"`
	// WaitingSince is when that wait began.
	WaitingSince time.Time `json:"waiting_since,omitzero"`
	// WaitingMessage is the line a Claude Code notification showed for the
	// wait, such as "Claude needs your permission to use Bash": format and
	// control characters dropped, masked as transcripts are and at most
	// maxWaitingMessage characters. Absent when the agent sent none.
	WaitingMessage string `json:"waiting_message,omitempty"`
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
	// Mask, when set, masks the secrets and e-mail addresses in a waiting
	// message, as transcripts are masked; nil keeps the text.
	Mask func(string) string
	// Waiting, when set, is called once for each wait for a permission or an
	// answer (WaitPermission, WaitInput) of a session the application is
	// shown, as it begins: not for a finished turn, not for the waits a
	// rebuild replays when the daemon starts, and not for one that began more
	// than AnnounceWithin ago. It is called without the tracker's lock and
	// must not block.
	Waiting func(Session)

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
	// announcements are the waits to hand Waiting once the lock is released.
	announcements []Session
}

// waitKey names one wait of a session.
type waitKey struct {
	reason string
	since  time.Time
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
	// terminal is where the session's hooks last said it runs, at
	// terminalAt; nil until one did. Focus reads it.
	terminal   *hookevents.Terminal
	terminalAt time.Time
	// announced is the last wait handed to Waiting, or skipped.
	announced waitKey
	ended     bool
	endedAt   time.Time
	linkedAt  time.Time
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
	case "pre-task", "post-task", "post-todo", "post-tool-use", "post-tool-use-failure", "pre-compact", "subagent-start", "subagent-stop":
		return EventTool, true
	case "notification", "permission-request":
		return EventWait, true
	case "stop":
		return EventStop, true
	case "session-end":
		return EventEnd, true
	}
	return "", false
}

// waitReason is why a wait event (EventWait) says its session waits, or false
// for a Claude Code notification that asks nothing of the user: a sign-in
// that succeeded, an answer already given, a background agent that finished,
// a usage limit that resumed on its own, a mod's notice. A notification of a
// type not listed, or with none, is taken to want an answer, so a type Claude
// Code adds later still shows.
func waitReason(e hookevents.Event) (string, bool) {
	if e.Name == "permission-request" {
		return WaitPermission, true
	}
	switch e.Session.NotificationType {
	case "permission_prompt":
		return WaitPermission, true
	case "idle_prompt":
		return WaitTurnFinished, true
	case "auth_success", "elicitation_complete", "elicitation_response", "agent_completed",
		"quota_auto_resume_fired", "quota_auto_resume_disabled", "plugin_notification":
		return "", false
	}
	return WaitInput, true
}

// state is the session's state at now.
func (e *entry) state(now time.Time) string {
	switch {
	case e.ended:
		return StateEnded
	case e.WaitingReason != "" || now.Sub(e.LastActivityAt) > IdleAfter:
		return StateIdle
	}
	return StateRunning
}

// wait records that the session waits on its user for reason from at, or,
// with no reason, that it does not. A wait for the reason it already waits
// for goes on: its start is kept, and its message unless a new one came.
func (e *entry) wait(reason string, at time.Time, message string) {
	switch {
	case reason == "":
		e.WaitingReason, e.WaitingSince, e.WaitingMessage = "", time.Time{}, ""
	case reason != e.WaitingReason:
		e.WaitingReason, e.WaitingSince, e.WaitingMessage = reason, at, message
	case message != "":
		e.WaitingMessage = message
	}
}

// message is a notification's text as the application is shown it: one
// line (oneLine) of at most maxWaitingMessage characters.
func (t *Tracker) message(e hookevents.Event) string {
	return t.oneLine(e.Session.Message, maxWaitingMessage)
}

// oneLine is text an agent wrote as the application is shown it: format
// characters dropped, control characters and runs of spaces made one space,
// masked (before it is cut, so a secret is never cut where the mask cannot
// see it), then cut to limit characters.
func (t *Tracker) oneLine(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	text = strings.Join(strings.Fields(text), " ")
	if text != "" && t.Mask != nil {
		text = t.Mask(text)
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// block is text an agent wrote as the application is shown it with its line
// breaks kept, so it can be rendered as Markdown: format characters dropped,
// line endings made "\n", other control characters but tabs made one space,
// trailing spaces of each line and blank lines at its ends dropped, runs of
// blank lines made one, masked (before it is cut), then cut to limit
// characters.
func (t *Tracker) block(text string, limit int) string {
	text = strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\r\n", "\n")
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, text)
	var lines []string
	blank := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			blank = len(lines) > 0
			continue
		}
		if blank {
			lines = append(lines, "")
			blank = false
		}
		lines = append(lines, line)
	}
	text = strings.Join(lines, "\n")
	if text != "" && t.Mask != nil {
		text = t.Mask(text)
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
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
	// The waits the record replays were told of, if at all, by the daemon
	// that saw them begin.
	for _, s := range t.sessions {
		s.announced = waitKey{s.WaitingReason, s.WaitingSince}
	}
	t.announcements = nil
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
	changed := false
	for _, e := range events {
		if t.applyLocked(e, now) {
			changed = true
		}
	}
	announcements := t.takeAnnouncementsLocked()
	t.mu.Unlock()
	t.announce(announcements)
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
	changed := t.applyLocked(e, now)
	announcements := t.takeAnnouncementsLocked()
	t.mu.Unlock()
	t.announce(announcements)
	return changed
}

// Interrupted records that a session's user turned down, in the terminal, the
// permission or answer it waited for, at at: Claude Code interrupts the turn
// and sends no stop, so the session waits for its user's next prompt, as at
// the end of a turn. A session with an event newer than at, or that waits for
// nothing else, is left as it is. It reports whether the session changed.
func (t *Tracker) Interrupted(agent, id string, at time.Time) bool {
	return t.endWait(agent, id, at, EventStop, WaitTurnFinished, WaitPermission, WaitInput)
}

// Answered records that the app answered the permission or question a
// session waited for, at at: it works again, though the agent reports nothing
// until the tool call ends. A session with an event newer than at, or that
// waits for nothing else, is left as it is. It reports whether the session
// changed.
func (t *Tracker) Answered(agent, id string, at time.Time) bool {
	return t.endWait(agent, id, at, EventTool, "", WaitPermission, WaitInput)
}

// Resumed records that a session's Stop hook handed it a message the app
// queued, at at: the agent goes on with the turn the stop ended, though it
// may report nothing until it calls a tool or stops again. A stop the
// tracker records after this, but which happened before at, is the one that
// delivered the message and is ignored as old news. A session with an event
// newer than at, or that waits for its user, is left as it is. It reports
// whether the session changed.
func (t *Tracker) Resumed(agent, id string, at time.Time) bool {
	return t.endWait(agent, id, at, EventPrompt, "", WaitTurnFinished, "")
}

// Gone records that a session's agent no longer runs, found at at: it ended,
// as its end event would have said had it reached the daemon. An ended
// session is left as it is. It reports whether the session changed.
func (t *Tracker) Gone(agent, id string, at time.Time) bool {
	t.init()
	now := t.now()
	at = at.UTC()
	if at.After(now) {
		at = now
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[key{agent, id}]
	if s == nil || s.ended {
		return false
	}
	if at.Before(s.LastActivityAt) {
		at = s.LastActivityAt
	}
	s.LastActivityAt, s.LastEvent = at, EventEnd
	s.ended, s.endedAt, s.EndedAt = true, at, at
	s.wait("", at, "")
	s.State = s.state(now)
	return true
}

// endWait ends a session's wait, if it is one of ends, at at, with kind as
// its last event and reason as its new wait, unless the session has an event
// newer than at.
func (t *Tracker) endWait(agent, id string, at time.Time, kind, reason string, ends ...string) bool {
	t.init()
	now := t.now()
	at = at.UTC()
	if at.After(now) {
		at = now
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[key{agent, id}]
	if s == nil || s.ended || at.Before(s.LastActivityAt) || !slices.Contains(ends, s.WaitingReason) {
		return false
	}
	s.LastActivityAt, s.LastEvent = at, kind
	s.wait(reason, at, "")
	s.State = s.state(now)
	return true
}

// announceLocked queues the session's wait for Waiting when it is one to
// announce and was not announced yet.
func (t *Tracker) announceLocked(k key, now time.Time) {
	s := t.sessions[k]
	if s == nil || s.unlinked || s.ended || (s.WaitingReason != WaitPermission && s.WaitingReason != WaitInput) {
		return
	}
	wait := waitKey{s.WaitingReason, s.WaitingSince}
	if s.announced == wait {
		return
	}
	s.announced = wait
	if now.Sub(s.WaitingSince) > AnnounceWithin {
		return
	}
	if view, ok := t.view(k, now); ok {
		t.announcements = append(t.announcements, view)
	}
}

func (t *Tracker) takeAnnouncementsLocked() []Session {
	out := t.announcements
	t.announcements = nil
	return out
}

func (t *Tracker) announce(waits []Session) {
	if t.Waiting == nil {
		return
	}
	for _, s := range waits {
		t.Waiting(s)
	}
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
	var announcements []Session
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
				// A new session's wait is announced once it is shown.
				t.announceLocked(r.k, now)
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
		announcements = append(announcements, t.takeAnnouncementsLocked()...)
		t.mu.Unlock()
	}
	t.announce(announcements)
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
	t.announceLocked(k, now)
	after, exists := t.view(k, now)
	return significant(before, after, existed, exists)
}

// applyEvent is the state machine.
func (t *Tracker) applyEvent(e hookevents.Event, now time.Time) {
	if len(e.Session.ID) > maxID || len(e.Agent) > maxAgent {
		return
	}
	kind, _ := normalise(e.Name)
	reason := ""
	if kind == EventWait {
		var ok bool
		if reason, ok = waitReason(e); !ok {
			return
		}
	}
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
	if e.Terminal != nil && !at.Before(s.terminalAt) {
		s.terminal, s.terminalAt = e.Terminal, at
	}
	if kind == EventStart && at.Before(s.StartedAt) {
		s.StartedAt = at
	}
	if at.Before(s.LastActivityAt) {
		// A late delivery of an older event tells nothing newer.
		return
	}
	if e.Name == "subagent-stop" && s.WaitingReason != "" {
		// A subagent that finished says nothing of a session waiting on its
		// user: Claude Code runs one after every stop, and a background one
		// can finish while its session waits.
		return
	}
	if kind == EventStart && e.Session.Source == "compact" && s.WaitingReason != "" {
		// Nor does a compaction: Claude Code compacts a session that has sat
		// waiting a while, starting it again, and no stop follows to say it
		// still waits.
		return
	}
	s.LastActivityAt, s.LastEvent = at, kind
	switch kind {
	case EventStop:
		s.wait(WaitTurnFinished, at, "")
	case EventWait:
		// Claude Code's idle_prompt follows a stop: the turn is the same
		// one, and it never replaces a question asked since.
		if reason != WaitTurnFinished || s.WaitingReason == "" {
			s.wait(reason, at, t.message(e))
		}
	case EventEnd:
		s.ended, s.endedAt, s.EndedAt = true, at, at
		s.wait("", at, "")
	default:
		s.wait("", at, "")
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

// workdir is the session's working directory, for reading paths the session
// wrote relative to it; it is never served.
func (t *Tracker) workdir(agent, id string) string {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.sessions[key{agent, id}]; s != nil {
		return s.cwd
	}
	return ""
}

// Terminal is where the hooks of a session the application is shown last said
// it runs; nil when none did, and for a session that ended: nothing runs to
// bring forward.
func (t *Tracker) Terminal(agent, id string) (*hookevents.Terminal, bool) {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	k := key{agent, id}
	s := t.sessions[k]
	if s == nil || s.expired(now) {
		return nil, false
	}
	if _, ok := t.view(k, now); !ok {
		return nil, false
	}
	if s.ended {
		return nil, true
	}
	return s.terminal, true
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
