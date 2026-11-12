package console

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/config"
)

// The kinds of Event.
const (
	// EventCreated: a conversation was opened.
	EventCreated = "created"
	// EventState: a conversation started running or was closed.
	EventState = "state"
	// EventSession: the agent reported its session id.
	EventSession = "session"
	// EventText: the turn's answer grew; Progress has it.
	EventText = "text"
	// EventFinished: the turn ended, answered (idle) or not (failed, or idle
	// when the person stopped it); Get has why.
	EventFinished = "finished"
)

// Event is a change to a conversation, as the daemon's "console" event
// carries it: ids and state, never the conversation's text, which the app
// reads from Progress and Get.
type Event struct {
	Conversation string `json:"conversation"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	// CostUSD is a finished turn's cost, when it could be priced.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// textInterval is the most often a running turn's text is announced: the
// event stream replays only its last 128 events, which text must not crowd
// out.
const textInterval = time.Second

func (s *Service) publish(e Event) {
	if s.opts.Publish != nil {
		s.opts.Publish(e)
	}
}

// turn runs one turn of c's session and records how it ended. ctx is the
// turn's own, which Stop and Shutdown cancel; the run time limit bounds it.
func (s *Service) turn(ctx context.Context, c Conversation, message string, l *live) {
	defer s.turns.Done()
	defer close(l.done)
	result, backend, err := s.converse(ctx, c, message, l)
	var accounting agent.Accounting
	accounting.Add(result, err != nil)
	var cost *float64
	priced := false
	if s.opts.Price != nil && backend != "" {
		var reason string
		cost, reason = s.opts.Price(backend, &accounting)
		priced = cost != nil && reason == ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stopped, stopping := l.stopped, s.stopping
	l.cancel = nil
	if l.flush != nil {
		l.flush.Stop()
		l.flush = nil
	}
	current, readErr := s.store.get(c.ID)
	if readErr != nil {
		s.opts.Log.Warn("conversation not read after its turn", "conversation", c.ID, "error", readErr)
		current = c
	}
	// A session the agent cannot find any more is forgotten, so the next
	// turn starts a new one instead of failing the same way for ever.
	sessionGone := err != nil && errors.Is(err, agent.ErrSessionNotFound) && !stopped && !stopping
	switch {
	case sessionGone:
		current.Session = ""
	case result.SessionID != "" && current.Session == "":
		current.Session = result.SessionID
	case l.session != "" && current.Session == "":
		current.Session = l.session
	}
	current.UpdatedAt = s.opts.Now().UTC()
	closed := current.State == StateClosed
	current.State, current.Error = StateIdle, ""
	switch {
	case err == nil:
	case stopped:
		current.Error = ReasonStopped
	case stopping:
		current.State, current.Error = StateFailed, ReasonStopping
	case sessionGone:
		current.State, current.Error = StateFailed, ReasonSessionGone
	case errors.Is(err, context.DeadlineExceeded):
		current.State = StateFailed
		current.Error = fmt.Sprintf("The answer took longer than the run time limit of %s and was stopped; the limit is in Settings.", limitText(l.limit))
	default:
		current.State, current.Error = StateFailed, bounded(err.Error(), maxError)
	}
	// A conversation closed while the turn ran stays closed.
	if closed {
		current.State = StateClosed
	}
	if cost != nil {
		sum := *cost
		if current.CostUSD != nil {
			sum += *current.CostUSD
		}
		current.CostUSD = &sum
	}
	// A turn that never reached a session spent nothing; any other that
	// could not be priced leaves the total a lower bound.
	if !priced && (err == nil || result.SessionID != "" || l.session != "") {
		current.CostPartial = true
	}
	if saveErr := s.store.save(current); saveErr != nil {
		s.opts.Log.Warn("conversation not saved after its turn", "conversation", c.ID, "error", saveErr)
	}
	s.indexSession(current)
	// A token is bound to one session, once: the new session needs a new
	// token. A conversation that cannot get one mints it on its next turn.
	if sessionGone && !closed && s.live[c.ID] == l {
		s.revokeLocked(l)
		token, consumer, revoke, mintErr := s.opts.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
		if mintErr != nil {
			s.opts.Log.Warn("conversation token not minted again", "conversation", c.ID, "error", mintErr)
			delete(s.live, c.ID)
		} else {
			s.bindLocked(c.ID, l, token, consumer, revoke)
		}
	}
	l.progress.State, l.progress.Session, l.progress.Tool, l.progress.CostUSD = current.State, current.Session, "", cost
	if err == nil && len(result.Document) > 0 {
		final := tail(string(result.Document), agent.MaxConverseBytes)
		l.progress.Text, l.progress.streaming, l.progress.last = final, "", final
	} else {
		if l.progress.streaming != "" {
			l.progress.last = l.progress.streaming
		}
		l.progress.Text, l.progress.streaming = tail(joinText(l.progress.Text, l.progress.streaming), agent.MaxConverseBytes), ""
	}
	if err != nil && !stopped {
		s.opts.Log.Info("conversation turn failed", "conversation", c.ID, "agent", c.Agent, "error", err)
	}
	s.publish(Event{Conversation: c.ID, Kind: EventFinished, State: current.State, CostUSD: cost})
}

// limitText is a run time limit as the turn's failure names it.
func limitText(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

// converse runs the agent for the turn and returns its result and the backend
// its cost is priced as.
func (s *Service) converse(ctx context.Context, c Conversation, message string, l *live) (agent.Result, string, error) {
	cfg := config.Config{}
	if s.opts.Config != nil {
		var err error
		if cfg, err = s.opts.Config(ctx); err != nil {
			return agent.Result{}, "", err
		}
	}
	limit := time.Duration(cfg.Budgets.RunTimeout)
	if limit <= 0 {
		limit = config.DefaultRunTimeout
	}
	s.mu.Lock()
	l.limit = limit
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	backend, name, err := s.backend(c.Agent, c.Model, cfg)
	if err != nil {
		return agent.Result{}, "", err
	}
	command := s.opts.MCPCommand
	if command == "" {
		if command, err = os.Executable(); err != nil {
			return agent.Result{}, name, fmt.Errorf("finding baum for its MCP server: %w", err)
		}
	}
	// The SSH agent socket the daemon inherited may have gone stale since:
	// the agent then gets the login shell's, or none.
	if s.opts.SSHAgent != nil {
		s.opts.SSHAgent(ctx)
	}
	s.mu.Lock()
	token := l.token
	s.mu.Unlock()
	request := agent.Request{
		Directory: c.Directory, Prompt: message, ResumeSession: c.Session,
		MCP:            &agent.MCPServer{Command: command, Args: []string{"mcp"}, Token: token},
		SessionStarted: func(id string) { s.sessionStarted(c, l, id) },
		TextDelta:      func(text string) { s.textDelta(c.ID, l, text) },
		Transcript:     func(entry agent.TranscriptEntry) { s.transcript(c.ID, l, entry) },
	}
	result, err := backend.Converse(ctx, request, agent.Access(c.Mode))
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return result, name, err
}

// backendKey is what an agent backend is built from. The access is not part
// of it: it is passed with each turn.
type backendKey struct{ agent, model, binary, env string }

type builtBackend struct {
	backend agent.Converser
	name    string
}

// backend returns the agent for a turn, built once for each agent, model,
// executable and agent environment, so its CLI's version (and Codex's
// sandbox) is checked once rather than on every turn. A change of the
// executable or the agent environment in Settings builds a new one.
func (s *Service) backend(agentName, model string, cfg config.Config) (agent.Converser, string, error) {
	binary := cfg.BackendPaths[config.BackendClaude]
	if agentName == AgentCodex {
		binary = cfg.BackendPaths[config.BackendCodex]
	}
	key := backendKey{agent: agentName, model: model, binary: binary, env: strings.Join(cfg.AgentEnviron(), "\x00")}
	s.mu.Lock()
	built, ok := s.backends[key]
	s.mu.Unlock()
	if ok {
		return built.backend, built.name, nil
	}
	build := s.opts.Backend
	if build == nil {
		build = localBackend
	}
	backend, name, err := build(agentName, model, cfg)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if built, ok := s.backends[key]; ok {
		return built.backend, built.name, nil
	}
	s.backends[key] = builtBackend{backend: backend, name: name}
	return backend, name, nil
}

// localBackend builds the person's own Claude Code or Codex, with the
// executables cfg names and its agent environment, and no model effort of
// baum's choosing: the CLI's own settings decide.
func localBackend(agentName, model string, cfg config.Config) (agent.Converser, string, error) {
	switch agentName {
	case AgentClaudeCode:
		return &claude.Backend{Binary: cfg.BackendPaths[config.BackendClaude], Model: model, Profile: claude.User, Env: cfg.AgentEnviron()}, agent.BackendClaude, nil
	case AgentCodex:
		return &codex.Backend{Binary: cfg.BackendPaths[config.BackendCodex], Model: model, Profile: codex.Local, Env: cfg.AgentEnviron()}, agent.BackendCodex, nil
	}
	return nil, "", fmt.Errorf("unknown agent %q", agentName)
}

// sessionStarted binds the conversation's token to the session the agent
// reported, saves it and announces it. A token is bound once: a session the
// agent swapped is not bound again.
func (s *Service) sessionStarted(c Conversation, l *live, id string) {
	if !agent.ValidSessionID(id) {
		s.opts.Log.Warn("conversation agent reported an invalid session id", "conversation", c.ID)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.session == "" {
		if err := s.opts.Registry.SetSession(l.consumer, agentcontext.SessionKey(c.Agent, id)); err != nil {
			s.opts.Log.Warn("conversation token not bound to its session", "conversation", c.ID, "error", err)
		}
		l.session = id
	}
	l.progress.Session = id
	current, err := s.store.get(c.ID)
	if err == nil && current.Session == "" {
		current.Session = id
		if err = s.store.save(current); err == nil {
			s.indexSession(current)
		}
	}
	if err != nil {
		s.opts.Log.Warn("conversation session not saved", "conversation", c.ID, "error", err)
	}
	s.publish(Event{Conversation: c.ID, Kind: EventSession, State: StateRunning})
}

// textDelta adds a streamed piece of the answer.
func (s *Service) textDelta(id string, l *live, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.progress.streaming = tail(l.progress.streaming+text, agent.MaxConverseBytes)
	s.textChangedLocked(id, l)
}

// transcript follows the agent's steps: a finished message joins the text
// (replacing what streamed of it), and a tool shows while it runs.
func (s *Service) transcript(id string, l *live, entry agent.TranscriptEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch entry.Kind {
	case "message":
		l.progress.Text, l.progress.streaming = tail(joinText(l.progress.Text, entry.Text), agent.MaxConverseBytes), ""
		l.progress.last = tail(entry.Text, agent.MaxConverseBytes)
	case "tool":
		if entry.Status == "running" {
			l.progress.Tool = bounded(entry.Title, 512)
		} else {
			l.progress.Tool = ""
		}
	default:
		return
	}
	s.textChangedLocked(id, l)
}

// textChangedLocked announces new text at most every textInterval: at once
// when the last announcement is that old, otherwise once the interval is up.
// s.mu is held.
func (s *Service) textChangedLocked(id string, l *live) {
	if l.flush != nil {
		return
	}
	wait := textInterval - s.opts.Now().Sub(l.lastText)
	if wait <= 0 {
		l.lastText = s.opts.Now()
		s.publish(Event{Conversation: id, Kind: EventText, State: StateRunning})
		return
	}
	l.flush = time.AfterFunc(wait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if l.flush == nil {
			return
		}
		l.flush = nil
		l.lastText = s.opts.Now()
		s.publish(Event{Conversation: id, Kind: EventText, State: StateRunning})
	})
}
