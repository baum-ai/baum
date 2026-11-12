// Package console runs the conversations of the desktop app's session panel:
// real Claude Code or Codex sessions, set up as the person set up their own
// CLI, that the person opens from any page of the app. It keeps each
// conversation in the application database, chooses the directory its agent
// works in, runs its turns with baum's MCP server next to the person's own,
// and reports their progress as daemon events. See ARCHITECTURE.md (Session
// panel conversations).
package console

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// The agents a conversation runs, named as live sessions and MCP consumers
// name them.
const (
	AgentClaudeCode = "claude-code"
	AgentCodex      = "codex"
)

// The access a conversation's agent has (agent.Access).
const (
	ModeFull      = string(agent.AccessFull)
	ModeWorkspace = string(agent.AccessWorkspace)
)

// The states of a conversation. A failed one can be sent to again; a closed
// one cannot.
const (
	StateIdle    = "idle"
	StateRunning = "running"
	StateFailed  = "failed"
	StateClosed  = "closed"
)

// Limits on what a conversation keeps.
const (
	// MaxMessageBytes bounds one message the person sends.
	MaxMessageBytes = 64 << 10
	// MaxTitleRunes bounds the title taken from the first message.
	MaxTitleRunes = 80
	// MaxPages bounds the pages a conversation remembers being asked from;
	// the oldest are dropped.
	MaxPages = 20
	// maxModel bounds a model name.
	maxModel = 200
	// maxError bounds the reason a turn failed.
	maxError = 2 << 10
)

// Reasons a turn ends without an answer.
const (
	ReasonRestarted = "baum stopped while the agent was answering, so the turn ended without an answer. Send the message again to continue the session."
	ReasonStopping  = "baum stopped while the agent was answering. Send the message again to continue the session."
	ReasonStopped   = "Stopped before the agent finished."
)

var (
	// ErrNotFound is a conversation that does not exist.
	ErrNotFound = errors.New("conversation not found")
	// ErrBusy is a message to a conversation whose agent is still answering.
	ErrBusy = errors.New("the agent is still answering the last message")
	// ErrClosed is a message to, or a stop of, a closed conversation.
	ErrClosed = errors.New("the conversation is closed")
	// ErrStopping is a message sent while the daemon stops.
	ErrStopping = errors.New("baum is stopping")
)

// InvalidError is a request the rules refuse.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// Conversation is one session of the panel.
type Conversation struct {
	ID string `json:"id"`
	// Agent is AgentClaudeCode or AgentCodex; Model is the model asked for,
	// empty for the agent's own default.
	Agent string `json:"agent"`
	Model string `json:"model"`
	// Mode is ModeFull or ModeWorkspace.
	Mode string `json:"mode"`
	// Directory is where the agent works. With Repository and PullRequest it
	// is a worktree of a clone of that repository with the pull request's
	// head on Branch: the person's own, or one baum added
	// (CreatedWorktree). Without them it is an empty directory baum made for
	// the conversation.
	Directory       string `json:"directory"`
	Repository      string `json:"repository,omitempty"`
	PullRequest     int    `json:"pull_request,omitempty"`
	Branch          string `json:"branch,omitempty"`
	CreatedWorktree bool   `json:"created_worktree"`
	// Session is the agent's session id once the agent reported it; every
	// later turn resumes it.
	Session string `json:"session,omitempty"`
	// Pages are the pages the conversation was opened and sent from, oldest
	// first, without repeats in a row, at most MaxPages.
	Pages []agentcontext.PageContext `json:"pages"`
	// Title is the first message, on one line and trimmed.
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	State     string    `json:"state"`
	// Error is why the last turn failed or stopped, empty after an answer.
	Error string `json:"error,omitempty"`
	// CostUSD is what the turns that could be priced cost, nil while none
	// could; CostPartial is set when a turn could not be, so CostUSD is a
	// lower bound.
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	CostPartial bool     `json:"cost_partial,omitempty"`
}

// Pull is what the directory choice needs to know of a pull request.
type Pull struct {
	HeadRef, HeadSHA string
	// Fork is set when the head branch is in another repository.
	Fork bool
}

// Options configure a Service.
type Options struct {
	// Database is the application database.
	Database string
	// Root holds the directories baum makes for conversations: empty ones
	// under sessions/ and worktrees under worktrees/. Empty is
	// <user cache directory>/baum/console. They are never under a
	// directory named with agent.RunDirPrefix, so the agents' hooks report
	// these sessions like any other.
	Root string
	// Registry mints each conversation's MCP token.
	Registry *agentcontext.Registry
	// MCPCommand is the absolute path of the baum the agents start as
	// `baum mcp`; empty is this executable.
	MCPCommand string
	// Clones lists the registered clones; Pull reads a pull request's head
	// as discovery last saw it. Without either, every conversation gets an
	// empty directory.
	Clones func(ctx context.Context) ([]repos.Clone, error)
	Pull   func(ctx context.Context, repository string, number int) (Pull, error)
	// Config is the configuration a turn runs with, the app's settings
	// applied: its run_timeout bounds each turn, and its agent environment
	// and backend paths reach the agent CLI.
	Config func(ctx context.Context) (config.Config, error)
	// Backend builds the agent a turn runs and names the backend its cost is
	// priced as; nil builds the local Claude Code or Codex backend.
	Backend func(agentName, model string, cfg config.Config) (agent.Converser, string, error)
	// Publish receives the events of every change; nil drops them.
	Publish func(Event)
	// Price prices a turn's usage for a backend; nil prices none.
	Price func(backend string, accounting *agent.Accounting) (*float64, string)
	Log   *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service keeps the conversations and runs their turns.
type Service struct {
	opts  Options
	store *store
	root  string
	// base is every turn's parent: Shutdown cancels it.
	base   context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	live     map[string]*live
	page     *agentcontext.PageContext
	stopping bool
	turns    sync.WaitGroup
}

// live is what a conversation holds while the daemon runs: its MCP token and
// its running turn, if any.
type live struct {
	// consumer is the token's consumer id; revoke ends the token.
	token, consumer string
	revoke          func()
	// session is the session the token was bound to, once known.
	session string
	// cancel stops the running turn; done closes when it has been recorded.
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
	// progress is the running or last turn's, for the app to read.
	progress Progress
	// flush publishes the latest text once the throttle allows.
	flush *time.Timer
	// lastText is when the text was last published.
	lastText time.Time
	// limit is the running turn's run time limit.
	limit time.Duration
}

// New opens the conversations in database, marking the ones a stopped
// daemon left running failed (ReasonRestarted).
func New(opts Options) (*Service, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	root := opts.Root
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(cache, "baum", "console")
	}
	st, err := openStore(opts.Database, ReasonRestarted, opts.Now())
	if err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	return &Service{opts: opts, store: st, root: root, base: base, cancel: cancel, live: map[string]*live{}}, nil
}

// CreateRequest opens a conversation.
type CreateRequest struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
	// Mode is ModeFull or ModeWorkspace; empty is ModeFull.
	Mode string `json:"mode"`
	// Page is the page the person opened it from, which decides its
	// directory: a pull request's page gets a checkout of the pull request.
	Page *agentcontext.PageContext `json:"page,omitempty"`
}

// Create opens a conversation: it chooses and, when needed, makes its
// directory, saves it idle and mints its MCP token. The agent runs on the
// first Send.
func (s *Service) Create(ctx context.Context, request CreateRequest) (Conversation, error) {
	if request.Agent != AgentClaudeCode && request.Agent != AgentCodex {
		return Conversation{}, &InvalidError{Message: fmt.Sprintf("agent must be %s or %s", AgentClaudeCode, AgentCodex)}
	}
	if request.Mode == "" {
		request.Mode = ModeFull
	}
	if err := agent.Access(request.Mode).Validate(); err != nil {
		return Conversation{}, &InvalidError{Message: fmt.Sprintf("mode must be %s or %s", ModeFull, ModeWorkspace)}
	}
	request.Model = strings.TrimSpace(request.Model)
	if len(request.Model) > maxModel || strings.ContainsFunc(request.Model, unicode.IsControl) {
		return Conversation{}, &InvalidError{Message: "the model is one line of at most 200 characters"}
	}
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return Conversation{}, ErrStopping
	}
	id := newID()
	placed, err := s.place(ctx, id, request.Page)
	if err != nil {
		return Conversation{}, err
	}
	now := s.opts.Now().UTC()
	c := Conversation{ID: id, Agent: request.Agent, Model: request.Model, Mode: request.Mode, Directory: placed.dir,
		Repository: placed.repository, PullRequest: placed.number, Branch: placed.branch, CreatedWorktree: placed.created,
		Pages: []agentcontext.PageContext{}, CreatedAt: now, UpdatedAt: now, State: StateIdle}
	if request.Page != nil {
		c.Pages = addPage(c.Pages, *request.Page)
	}
	if err := s.store.save(c); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	_, err = s.liveLocked(c)
	s.mu.Unlock()
	if err != nil {
		s.opts.Log.Warn("conversation token not minted", "conversation", id, "error", err)
	}
	s.publish(Event{Conversation: id, Kind: EventCreated, State: c.State})
	return c, nil
}

// Get returns one conversation.
func (s *Service) Get(_ context.Context, id string) (Conversation, error) {
	return s.store.get(id)
}

// List returns every conversation, the most recently updated first.
func (s *Service) List(context.Context) ([]Conversation, error) {
	return s.store.list()
}

// Send starts a turn: the agent answers message in the conversation's
// session, in the background. page, when set, is the page it was sent from.
// It answers at once, with the conversation running; Progress and the
// events follow the turn.
func (s *Service) Send(_ context.Context, id, message string, page *agentcontext.PageContext) (Conversation, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > MaxMessageBytes || !utf8.ValidString(message) {
		return Conversation{}, &InvalidError{Message: fmt.Sprintf("a message is 1 to %d bytes of text", MaxMessageBytes)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return Conversation{}, ErrStopping
	}
	c, err := s.store.get(id)
	if err != nil {
		return Conversation{}, err
	}
	switch c.State {
	case StateClosed:
		return Conversation{}, ErrClosed
	case StateRunning:
		return Conversation{}, ErrBusy
	}
	if info, err := os.Stat(c.Directory); err != nil || !info.IsDir() {
		return Conversation{}, &InvalidError{Message: "the conversation's directory " + c.Directory + " is gone; start a new conversation"}
	}
	l, err := s.liveLocked(c)
	if err != nil {
		return Conversation{}, err
	}
	if page != nil {
		if err := s.opts.Registry.SetPage(l.consumer, *page); err != nil {
			return Conversation{}, &InvalidError{Message: "page: " + err.Error()}
		}
		c.Pages = addPage(c.Pages, *page)
	}
	if c.Title == "" {
		c.Title = title(message)
	}
	c.State, c.Error, c.UpdatedAt = StateRunning, "", s.opts.Now().UTC()
	if err := s.store.save(c); err != nil {
		return Conversation{}, err
	}
	ctx, cancel := context.WithCancel(s.base)
	l.cancel, l.done, l.stopped = cancel, make(chan struct{}), false
	l.progress = Progress{Conversation: id, State: StateRunning, Session: c.Session}
	s.turns.Add(1)
	go s.turn(ctx, c, message, l)
	s.publish(Event{Conversation: id, Kind: EventState, State: c.State})
	return c, nil
}

// Stop cancels the conversation's running turn, killing the agent's whole
// process group, and waits until it is recorded. A conversation that is not
// running is left as it is.
func (s *Service) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	l := s.live[id]
	var done chan struct{}
	if l != nil && l.cancel != nil {
		l.stopped = true
		l.cancel()
		done = l.done
	}
	s.mu.Unlock()
	if done == nil {
		_, err := s.store.get(id)
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the conversation's turn, revokes its MCP token and marks it
// closed. Its directory stays on disk: a worktree may hold the agent's work.
func (s *Service) Close(ctx context.Context, id string) (Conversation, error) {
	if err := s.Stop(ctx, id); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.store.get(id)
	if err != nil {
		return Conversation{}, err
	}
	if l := s.live[id]; l != nil {
		// A turn sent after Stop returned is stopped too; it records the
		// conversation closed.
		if l.cancel != nil {
			l.stopped = true
			l.cancel()
		}
		l.revoke()
		if l.flush != nil {
			l.flush.Stop()
		}
		delete(s.live, id)
	}
	if c.State == StateClosed {
		return c, nil
	}
	c.State, c.UpdatedAt = StateClosed, s.opts.Now().UTC()
	if err := s.store.save(c); err != nil {
		return Conversation{}, err
	}
	s.publish(Event{Conversation: id, Kind: EventState, State: c.State})
	return c, nil
}

// SetPage records the page the person has open for every open conversation's
// MCP token, and for the tokens minted later. It returns the first refusal
// of the page.
func (s *Service) SetPage(page agentcontext.PageContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, l := range s.live {
		if err := s.opts.Registry.SetPage(l.consumer, page); err != nil && first == nil {
			first = err
		}
	}
	if first == nil {
		s.page = &page
	}
	return first
}

// Progress is a turn as it runs, kept in memory: the latest text of the
// agent's answer and the tool it runs.
type Progress struct {
	Conversation string `json:"conversation"`
	State        string `json:"state"`
	// Session is the agent's session id once known.
	Session string `json:"session,omitempty"`
	// Text is the turn's answer so far: its finished messages, then the
	// one streaming; after the turn, its final message.
	Text string `json:"text"`
	// Tool is the title of the tool the agent runs now, if any.
	Tool string `json:"tool,omitempty"`
	// CostUSD is what the turn cost, once it is over and could be priced.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// streaming is the message still streaming, not yet in Text.
	streaming string
}

// Progress returns the running or last turn of the conversation since the
// daemon started, if it had one.
func (s *Service) Progress(id string) (Progress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.live[id]
	if l == nil || l.progress.Conversation == "" {
		return Progress{}, false
	}
	p := l.progress
	p.Text = joinText(p.Text, p.streaming)
	p.streaming = ""
	return p, true
}

// Run waits until ctx ends, then shuts the service down.
func (s *Service) Run(ctx context.Context) {
	<-ctx.Done()
	s.Shutdown()
}

// Shutdown stops every running turn, waits until they are recorded failed
// (ReasonStopping) and revokes every MCP token. Later sends are refused.
func (s *Service) Shutdown() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.cancel()
	s.turns.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.live {
		l.revoke()
		if l.flush != nil {
			l.flush.Stop()
		}
		delete(s.live, id)
	}
}

// CloseStore shuts the service down and closes its database.
func (s *Service) CloseStore() error {
	s.Shutdown()
	return s.store.close()
}

// liveLocked returns the conversation's live state, minting its MCP token on
// first use after the daemon started. A token minted for a conversation
// whose session is known is bound to it at once. s.mu is held.
func (s *Service) liveLocked(c Conversation) (*live, error) {
	if l := s.live[c.ID]; l != nil {
		return l, nil
	}
	consumer := agentcontext.Consumer{Kind: agentcontext.Chat}
	if c.Session != "" {
		consumer.Session = agentcontext.SessionKey(c.Agent, c.Session)
	}
	token, consumerID, revoke, err := s.opts.Registry.Mint(consumer)
	if err != nil {
		return nil, err
	}
	l := &live{token: token, consumer: consumerID, revoke: revoke, session: c.Session}
	if s.page != nil {
		if err := s.opts.Registry.SetPage(consumerID, *s.page); err != nil {
			s.opts.Log.Warn("page not set on a conversation's token", "conversation", c.ID, "error", err)
		}
	}
	s.live[c.ID] = l
	return l, nil
}

func newID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error; it ends the program instead.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// title is a conversation's title from its first message: the first line,
// trimmed, at most MaxTitleRunes characters.
func title(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	line = strings.Join(strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(line) > MaxTitleRunes {
		runes := []rune(line)
		line = strings.TrimSpace(string(runes[:MaxTitleRunes-1])) + "…"
	}
	return line
}

// addPage appends page unless it repeats the last one, keeping at most
// MaxPages.
func addPage(pages []agentcontext.PageContext, page agentcontext.PageContext) []agentcontext.PageContext {
	if n := len(pages); n > 0 {
		last, _ := json.Marshal(pages[n-1])
		next, _ := json.Marshal(page)
		if bytes.Equal(last, next) {
			return pages
		}
	}
	pages = append(slices.Clone(pages), page)
	if len(pages) > MaxPages {
		pages = pages[len(pages)-MaxPages:]
	}
	return pages
}

// joinText puts a message after the text before it, a blank line between.
func joinText(before, next string) string {
	switch {
	case before == "":
		return next
	case next == "":
		return before
	}
	return before + "\n\n" + next
}

// bounded cuts text to at most limit bytes on a rune boundary.
func bounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}
