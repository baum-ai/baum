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
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	// ReasonSessionGone: the agent could not find the session to resume.
	ReasonSessionGone = "The agent could not find this conversation's session any more (its history may have been removed), so the next message starts a new session in the same directory."
)

var (
	// ErrNotFound is a conversation that does not exist.
	ErrNotFound = errors.New("conversation not found")
	// ErrBusy is a message to a conversation whose agent is still answering.
	ErrBusy = errors.New("the agent is still answering the last message")
	// ErrClosed is a message to, or a stop of, a closed conversation.
	ErrClosed = errors.New("the conversation is closed")
	// ErrShuttingDown is a conversation opened or a message sent once the
	// service has begun to shut down.
	ErrShuttingDown = errors.New("baum is shutting down")
)

// InvalidError is a request the rules refuse. Code is CodeInvalidPage for a
// page the MCP server would refuse, else CodeInvalidRequest.
type InvalidError struct {
	Code    string
	Message string
}

// The codes of InvalidError.
const (
	CodeInvalidRequest = "invalid_request"
	CodeInvalidPage    = "invalid_page"
)

func invalid(format string, args ...any) error {
	return &InvalidError{Code: CodeInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func invalidPage(err error) error {
	return &InvalidError{Code: CodeInvalidPage, Message: "page: " + err.Error()}
}

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
	// CreatedBranch is set when baum also created Branch for that worktree.
	// A branch baum did not create is the person's: nothing baum does when
	// it cleans up may delete it.
	CreatedBranch bool `json:"created_branch"`
	// DirectoryNote says why a conversation opened on a pull request works
	// in an empty directory instead of a checkout of it: no clone, or a head
	// that could not be fetched or checked out.
	DirectoryNote string `json:"directory_note,omitempty"`
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

// Defaults are what a conversation opens with when the request leaves them
// out: the panel's setting (repos.Preferences.ResolvePanel).
type Defaults struct {
	Agent, Model, Mode string
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
	// MCPURL is the daemon's MCP route the agents reach over HTTP, read at
	// each turn because the app server's port is known only once it listens.
	// Nil or empty refuses the turn.
	MCPURL func() string
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
	// Defaults gives the agent, model and mode a conversation opens with
	// when its request names none; nil is Claude Code, its own default model
	// and full access.
	Defaults func(ctx context.Context) (Defaults, error)
	// Proposals holds what conversations' agents propose (the MCP server's);
	// nil lists none.
	Proposals *agentcontext.Proposals
	// SSHAgent, when set, is asked before every turn for a usable SSH agent
	// socket: it replaces a stale SSH_AUTH_SOCK in this process's environment,
	// which the agent's environment comes from (shellenv.Agent.Socket).
	SSHAgent func(ctx context.Context) string
	// Price prices a turn's usage for a backend; nil prices none.
	Price func(backend string, accounting *agent.Accounting) (*float64, string)
	// Transcript writes the transcript of the agent's session id as the live
	// sessions' transcript route answers (livesessions.Transcripts.Read and
	// WriteAnswer): found by its id in the agent's own session store,
	// whether or not the tracker lists it, never from a path a request
	// names. Nil answers every conversation's transcript as unavailable.
	Transcript func(w http.ResponseWriter, r *http.Request, agent, id string)
	Log        *slog.Logger
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
	// creating counts the Creates in flight and turns the running turns,
	// which Shutdown waits for before it revokes the tokens.
	creating sync.WaitGroup
	turns    sync.WaitGroup
	// placing serializes placement per repository (lockRepository).
	placing map[string]*sync.Mutex
	// backends are the agents built so far, by what builds them, so each
	// checks its CLI's version once.
	backends map[backendKey]builtBackend
	// consumers maps each live token's consumer id to its conversation, under
	// its own lock: the proposals' OnChange reads it, and runs while a revoke
	// under mu is in progress.
	cmu       sync.Mutex
	consumers map[string]string
	// sessions ("agent:id") and dirs (the directories baum made) map to
	// their conversation, for Owns; under cmu too.
	sessions map[string]string
	dirs     map[string]string
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
	s := &Service{opts: opts, store: st, root: root, base: base, cancel: cancel, live: map[string]*live{},
		placing: map[string]*sync.Mutex{}, backends: map[backendKey]builtBackend{}, consumers: map[string]string{},
		sessions: map[string]string{}, dirs: map[string]string{}}
	all, err := st.list()
	if err != nil {
		_ = st.close()
		cancel()
		return nil, err
	}
	for _, c := range all {
		s.indexDirectory(c)
		s.indexSession(c)
	}
	if opts.Proposals != nil {
		opts.Proposals.OnChange(s.proposalsChanged)
	}
	return s, nil
}

// CreateRequest opens a conversation.
type CreateRequest struct {
	// Agent, Model and Mode left out are the Defaults: a request that names
	// no agent takes the default agent and model, one that names the default
	// agent and no model the default model. Mode is ModeFull or
	// ModeWorkspace.
	Agent string  `json:"agent,omitempty"`
	Model *string `json:"model,omitempty"`
	Mode  string  `json:"mode,omitempty"`
	// Page is the page the person opened it from, which decides its
	// directory: a pull request's page gets a checkout of the pull request.
	Page *agentcontext.PageContext `json:"page,omitempty"`
}

// Create opens a conversation: it chooses and, when needed, makes its
// directory, saves it idle and mints its MCP token. The agent runs on the
// first Send.
func (s *Service) Create(ctx context.Context, request CreateRequest) (Conversation, error) {
	defaults := Defaults{Agent: AgentClaudeCode, Mode: ModeFull}
	if s.opts.Defaults != nil {
		chosen, err := s.opts.Defaults(ctx)
		if err != nil {
			return Conversation{}, err
		}
		defaults = chosen
	}
	model := ""
	switch {
	case request.Model != nil:
		model = *request.Model
	case request.Agent == "" || request.Agent == defaults.Agent:
		model = defaults.Model
	}
	request.Agent = cmp.Or(request.Agent, defaults.Agent)
	request.Mode = cmp.Or(request.Mode, defaults.Mode)
	if request.Agent != AgentClaudeCode && request.Agent != AgentCodex {
		return Conversation{}, invalid("agent must be %s or %s", AgentClaudeCode, AgentCodex)
	}
	if request.Mode == "" {
		request.Mode = ModeFull
	}
	if err := agent.Access(request.Mode).Validate(); err != nil {
		return Conversation{}, invalid("mode must be %s or %s", ModeFull, ModeWorkspace)
	}
	model = strings.TrimSpace(model)
	if len(model) > maxModel || strings.ContainsFunc(model, unicode.IsControl) {
		return Conversation{}, invalid("the model is one line of at most 200 characters")
	}
	if request.Page != nil {
		if err := request.Page.Validate(); err != nil {
			return Conversation{}, invalidPage(err)
		}
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return Conversation{}, ErrShuttingDown
	}
	// Shutdown waits for this Create, so the row is saved for any directory
	// it made and the token it mints is revoked with the others.
	s.creating.Add(1)
	s.mu.Unlock()
	defer s.creating.Done()
	// Shutting down cancels a fetch in progress; the conversation then gets
	// an empty directory.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.base, cancel)()
	id := newID()
	placed, err := s.place(ctx, id, request.Page)
	if err != nil {
		return Conversation{}, err
	}
	now := s.opts.Now().UTC()
	c := Conversation{ID: id, Agent: request.Agent, Model: model, Mode: request.Mode, Directory: placed.dir,
		Repository: placed.repository, PullRequest: placed.number, Branch: placed.branch, CreatedWorktree: placed.created,
		CreatedBranch: placed.newBranch, DirectoryNote: placed.note, Pages: []agentcontext.PageContext{}, CreatedAt: now, UpdatedAt: now, State: StateIdle}
	if request.Page != nil {
		c.Pages = addPage(c.Pages, *request.Page)
	}
	if err := s.store.save(c); err != nil {
		return Conversation{}, err
	}
	s.indexDirectory(c)
	s.mu.Lock()
	// The person is on the page they opened the conversation from, so it is
	// the page every token, the new one included, has open.
	if request.Page != nil {
		s.setPageLocked(*request.Page)
	}
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
		return Conversation{}, invalid("a message is 1 to %d bytes of text", MaxMessageBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return Conversation{}, ErrShuttingDown
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
		return Conversation{}, invalid("the conversation's directory %s is gone; start a new conversation", c.Directory)
	}
	l, err := s.liveLocked(c)
	if err != nil {
		return Conversation{}, err
	}
	if page != nil {
		if err := page.Validate(); err != nil {
			return Conversation{}, invalidPage(err)
		}
		s.setPageLocked(*page)
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
		s.revokeLocked(l)
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
// MCP token, and for the tokens minted later, and refuses a page the MCP
// server would refuse. Create and Send set the page they come from too.
func (s *Service) SetPage(page agentcontext.PageContext) error {
	if err := page.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPageLocked(page)
	return nil
}

// setPageLocked sets a valid page on every token. s.mu is held.
func (s *Service) setPageLocked(page agentcontext.PageContext) {
	for id, l := range s.live {
		if err := s.opts.Registry.SetPage(l.consumer, page); err != nil {
			s.opts.Log.Warn("page not set on a conversation's token", "conversation", id, "error", err)
		}
	}
	s.page = &page
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
	// Message is the turn's latest message on its own: the one streaming,
	// else the last one finished (after the turn, its final message). The
	// app shows it until the session's transcript holds it, so a turn of
	// several messages shows each as it comes rather than all or none.
	Message string `json:"message,omitempty"`
	// streaming is the message still streaming, not yet in Text; last is the
	// last message finished.
	streaming, last string
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
	p.Message = p.last
	if p.streaming != "" {
		p.Message = p.streaming
	}
	p.Text = tail(joinText(p.Text, p.streaming), agent.MaxConverseBytes)
	p.streaming, p.last = "", ""
	return p, true
}

// Run waits until ctx ends, then shuts the service down.
func (s *Service) Run(ctx context.Context) {
	<-ctx.Done()
	s.Shutdown()
}

// Shutdown stops every running turn, waits until they and the Creates in
// flight are recorded, the turns failed (ReasonStopping), and revokes every
// MCP token. Later Creates and Sends are refused with ErrShuttingDown.
func (s *Service) Shutdown() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.cancel()
	s.creating.Wait()
	s.turns.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.live {
		s.revokeLocked(l)
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
	l := &live{}
	s.bindLocked(c.ID, l, token, consumerID, revoke)
	l.session = c.Session
	s.live[c.ID] = l
	return l, nil
}

// bindLocked gives l a newly minted token, with the page the person has
// open. s.mu is held.
func (s *Service) bindLocked(id string, l *live, token, consumer string, revoke func()) {
	l.token, l.consumer, l.revoke, l.session = token, consumer, revoke, ""
	s.cmu.Lock()
	s.consumers[consumer] = id
	s.cmu.Unlock()
	if s.page != nil {
		if err := s.opts.Registry.SetPage(consumer, *s.page); err != nil {
			s.opts.Log.Warn("page not set on a conversation's token", "conversation", id, "error", err)
		}
	}
}

// revokeLocked revokes l's token, which drops its open proposals and
// announces that while the token still maps to its conversation. s.mu is
// held.
func (s *Service) revokeLocked(l *live) {
	l.revoke()
	s.cmu.Lock()
	delete(s.consumers, l.consumer)
	s.cmu.Unlock()
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

// tail keeps the last limit bytes of text, from a rune boundary.
func tail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
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
