// Package chat answers a reviewer's questions about a pull request with the
// agent that wrote its review. A conversation belongs to one saved review
// version and keeps one agent session: its first turn forks the session the
// review was generated in, so the agent starts with everything it read, and
// every later turn resumes the conversation's own session in the same
// working directory. When the review's session is gone, the conversation
// starts a new session in a seeded run directory (the checkout at the
// review's head, the saved review and the earlier turns) and says so.
//
// Only a review by a local Claude Code (User profile) or Codex (Local
// profile) keeps a session, so only those chat. The agent is read-only, as it
// was for the review. Answers are checked (schema, cited files and lines),
// masked for secrets and stored with the conversation in the clone's review
// store (internal/store).
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Limits of what a question and its answer may hold.
const (
	MaxQuestionRunes = 4000
	MaxAnswerBytes   = 20000
	MaxCitations     = 20
	MaxAnchors       = 20
	maxFocusID       = 200
	maxFocusTitle    = 500
	maxPath          = 1024
	// maxSeedTurns bounds the earlier turns a seeded session is given,
	// keeping the newest.
	maxSeedTurns = 64 << 10
)

// Errors a question is refused with before it is queued. The API answers
// each with its own code.
var (
	ErrRepositoryNotFound = errors.New("the repository is not registered")
	ErrNeedsClone         = errors.New("a local clone of the repository is required")
	ErrNoReview           = errors.New("there is no saved review of this pull request at that version")
	ErrUnsupportedBackend = errors.New("only a review written by Claude Code or Codex on this computer can be asked about")
	ErrBusy               = store.ErrConversationBusy
	ErrNotFound           = store.ErrConversationNotFound
	ErrInvalid            = errors.New("invalid question")
)

// Supported reports whether a review by backend can be chatted with: only a
// local Claude Code or Codex session can be resumed.
func Supported(backend string) bool {
	return backend == agent.BackendClaude || backend == agent.BackendCodex
}

// Focus is the part of the review a question is about, as the app sends it.
// It is untrusted data in the prompt.
type Focus struct {
	// Kind is "section", "stop" or "overview".
	Kind    string   `json:"kind"`
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title,omitempty"`
	Anchors []Anchor `json:"anchors,omitempty"`
}

// Anchor is a file, or lines of it, the focused part of the review points at.
type Anchor struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// Validate checks the focus's kind and bounds.
func (f Focus) Validate() error {
	switch f.Kind {
	case "section", "stop", "overview":
	default:
		return fmt.Errorf("%w: focus kind must be section, stop or overview", ErrInvalid)
	}
	if len(f.ID) > maxFocusID || len(f.Title) > maxFocusTitle || !utf8.ValidString(f.ID) || !utf8.ValidString(f.Title) {
		return fmt.Errorf("%w: focus id or title too long", ErrInvalid)
	}
	if len(f.Anchors) > MaxAnchors {
		return fmt.Errorf("%w: at most %d focus anchors", ErrInvalid, MaxAnchors)
	}
	for _, a := range f.Anchors {
		if a.Path == "" || len(a.Path) > maxPath || !utf8.ValidString(a.Path) || a.StartLine < 0 || a.EndLine < 0 || (a.StartLine > 0 && a.EndLine > 0 && a.EndLine < a.StartLine) {
			return fmt.Errorf("%w: each focus anchor needs a path and lines in order", ErrInvalid)
		}
	}
	return nil
}

// Ask is a question to queue: on a new conversation about the review version
// Version, or the next turn of Conversation.
type Ask struct {
	Clone        string
	PR           int
	Version      int
	Conversation string
	Question     string
	Focus        *Focus
}

var conversationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (a Ask) validate() error {
	question := strings.TrimSpace(a.Question)
	switch {
	case a.Clone == "" || a.PR < 1 || a.Version < 1:
		return fmt.Errorf("%w: clone, a positive pr and a positive version are required", ErrInvalid)
	case question == "" || !utf8.ValidString(question) || utf8.RuneCountInString(question) > MaxQuestionRunes:
		return fmt.Errorf("%w: the question must have 1 to %d characters", ErrInvalid, MaxQuestionRunes)
	case a.Conversation != "" && !conversationID.MatchString(a.Conversation):
		return ErrNotFound
	}
	if a.Focus != nil {
		return a.Focus.Validate()
	}
	return nil
}

// Ref names one turn: the clone, pull request, conversation and turn number.
// A chat job carries it.
type Ref struct {
	Clone        string `json:"clone"`
	PR           int    `json:"pr"`
	Conversation string `json:"conversation"`
	Turn         int    `json:"turn"`
}

// Begun is a question stored as a queued turn.
type Begun struct {
	Ref
	Repository     string
	Version        int
	Backend, Model string
}

// Service stores conversations and runs their turns.
type Service struct {
	Registry *repos.Registry
	Config   config.Config
	// Backends builds the agents; the zero value builds the default ones for
	// the resolved configuration.
	Backends generate.Backends
	// Root holds the run directories of seeded sessions; empty is
	// reviewruns.DefaultRoot. The review's kept directories are there too.
	Root string
	// Log receives sizes and outcomes, never questions or answers; nil logs
	// nothing.
	Log *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// RetryDelay is the wait before a provider retry (agent.Generator);
	// nil is the loop's default.
	RetryDelay func(attempt int) time.Duration
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// clone resolves a registered clone; one that is not available needs a clone.
func (s *Service) clone(ctx context.Context, id string, available bool) (repos.Clone, error) {
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return repos.Clone{}, err
	}
	for _, c := range clones {
		if c.ID == id {
			if available && !c.Available {
				return repos.Clone{}, ErrNeedsClone
			}
			return c, nil
		}
	}
	return repos.Clone{}, ErrRepositoryNotFound
}

// Begin stores a question as a queued turn of job jobID: the first turn of a
// new conversation about the review version, or the next turn of an existing
// one. ended reports how the job of a turn still pending ended, so a turn a
// stopped daemon left pending does not keep the conversation busy.
func (s *Service) Begin(ctx context.Context, a Ask, jobID string, ended store.Ended) (Begun, error) {
	if err := a.validate(); err != nil {
		return Begun{}, err
	}
	c, err := s.clone(ctx, a.Clone, true)
	if err != nil {
		return Begun{}, err
	}
	key := store.Key{Repository: c.Repository, Number: a.PR}
	read, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return Begun{}, err
	}
	version, err := read.Get(ctx, key, a.Version)
	if errors.Is(err, store.ErrNotFound) {
		return Begun{}, ErrNoReview
	}
	if err != nil {
		return Begun{}, err
	}
	var focus json.RawMessage
	if a.Focus != nil {
		if focus, err = json.Marshal(a.Focus); err != nil {
			return Begun{}, err
		}
	}
	now := s.now()
	turn := store.Turn{Seq: 1, Question: strings.TrimSpace(a.Question), Focus: focus, JobID: jobID, State: store.TurnQueued, Created: now}
	begun := Begun{Ref: Ref{Clone: c.ID, PR: a.PR}, Repository: strings.ToLower(c.Repository), Version: a.Version}
	if a.Conversation == "" {
		if !Supported(version.Metadata.Backend) {
			return Begun{}, ErrUnsupportedBackend
		}
		st, err := store.Open(c.CommonDir, "", true)
		if err != nil {
			return Begun{}, err
		}
		conversation := store.Conversation{ID: newID(), Version: a.Version, Backend: version.Metadata.Backend, Model: version.Metadata.Model, Created: now, Updated: now}
		if err := st.CreateConversation(ctx, key, conversation, turn); err != nil {
			return Begun{}, err
		}
		begun.Conversation, begun.Turn, begun.Backend, begun.Model = conversation.ID, 1, conversation.Backend, conversation.Model
		return begun, nil
	}
	existing, err := read.Conversation(ctx, key, a.Conversation)
	if err != nil {
		return Begun{}, err
	}
	if existing.Version != a.Version {
		return Begun{}, fmt.Errorf("%w: the conversation is about version %d of the review", ErrInvalid, existing.Version)
	}
	if !Supported(existing.Backend) {
		return Begun{}, ErrUnsupportedBackend
	}
	st, err := store.Open(c.CommonDir, "", true)
	if err != nil {
		return Begun{}, err
	}
	_, seq, err := st.AppendTurn(ctx, key, a.Conversation, turn, ended)
	if err != nil {
		return Begun{}, err
	}
	begun.Conversation, begun.Turn, begun.Backend, begun.Model = a.Conversation, seq, existing.Backend, existing.Model
	return begun, nil
}

// Conversations lists the pull request's conversations, newest first. A turn
// still pending whose job ended reads as ended reports it: the turn is
// settled a moment after its job, and a stopped daemon leaves it pending.
func (s *Service) Conversations(ctx context.Context, clone string, pr int, ended store.Ended) ([]store.Conversation, error) {
	if clone == "" || pr < 1 {
		return nil, fmt.Errorf("%w: clone and a positive pr are required", ErrInvalid)
	}
	c, err := s.clone(ctx, clone, false)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return nil, err
	}
	list, err := st.Conversations(ctx, store.Key{Repository: c.Repository, Number: pr})
	if err != nil {
		return nil, err
	}
	for i := range list {
		for j := range list[i].Turns {
			turn := &list[i].Turns[j]
			if !store.Pending(turn.State) || ended == nil {
				continue
			}
			if state, reason, done := ended(turn.JobID); done && !store.Pending(state) && state != "" {
				turn.State, turn.Reason = state, reason
			}
		}
	}
	return list, nil
}

// Settle ends a turn that is still pending, for a job that failed, was
// cancelled or was interrupted: a completed turn keeps its answer.
func (s *Service) Settle(ctx context.Context, ref Ref, state, reason string, cost *float64, costReason string) error {
	c, err := s.clone(ctx, ref.Clone, false)
	if err != nil {
		return err
	}
	st, err := store.Open(c.CommonDir, "", true)
	if err != nil {
		return err
	}
	key := store.Key{Repository: c.Repository, Number: ref.PR}
	settled, err := st.SettleTurn(ctx, key, ref.Conversation, ref.Turn,
		store.Settlement{State: state, Reason: reason, CostUSD: cost, CostReason: costReason, Finished: s.now()})
	if err != nil || settled {
		return err
	}
	// A next question may have settled the turn from its ended job first,
	// without the job's cost.
	return st.FillTurnCost(ctx, key, ref.Conversation, ref.Turn, cost, costReason)
}
