// Package agentrequests lets the desktop app answer what a coding agent asks
// its user, and send a running session a message. A Claude Code or Codex
// PermissionRequest hook describes the request to the daemon over the
// control socket (MethodRequest) and holds until the app answers, the
// request is withdrawn or the hook goes away; a Stop hook takes the message
// queued for its session (MethodTakeMessage) at once.
//
// The hook keeps the agent's own payload and builds the agent's answer from
// it: the daemon is told only what the app shows (rows, suggestion labels and
// questions), masks every string as transcripts are masked, and answers with
// a behaviour, a suggestion index and the chosen options. Requests and
// messages live in memory only, are never persisted or logged, and never
// leave the machine; only Settings is kept, in the daemon's state directory.
package agentrequests

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/livesessions"
)

// Control requests a hook sends.
const (
	// MethodRequest is a waiting control request: its params are an Ask, its
	// result the Decision, which is empty when nobody decided.
	MethodRequest = "agent.request"
	// MethodTakeMessage answers at once with the message queued for a
	// session (TakeParams, Taken) and removes it.
	MethodTakeMessage = "agent.take-message"
)

// Agents whose requests the app answers.
const (
	AgentClaudeCode = "claude-code"
	AgentCodex      = "codex"
)

// Kinds of request.
const (
	KindPermission = "permission"
	// KindQuestion is Claude Code's AskUserQuestion.
	KindQuestion = "question"
)

// Behaviours of a decision.
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
)

// Bounds, in characters unless named otherwise.
const (
	// MaxValue bounds a row's value, which may keep its newlines.
	MaxValue = 4000
	// MaxMessage bounds a queued message.
	MaxMessage   = 4000
	maxLabel     = 300
	maxText      = 1000
	maxRows      = 8
	MaxQuestions = 8
	MaxOptions   = 16
	// MaxSuggestions bounds the suggestions shown; a hook sends the first
	// ones, so an index stays the agent's own.
	MaxSuggestions = 16
	maxPending     = 64
	maxID          = 128
	// DefaultCodexWait is how long a Codex request waits for the app before
	// the hook prints nothing and Codex asks in its terminal: the hook's
	// own timeout is 30 seconds.
	DefaultCodexWait = 25 * time.Second
	// recentFor and maxRecent bound the turn boundaries and finished tool
	// calls remembered for a request that arrives after them.
	recentFor = 10 * time.Minute
	maxRecent = 512
	// SettingsFile is the name of the settings file in the state directory.
	SettingsFile = "agent-requests.json"
)

// Errors of Answer and Queue.
var (
	// ErrGone is a request answered already, in the terminal or the app,
	// timed out, or withdrawn because its session moved on.
	ErrGone = errors.New("this request was already answered, timed out or its session moved on")
	// ErrNotRunning is a message for a session that is not working on a
	// turn, so no Stop hook would take it.
	ErrNotRunning = errors.New("the session is not running")
)

// BadRequestError is an answer or message the store refuses as given.
type BadRequestError struct{ Reason string }

func (e *BadRequestError) Error() string { return e.Reason }

func bad(reason string) error { return &BadRequestError{Reason: reason} }

// Row is one line of what a request would do, as the app shows it.
type Row struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Code renders the value monospace.
	Code bool `json:"code,omitempty"`
}

// Option is one answer a question offers.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one of the questions an AskUserQuestion request asks.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	MultiSelect bool     `json:"multi_select"`
	Options     []Option `json:"options"`
}

// Ask is a hook's request: MethodRequest's params.
type Ask struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Tool      string `json:"tool"`
	// Digest is Digest of the tool's name and input, which the session's
	// post-tool-use event carries once the tool ran.
	Digest string `json:"digest,omitempty"`
	// StartedAt is when the hook started: a turn boundary or a finished
	// tool call recorded since makes the request moot.
	StartedAt   time.Time  `json:"started_at"`
	Input       []Row      `json:"input,omitempty"`
	Suggestions []string   `json:"suggestions,omitempty"`
	Questions   []Question `json:"questions,omitempty"`
}

// Decision is the app's answer to an Ask; an empty Behavior decides nothing,
// and the hook prints nothing.
type Decision struct {
	Behavior string `json:"behavior,omitempty"`
	// Suggestion is the index of the permission suggestion to apply.
	Suggestion *int `json:"suggestion,omitempty"`
	// Answers holds one answer per question, in order.
	Answers []Answer `json:"answers,omitempty"`
}

// Answer is the answer to one question: the indexes of the options chosen,
// or text the user wrote instead.
type Answer struct {
	Options []int  `json:"options,omitempty"`
	Text    string `json:"text,omitempty"`
}

// TakeParams name the session whose message MethodTakeMessage takes.
type TakeParams struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

// Taken answers MethodTakeMessage; Text is empty when none was queued.
type Taken struct {
	Text string `json:"text,omitempty"`
}

// Request is a pending request as the app is shown it.
type Request struct {
	ID          string       `json:"id"`
	Agent       string       `json:"agent"`
	SessionID   string       `json:"session_id"`
	Kind        string       `json:"kind"`
	Tool        string       `json:"tool"`
	CreatedAt   time.Time    `json:"created_at"`
	Input       []Row        `json:"input"`
	Suggestions []Suggestion `json:"suggestions"`
	Questions   []Question   `json:"questions"`
}

// Suggestion is a permission suggestion; its ID is its index.
type Suggestion struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Message is a message queued for a session's next Stop hook.
type Message struct {
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	Text      string    `json:"text"`
	QueuedAt  time.Time `json:"queued_at"`
}

// Settings are what the user chose.
type Settings struct {
	// CodexApprovals lets the app answer Codex's permission requests. Codex
	// hides its own prompt while the hook waits, so it is off unless chosen.
	CodexApprovals bool `json:"codex_approvals"`
}

// Reply is the app's answer to a request.
type Reply struct {
	Behavior   string `json:"behavior"`
	Suggestion string `json:"suggestion,omitempty"`
	// Answers maps a question's text, as shown, to the label chosen (labels
	// joined by ", " for several) or to text the user wrote.
	Answers map[string]string `json:"answers,omitempty"`
}

// Sessions are the live sessions; *livesessions.Tracker is one.
type Sessions interface {
	Lookup(agent, id string) (livesessions.Session, string, bool)
}

// Store holds the pending requests and queued messages.
type Store struct {
	// Sessions decides which sessions can be sent a message.
	Sessions Sessions
	// Mask masks what an agent wrote, as transcripts are masked; nil keeps
	// it.
	Mask func(string) string
	// Notify is called after the requests or messages changed. It must not
	// block.
	Notify func()
	// StateDir holds SettingsFile; empty keeps the settings in memory.
	StateDir string
	// CodexWait replaces DefaultCodexWait.
	CodexWait time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	Log *slog.Logger

	once     sync.Once
	mu       sync.Mutex
	pending  map[string]*pending
	messages map[key]Message
	recent   []mark
	settings Settings
}

type key struct{ agent, id string }

// mark is a turn boundary (no digest) or a finished tool call of a session.
type mark struct {
	key
	digest string
	at     time.Time
}

type pending struct {
	Request
	digest    string
	startedAt time.Time
	decided   chan Decision
	gone      chan struct{}
}

func (s *Store) init() {
	s.once.Do(func() {
		s.pending, s.messages = map[string]*pending{}, map[key]Message{}
		s.settings = s.loadSettings()
	})
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) notify() {
	if s.Notify != nil {
		s.Notify()
	}
}

func (s *Store) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Ask shows a request to the app and waits until it is answered (the
// Decision), withdrawn, ctx ends or, for Codex, CodexWait passed; all but the
// first decide nothing. A Codex request decides nothing at once unless the
// user turned CodexApprovals on.
func (s *Store) Ask(ctx context.Context, ask Ask) (Decision, error) {
	s.init()
	if err := ask.check(); err != nil {
		return Decision{}, err
	}
	var timeout <-chan time.Time
	if ask.Agent == AgentCodex {
		if !s.Settings().CodexApprovals {
			return Decision{}, nil
		}
		wait := cmp.Or(s.CodexWait, DefaultCodexWait)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	p := s.newPending(ask)
	s.mu.Lock()
	if len(s.pending) >= maxPending || s.passedLocked(p) {
		s.mu.Unlock()
		return Decision{}, nil
	}
	s.pending[p.ID] = p
	s.mu.Unlock()
	s.notify()
	defer s.drop(p)
	select {
	case d := <-p.decided:
		return d, nil
	case <-p.gone:
	case <-ctx.Done():
	case <-timeout:
	}
	return Decision{}, nil
}

func (a Ask) check() error {
	switch {
	case a.Agent != AgentClaudeCode && a.Agent != AgentCodex:
		return bad("unsupported agent")
	case a.SessionID == "" || len(a.SessionID) > maxID:
		return bad("invalid session id")
	case a.Kind != KindPermission && a.Kind != KindQuestion:
		return bad("unknown kind")
	case a.Kind == KindQuestion && (len(a.Questions) == 0 || a.Agent != AgentClaudeCode):
		return bad("a question needs questions")
	case a.Agent == AgentCodex && len(a.Suggestions) > 0:
		return bad("Codex has no permission suggestions")
	case len(a.Input) > maxRows || len(a.Suggestions) > MaxSuggestions || len(a.Questions) > MaxQuestions:
		return bad("too many rows, suggestions or questions")
	}
	for _, q := range a.Questions {
		if len(q.Options) > MaxOptions {
			return bad("too many options")
		}
	}
	return nil
}

// newPending is the request as the app is shown it: every string the agent
// wrote cleaned, masked and bounded.
func (s *Store) newPending(ask Ask) *pending {
	r := Request{
		ID: newID(), Agent: ask.Agent, SessionID: ask.SessionID, Kind: ask.Kind,
		Tool: s.line(ask.Tool, maxLabel), CreatedAt: s.now(),
		Input: []Row{}, Suggestions: []Suggestion{}, Questions: []Question{},
	}
	for _, row := range ask.Input {
		r.Input = append(r.Input, Row{Label: s.line(row.Label, maxLabel), Value: s.block(row.Value, MaxValue), Code: row.Code})
	}
	for i, label := range ask.Suggestions {
		r.Suggestions = append(r.Suggestions, Suggestion{ID: strconv.Itoa(i), Label: s.line(label, maxLabel)})
	}
	for _, q := range ask.Questions {
		shown := Question{Question: s.line(q.Question, maxText), Header: s.line(q.Header, maxLabel), MultiSelect: q.MultiSelect, Options: []Option{}}
		for _, o := range q.Options {
			shown.Options = append(shown.Options, Option{Label: s.line(o.Label, maxLabel), Description: s.line(o.Description, maxText)})
		}
		r.Questions = append(r.Questions, shown)
	}
	startedAt := ask.StartedAt.UTC()
	if startedAt.IsZero() || startedAt.After(r.CreatedAt) {
		startedAt = r.CreatedAt
	}
	return &pending{Request: r, digest: ask.Digest, startedAt: startedAt, decided: make(chan Decision, 1), gone: make(chan struct{})}
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// drop removes a request that stopped waiting.
func (s *Store) drop(p *pending) {
	s.mu.Lock()
	removed := s.pending[p.ID] == p
	if removed {
		delete(s.pending, p.ID)
	}
	s.mu.Unlock()
	if removed {
		s.notify()
	}
}

// Answer decides a pending request with the app's reply. A request that is
// no longer pending is ErrGone; a reply that does not fit it is a
// *BadRequestError.
func (s *Store) Answer(id string, reply Reply) error {
	s.init()
	s.mu.Lock()
	p := s.pending[id]
	if p == nil {
		s.mu.Unlock()
		return ErrGone
	}
	d, err := p.decide(reply)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	delete(s.pending, id)
	p.decided <- d
	s.mu.Unlock()
	s.notify()
	return nil
}

func (p *pending) decide(reply Reply) (Decision, error) {
	switch {
	case reply.Behavior == BehaviorDeny:
		return Decision{Behavior: BehaviorDeny}, nil
	case reply.Behavior != BehaviorAllow:
		return Decision{}, bad(`behavior must be "allow" or "deny"`)
	case p.Kind == KindQuestion:
		if reply.Suggestion != "" {
			return Decision{}, bad("a question has no suggestions")
		}
		answers, err := p.answers(reply.Answers)
		return Decision{Behavior: BehaviorAllow, Answers: answers}, err
	case len(reply.Answers) > 0:
		return Decision{}, bad("a permission request has no questions")
	case reply.Suggestion == "":
		return Decision{Behavior: BehaviorAllow}, nil
	}
	index, err := strconv.Atoi(reply.Suggestion)
	if err != nil || index < 0 || index >= len(p.Suggestions) || strconv.Itoa(index) != reply.Suggestion {
		return Decision{}, bad("unknown suggestion")
	}
	return Decision{Behavior: BehaviorAllow, Suggestion: &index}, nil
}

// answers turns the app's answers, keyed by the questions and labels it was
// shown, into option indexes. Every question needs an answer; one that names
// no option is the user's own text.
func (p *pending) answers(given map[string]string) ([]Answer, error) {
	out := make([]Answer, len(p.Questions))
	answered := 0
	for text, value := range given {
		index := slices.IndexFunc(p.Questions, func(q Question) bool { return q.Question == text })
		if index < 0 {
			return nil, bad("an answer names an unknown question")
		}
		value = strings.TrimSpace(value)
		if value == "" || utf8.RuneCountInString(value) > maxText || !utf8.ValidString(value) {
			return nil, bad("an answer is empty or too long")
		}
		out[index] = matchOptions(p.Questions[index], value)
		answered++
	}
	if answered != len(p.Questions) {
		return nil, bad("every question needs an answer")
	}
	return out, nil
}

// matchOptions reads value as the labels of options, joined by ", " when
// the question takes several, or else as the user's own text.
func matchOptions(q Question, value string) Answer {
	label := func(text string) int {
		return slices.IndexFunc(q.Options, func(o Option) bool { return o.Label == text })
	}
	if i := label(value); i >= 0 {
		return Answer{Options: []int{i}}
	}
	if q.MultiSelect {
		parts := strings.Split(value, ", ")
		var chosen []int
		for start := 0; start < len(parts); {
			found := false
			for end := len(parts); end > start; end-- {
				if i := label(strings.Join(parts[start:end], ", ")); i >= 0 {
					chosen, start, found = append(chosen, i), end, true
					break
				}
			}
			if !found {
				return Answer{Text: value}
			}
		}
		return Answer{Options: chosen}
	}
	return Answer{Text: value}
}

// passedLocked reports whether the session moved on since the request's hook
// started: its turn ended or a new one began, or the same tool call finished,
// so the user answered in the terminal before the request arrived.
func (s *Store) passedLocked(p *pending) bool {
	k := key{p.Agent, p.SessionID}
	for _, m := range s.recent {
		if m.key == k && !m.at.Before(p.startedAt) && (m.digest == "" || (p.digest != "" && m.digest == p.digest)) {
			return true
		}
	}
	return false
}

// Observe applies a recorded hook event: a turn boundary of a session
// (user-prompt-submit, stop, session-end) withdraws its requests, a finished
// tool call (post-tool-use) the request for the same call, the oldest when
// several match, and a session's end drops its message too. Only requests
// whose hooks started before the event are withdrawn, so a late event never
// withdraws a newer request.
func (s *Store) Observe(e hookevents.Event) {
	s.init()
	if !Match(e) {
		return
	}
	m := mark{key: key{e.Agent, e.Session.ID}, at: e.Time.UTC()}
	if e.Name == "post-tool-use" {
		m.digest = e.Session.ToolDigest
	}
	now := s.now()
	s.mu.Lock()
	s.recent = slices.DeleteFunc(append(s.recent, m), func(r mark) bool { return now.Sub(r.at) > recentFor })
	if len(s.recent) > maxRecent {
		s.recent = slices.Clone(s.recent[len(s.recent)-maxRecent:])
	}
	var withdrawn []*pending
	for _, p := range s.pending {
		if p.key() == m.key && !m.at.Before(p.startedAt) && (m.digest == "" || m.digest == p.digest) {
			withdrawn = append(withdrawn, p)
		}
	}
	if m.digest != "" && len(withdrawn) > 1 {
		slices.SortFunc(withdrawn, func(a, b *pending) int { return a.CreatedAt.Compare(b.CreatedAt) })
		withdrawn = withdrawn[:1]
	}
	for _, p := range withdrawn {
		delete(s.pending, p.ID)
		close(p.gone)
	}
	_, queued := s.messages[m.key]
	ended := e.Name == "session-end" && queued
	if ended {
		delete(s.messages, m.key)
	}
	s.mu.Unlock()
	if len(withdrawn) > 0 || ended {
		s.notify()
	}
}

func (p *pending) key() key { return key{p.Agent, p.SessionID} }

// Match reports whether Observe acts on an event; the daemon subscribes with
// it.
func Match(e hookevents.Event) bool {
	if e.Kind != hookevents.KindAgent || e.Session == nil || e.Session.ID == "" || e.Time.IsZero() {
		return false
	}
	switch e.Name {
	case "user-prompt-submit", "stop", "session-end":
		return true
	case "post-tool-use":
		return e.Session.ToolDigest != ""
	}
	return false
}

// Run observes events until ctx ends or the channel closes.
func (s *Store) Run(ctx context.Context, events <-chan hookevents.Record) {
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-events:
			if !ok {
				return
			}
			s.Observe(record.Event)
		}
	}
}

// Queue queues a message for the session's next Stop hook, replacing one
// queued before. The text must not be blank or longer than MaxMessage; the
// session must be working on a turn (ErrNotRunning otherwise).
func (s *Store) Queue(agent, id, text string) error {
	s.init()
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxMessage {
		return bad("the message is empty or too long")
	}
	if !s.running(agent, id) {
		return ErrNotRunning
	}
	s.mu.Lock()
	s.messages[key{agent, id}] = Message{Agent: agent, SessionID: id, Text: text, QueuedAt: s.now()}
	s.mu.Unlock()
	s.notify()
	return nil
}

// running reports whether a Stop hook will come: the session is listed, has
// not ended and is not waiting for its next prompt.
func (s *Store) running(agent, id string) bool {
	if agent != AgentClaudeCode && agent != AgentCodex || s.Sessions == nil {
		return false
	}
	session, _, ok := s.Sessions.Lookup(agent, id)
	return ok && session.State != livesessions.StateEnded && session.WaitingReason != livesessions.WaitTurnFinished
}

// TakeMessage removes and returns the message queued for a session.
func (s *Store) TakeMessage(agent, id string) (string, bool) {
	s.init()
	s.mu.Lock()
	m, ok := s.messages[key{agent, id}]
	delete(s.messages, key{agent, id})
	s.mu.Unlock()
	if ok {
		s.notify()
	}
	return m.Text, ok
}

// DeleteMessage removes the message queued for a session, if any.
func (s *Store) DeleteMessage(agent, id string) {
	s.TakeMessage(agent, id)
}

// List is the pending requests, oldest first, and the queued messages of
// sessions still listed, with their text masked.
func (s *Store) List() ([]Request, []Message) {
	s.init()
	s.mu.Lock()
	requests := make([]Request, 0, len(s.pending))
	for _, p := range s.pending {
		requests = append(requests, p.Request)
	}
	messages := make([]Message, 0, len(s.messages))
	for _, m := range s.messages {
		messages = append(messages, m)
	}
	s.mu.Unlock()
	slices.SortFunc(requests, func(a, b Request) int { return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID)) })
	slices.SortFunc(messages, func(a, b Message) int { return a.QueuedAt.Compare(b.QueuedAt) })
	shown := messages[:0]
	for _, m := range messages {
		if s.Sessions != nil {
			if session, _, ok := s.Sessions.Lookup(m.Agent, m.SessionID); !ok || session.State == livesessions.StateEnded {
				s.DeleteMessage(m.Agent, m.SessionID)
				continue
			}
		}
		m.Text = s.block(m.Text, MaxMessage)
		shown = append(shown, m)
	}
	return requests, shown
}

// Settings are the current settings.
func (s *Store) Settings() Settings {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// SetSettings saves new settings. Turning Codex approvals off withdraws the
// Codex requests waiting, which then fall back to Codex's own prompt.
func (s *Store) SetSettings(settings Settings) (Settings, error) {
	s.init()
	if s.StateDir != "" {
		data, err := json.Marshal(settings)
		if err != nil {
			return Settings{}, err
		}
		if err := writeAtomic(filepath.Join(s.StateDir, SettingsFile), data); err != nil {
			return Settings{}, err
		}
	}
	s.mu.Lock()
	s.settings = settings
	withdrawn := 0
	if !settings.CodexApprovals {
		for id, p := range s.pending {
			if p.Agent == AgentCodex {
				delete(s.pending, id)
				close(p.gone)
				withdrawn++
			}
		}
	}
	s.mu.Unlock()
	if withdrawn > 0 {
		s.notify()
	}
	return settings, nil
}

func (s *Store) loadSettings() Settings {
	if s.StateDir == "" {
		return Settings{}
	}
	data, err := os.ReadFile(filepath.Join(s.StateDir, SettingsFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger().Warn("agent request settings not read; using the defaults", "error", err)
		}
		return Settings{}
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		s.logger().Warn("agent request settings not read; using the defaults", "error", err)
		return Settings{}
	}
	return settings
}

// writeAtomic replaces path with data through a temporary file, private to
// the user.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-requests-*")
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

// line is text an agent wrote as one line: format characters dropped,
// control characters and runs of spaces made one space, masked before it is
// cut, so a secret is never cut where the mask cannot see it.
func (s *Store) line(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	return s.finish(strings.Join(strings.Fields(text), " "), limit)
}

// block is line for a value that keeps its line breaks and tabs.
func (s *Store) block(text string, limit int) string {
	text = strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\r\n", "\n")
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, text)
	return s.finish(strings.TrimRight(text, " \t\n"), limit)
}

func (s *Store) finish(text string, limit int) string {
	if text != "" && s.Mask != nil {
		text = s.Mask(text)
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// Digest identifies a tool call by its tool's name and input, so a request
// and the post-tool-use event of the same call match without either keeping
// the input: SHA-256 of the name and the input's canonical JSON (members
// sorted, numbers as written). An AskUserQuestion is identified by its
// questions alone, since the input it ran with carries the answers. It is
// empty for input that is not JSON.
func Digest(tool string, input json.RawMessage) string {
	if tool == "AskUserQuestion" {
		var members map[string]json.RawMessage
		if json.Unmarshal(input, &members) != nil {
			return ""
		}
		input, _ = json.Marshal(map[string]json.RawMessage{"questions": members["questions"]})
	}
	canonical, err := canonicalJSON(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(data json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}
