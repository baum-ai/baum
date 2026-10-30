package chat

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewruns"
	"github.com/baum-ai/baum/internal/store"
)

// Report is what a turn has spent so far, as the job shows it.
type Report struct {
	Phase                  string
	Accounting             *agent.Accounting
	Calls, Tokens, Repairs int
	CostUSD                *float64
	CostReason             string
}

// RunOptions are where a turn's progress goes.
type RunOptions struct {
	// Transcript receives the agent's steps as the job's transcript; nil
	// keeps none.
	Transcript func(agent.TranscriptEntry)
	// Progress receives the phase and what was spent; nil discards it.
	Progress func(Report)
}

// SeededNote starts the note on a turn whose conversation had to start a
// new session without the review agent's own context.
const SeededNote = "Started without the review's own context: "

// NoSessionNote is the note on a turn whose agent reported no session: the
// next question starts from the saved review and the earlier turns.
const NoSessionNote = "The agent kept no session for this answer, so the next question starts without this conversation's context."

// PassTimeout bounds each agent call of a turn: half of a review's
// pass_timeout.
func PassTimeout(cfg config.Config) time.Duration { return time.Duration(cfg.Budgets.PassTimeout) / 2 }

// Budgets are a turn's: half of a review's calls, tokens and wall clock.
func Budgets(cfg config.Config) agent.Budgets {
	return agent.Budgets{WallClock: time.Duration(cfg.Budgets.RunTimeout) / 2, MaxCalls: max(cfg.Budgets.MaxCalls/2, 1), MaxTokens: max(cfg.Budgets.MaxTokens/2, 1),
		MaxProviderAttempts: 3, MaxRepairAttempts: 1}
}

func (s *Service) root() (string, error) {
	if s.Root != "" {
		return s.Root, nil
	}
	return reviewruns.DefaultRoot()
}

func (s *Service) resolve(ctx context.Context) (config.Config, error) {
	prefs, err := s.Registry.Preferences(ctx)
	if err != nil {
		return config.Config{}, err
	}
	return prefs.Resolve(s.Config), nil
}

// Run answers the turn ref names. It marks the turn running, runs the agent
// in the conversation's session (forking the review's on the first turn, or
// seeding a new one when that is gone), checks and masks the answer, and
// stores it with what it cost. A failure leaves the turn running for the
// queue to settle with the job's reason; the error is a *generate.Error whose
// category is the reason, or ErrNeedsClone.
func (s *Service) Run(ctx context.Context, ref Ref, o RunOptions) (report Report, err error) {
	progress := func(phase string) {
		report.Phase = phase
		if o.Progress != nil {
			// The provider's model names are masked only at the end.
			safe := report
			safe.Accounting = report.Accounting.Clone()
			safe.Accounting.ClearStrings()
			o.Progress(safe)
		}
	}
	c, err := s.clone(ctx, ref.Clone, true)
	if errors.Is(err, ErrRepositoryNotFound) {
		err = ErrNeedsClone
	}
	if err != nil {
		return report, err
	}
	cfg, err := s.resolve(ctx)
	if err != nil {
		return report, err
	}
	st, err := store.Open(c.CommonDir, "", true)
	if err != nil {
		return report, err
	}
	key := store.Key{Repository: c.Repository, Number: ref.PR}
	conversation, err := st.Conversation(ctx, key, ref.Conversation)
	if err != nil {
		return report, err
	}
	if ref.Turn < 1 || ref.Turn > len(conversation.Turns) {
		return report, ErrNotFound
	}
	turn := conversation.Turns[ref.Turn-1]
	if started, err := st.StartTurn(ctx, key, ref.Conversation, ref.Turn); err != nil || !started {
		return report, errors.Join(err, &generate.Error{Category: generate.CategoryInterrupted, Err: errors.New("the question was settled before it ran")})
	}
	version, err := st.Get(ctx, key, conversation.Version)
	if errors.Is(err, store.ErrNotFound) {
		return report, &generate.Error{Category: generate.CategoryInput, Err: ErrNoReview}
	}
	if err != nil {
		return report, err
	}
	if !Supported(conversation.Backend) {
		return report, &generate.Error{Category: generate.CategoryBackend, Err: ErrUnsupportedBackend}
	}
	root, err := s.root()
	if err != nil {
		return report, err
	}
	backends := s.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	progress("preparing")
	selected, err := generate.Select(ctx, config.Backend(conversation.Backend), conversation.Model, backends, io.Discard)
	if err != nil {
		return report, err
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return report, err
	}
	var focus *Focus
	if len(turn.Focus) > 0 {
		focus = &Focus{}
		if err := json.Unmarshal(turn.Focus, focus); err != nil {
			return report, err
		}
	}
	budgets := Budgets(cfg)
	runCtx, cancel := context.WithTimeout(ctx, budgets.WallClock)
	defer cancel()
	generator := agent.Generator{Backend: generate.BoundPasses(selected.Backend, PassTimeout(cfg)), Redactor: redactor, RetryDelay: s.RetryDelay}
	var outcome agent.Outcome
	list := pricing.Active()
	backend := selected.Backend.Name()
	account := func() {
		report.Accounting = outcome.Accounting.Clone()
		report.Calls, report.Tokens, report.Repairs = outcome.Calls, outcome.Tokens, outcome.ValidationRepairs
		report.CostUSD, report.CostReason = nil, "usage_not_reported"
		if report.Accounting != nil {
			report.CostUSD, report.CostReason = list.BackendAccountingCost(backend, report.Accounting)
		}
	}
	// Every failure from here on reports what was spent, with the provider's
	// model names masked like the answer.
	defer func() {
		account()
		maskAccounting(context.WithoutCancel(ctx), redactor, report.Accounting)
		if err != nil {
			err = classify(ctx, runCtx, redactor, err)
		}
	}()
	call := func(request agent.Request) (agent.Result, error) {
		request.Schema, request.Task, request.PersistSession, request.Transcript = Schema, true, true, o.Transcript
		progress("answering")
		result, err := generator.Continue(runCtx, budgets, request, &outcome)
		account()
		return result, err
	}

	// Plan the session: resume the conversation's own, fork the review's on
	// the first turn, or seed a new one.
	now := s.now()
	seeded := conversation.Seeded
	var directory, resume, why string
	fork := false
	review := version.Metadata.Session
	switch {
	case conversation.SessionID != "" && !agent.ValidSessionID(conversation.SessionID):
		why = "the conversation's agent session id is not valid"
	case conversation.SessionID != "":
		if reviewruns.Use(root, conversation.Directory, now) {
			directory, resume = conversation.Directory, conversation.SessionID
		} else {
			why = "the conversation's run directory is gone"
		}
	case answered(conversation.Turns[:ref.Turn-1]):
		// An earlier answer's agent kept no session: forking the review
		// again would lose the earlier turns.
		why = "the conversation has no agent session to continue"
	case review == nil || review.ID == "":
		why = "the review kept no agent session"
	case !agent.ValidSessionID(review.ID):
		why = "the review's agent session id is not valid"
	case review.Backend != conversation.Backend:
		why = "the review's agent session is another harness's"
	case !reviewruns.Use(root, review.Directory, now):
		why = "the review's run directory is gone"
	default:
		directory, resume, fork = review.Directory, review.ID, true
	}
	fellBack, recorded := false, false
	var notes []string
	note := func(text string) error {
		notes = append(notes, text)
		return st.NoteTurn(ctx, key, ref.Conversation, ref.Turn, strings.Join(notes, " "))
	}
	// A seeded directory no session was recorded for is not used again.
	defer func() {
		if fellBack && !recorded {
			_ = reviewruns.Remove(root, directory)
		}
	}()
	fallback := func(reason string) error {
		progress("preparing a new session")
		dir, err := s.seed(ctx, root, c, cfg, version, redactor, conversation.Turns[:ref.Turn-1])
		if err != nil {
			return err
		}
		directory, resume, fork, seeded, fellBack = dir, "", false, true, true
		if s.Log != nil {
			s.Log.Info("chat session seeded", "conversation", ref.Conversation, "turn", ref.Turn, "reason", reason)
		}
		return note(SeededNote + reason + ".")
	}
	if directory == "" {
		if err := fallback(why); err != nil {
			return report, err
		}
	}
	text, err := turnPrompt(ctx, redactor, backend, turn.Question, focus, fellBack)
	if err != nil {
		return report, err
	}
	result, err := call(agent.Request{Directory: directory, Prompt: text, ResumeSession: resume, ForkSession: fork})
	if err != nil && errors.Is(err, agent.ErrSessionNotFound) && !fellBack {
		// The session's files are gone though its directory is not: start
		// over once from the saved review and the earlier turns.
		if err := fallback("the agent session was not found"); err != nil {
			return report, err
		}
		if text, err = turnPrompt(ctx, redactor, backend, turn.Question, focus, true); err != nil {
			return report, err
		}
		result, err = call(agent.Request{Directory: directory, Prompt: text})
	}
	if err != nil {
		return report, err
	}
	session := result.SessionID
	if session != "" && !agent.ValidSessionID(session) {
		if s.Log != nil {
			s.Log.Warn("chat agent reported an invalid session id", "conversation", ref.Conversation, "turn", ref.Turn, "bytes", len(session))
		}
		session = ""
	}
	if session == "" && !fork {
		session = resume
	}
	if session != "" {
		if err := st.SetConversationSession(ctx, key, ref.Conversation, session, directory, seeded, s.now()); err != nil {
			return report, err
		}
		recorded = true
	} else {
		// The answer stands, but the next question cannot continue this
		// session: it starts from the saved review and the earlier turns.
		if s.Log != nil {
			s.Log.Warn("chat agent reported no session", "conversation", ref.Conversation, "turn", ref.Turn)
		}
		if err := note(NoSessionNote); err != nil {
			return report, err
		}
	}
	head := filepath.Join(directory, "head")
	document := result.Document
	answer, findings := parse(document)
	var invalid []string
	if findings == "" {
		invalid = invalidCitations(head, answer)
	}
	if (findings != "" || len(invalid) > 0) && session != "" {
		// One repair, in the same session, so the agent keeps what it read.
		outcome.ValidationRepairs++
		progress("checking citations")
		repaired, repairErr := call(agent.Request{Directory: directory, Prompt: repairPrompt(backend, findings, invalid), ResumeSession: session})
		switch {
		case repairErr == nil && findings == "" && schemaInvalid(repaired.Document):
			// Only citations were wrong and the repair broke the answer:
			// keep the first, without its invalid citations.
		case repairErr == nil:
			document = repaired.Document
			if repaired.SessionID != "" && repaired.SessionID != session && agent.ValidSessionID(repaired.SessionID) {
				session = repaired.SessionID
				if err := st.SetConversationSession(ctx, key, ref.Conversation, session, directory, seeded, s.now()); err != nil {
					return report, err
				}
			}
		case ctx.Err() != nil || findings != "":
			// Without a usable first answer the failure is the turn's.
			return report, repairErr
		}
	}
	progress("finishing")
	// What the model wrote is masked before it is checked further, stored or
	// shown.
	masked, err := agent.RedactTaskOutput(context.WithoutCancel(ctx), redactor, document, &outcome)
	if err != nil {
		return report, &generate.Error{Category: generate.CategoryInternal, Err: errors.New("masking the answer failed")}
	}
	answer, findings = parse(masked)
	if findings != "" {
		return report, &generate.Error{Category: generate.CategoryValidation, Err: errors.New("the agent's answer did not match the answer schema")}
	}
	answer, dropped := keepValid(head, answer)
	stored, err := json.Marshal(answer)
	if err != nil {
		return report, err
	}
	account()
	maskAccounting(context.WithoutCancel(ctx), redactor, report.Accounting)
	if _, err := st.SettleTurn(ctx, key, ref.Conversation, ref.Turn, store.Settlement{State: store.TurnCompleted, Answer: stored, CostUSD: report.CostUSD, CostReason: report.CostReason, Finished: s.now()}); err != nil {
		return report, err
	}
	if s.Log != nil {
		s.Log.Info("chat turn answered", "conversation", ref.Conversation, "turn", ref.Turn, "answer_bytes", len(answer.Markdown), "citations", len(answer.Citations), "dropped_citations", dropped, "calls", outcome.Calls, "tokens", outcome.Tokens)
	}
	return report, nil
}

// answered reports whether any of turns was answered.
func answered(turns []store.Turn) bool {
	for _, t := range turns {
		if t.State == store.TurnCompleted {
			return true
		}
	}
	return false
}

// maskAccounting masks the provider's model names, which are model-reported
// text, as a fix's are; on failure it clears them.
func maskAccounting(ctx context.Context, redactor *bundle.Redactor, accounting *agent.Accounting) {
	if accounting == nil {
		return
	}
	for i := range accounting.Attempts {
		masked, _, err := redactor.RedactString(ctx, accounting.Attempts[i].Model)
		if err != nil {
			accounting.ClearStrings()
			return
		}
		accounting.Attempts[i].Model = masked
	}
}

// classify gives a turn's failure the category the job reports as its
// reason, with its text masked.
func classify(parent, run context.Context, redactor *bundle.Redactor, err error) error {
	if errors.Is(err, ErrNeedsClone) || generate.CategoryOf(err) != "" {
		return err
	}
	text := err.Error()
	if masked, _, maskErr := redactor.RedactString(context.WithoutCancel(parent), text); maskErr == nil {
		text = masked
	}
	switch {
	case parent.Err() != nil:
		return &generate.Error{Category: generate.CategoryInterrupted, Err: errors.New("interrupted")}
	case errors.Is(run.Err(), context.DeadlineExceeded):
		return &generate.Error{Category: generate.CategoryBudget, Err: errors.New(agent.GenerationBudgetExceededReason("wall_clock"))}
	case strings.HasPrefix(text, agent.GenerationBudgetExceededReason("")):
		return &generate.Error{Category: generate.CategoryBudget, Err: errors.New(text)}
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConversationNotFound):
		return &generate.Error{Category: generate.CategoryInput, Err: errors.New(text)}
	default:
		return &generate.Error{Category: generate.CategoryBackend, Err: errors.New(text)}
	}
}

// seed makes a new run directory under root for a session that starts
// without the review agent's context: the checkout of the review's head from
// the clone (head/), the saved review as review.json and the earlier
// answered turns as conversation.json. A clone that lacks the commit needs a
// fresh clone.
func (s *Service) seed(ctx context.Context, root string, c repos.Clone, cfg config.Config, version store.Version, redactor *bundle.Redactor, earlier []store.Turn) (dir string, err error) {
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return "", err
	}
	limits := assemble.LimitsFrom(caps).Checkout
	repo, err := gitlocal.Open(ctx, c.Path, gitlocal.Options{})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNeedsClone, err)
	}
	defer func() { _ = repo.Close() }()
	head, err := repo.ResolveCommit(ctx, version.Metadata.Head)
	if err != nil {
		return "", fmt.Errorf("%w: the review's head commit is not in the clone", ErrNeedsClone)
	}
	if dir, err = reviewruns.Create(root); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = reviewruns.Remove(root, dir)
		}
	}()
	archive, err := os.CreateTemp("", "baum-chat-checkout-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archive.Name()) }()
	writeErr := writeCheckout(ctx, limits, repo, head, archive)
	if err := errors.Join(writeErr, archive.Close()); err != nil {
		return "", fmt.Errorf("exporting the checkout: %w", err)
	}
	if err := bundle.UnpackHeadCheckout(ctx, archive.Name(), dir, limits); err != nil {
		return "", fmt.Errorf("unpacking the checkout: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.json"), version.Document, 0o400); err != nil {
		return "", err
	}
	transcript, err := seedConversation(ctx, redactor, earlier)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.json"), transcript, 0o400); err != nil {
		return "", err
	}
	return dir, nil
}

// seedConversation is the earlier answered turns, oldest first, as
// [{"question","answer_markdown"}], keeping the newest that fit in
// maxSeedTurns bytes. Answers are stored masked; the questions, stored as
// the reviewer typed them, are masked here.
func seedConversation(ctx context.Context, redactor *bundle.Redactor, turns []store.Turn) ([]byte, error) {
	type exchange struct {
		Question string `json:"question"`
		Answer   string `json:"answer_markdown"`
	}
	kept := []exchange{}
	size := 2
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.State != store.TurnCompleted || len(t.Answer) == 0 {
			continue
		}
		var answer Answer
		if json.Unmarshal(t.Answer, &answer) != nil {
			continue
		}
		question, _, err := redactor.RedactString(ctx, t.Question)
		if err != nil {
			return nil, err
		}
		e := exchange{Question: question, Answer: answer.Markdown}
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if size+len(raw)+1 > maxSeedTurns {
			break
		}
		size += len(raw) + 1
		kept = append([]exchange{e}, kept...)
	}
	return json.MarshalIndent(kept, "", "  ")
}

// writeCheckout writes the head tree of commit head alone, gzipped, within
// the checkout limits a review has.
func writeCheckout(ctx context.Context, limits bundle.CheckoutConfig, repo *gitlocal.Repo, head string, out io.Writer) error {
	compressed := gzip.NewWriter(out)
	_, err := bundle.NewCheckoutBuilder(limits).WriteArchive(ctx, repo, head, "", nil, compressed)
	return errors.Join(err, compressed.Close())
}
