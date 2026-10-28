package decisionlog

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// Skip reasons. The transient ones are tried again the next day, or when the
// transcript changes; the others only when it changes.
const (
	ReasonTooLarge      = "too_large"
	ReasonUnreadable    = "unreadable"
	ReasonMismatch      = "session_mismatch"
	ReasonGenerationRun = "generation_run"
	ReasonOverTokenCap  = "over_token_cap"
	ReasonFailed        = "extraction_failed"
	// Transient.
	ReasonUnsupported = "backend_unsupported"
	ReasonUnavailable = "backend_unavailable"
	ReasonUnpriced    = "unpriced_model"
	ReasonOverCostCap = "over_cost_cap"
	ReasonDailyCap    = "daily_cap"
)

var transient = map[string]bool{ReasonUnsupported: true, ReasonUnavailable: true, ReasonUnpriced: true, ReasonOverCostCap: true, ReasonDailyCap: true}

// Outcomes of a capture.
const (
	OutcomeDisabled    = "disabled"
	OutcomeThrottled   = "throttled"
	OutcomeUnavailable = "unavailable"
	OutcomeUnchanged   = "unchanged"
	OutcomeSkipped     = "skipped"
	OutcomeHit         = "hit"
	OutcomeDryRun      = "dry_run"
	OutcomeExtracted   = "extracted"
	OutcomeFailed      = "failed"
	// OutcomeAbandoned is a capture the daemon stopping cut short; its
	// attempt is abandoned and nothing else is recorded.
	OutcomeAbandoned = "abandoned"
)

// Throttle bounds.
const (
	MinInterval  = 5 * time.Minute
	FailureDelay = 10 * time.Minute
	MaxPerDay    = 12
	MaxFailures  = 3
)

// Registry is what capture reads of the repository registry.
type Registry interface {
	Preferences(context.Context) (repos.Preferences, error)
	RepositorySettings(ctx context.Context, repository string) (repos.Settings, bool, error)
}

// Capturer captures one session at a time (README.md, "Capture").
type Capturer struct {
	Store    *Store
	Registry Registry
	// Config is the config file's; the preferences and the repository's
	// settings are layered on it for every capture.
	Config config.Config
	// Backends builds the backends; zero is generate.DefaultBackendsFor.
	Backends generate.Backends
	// Home holds the agents' session stores; empty is the user's home.
	Home string
	// CursorStore is Cursor's database; empty is its default.
	CursorStore string
	// Extractor is nil for the published prompt's.
	Extractor *decisions.Extractor
	// Prices is nil for pricing.Active.
	Prices func() *pricing.List
	Now    func() time.Time
	Log    *slog.Logger

	// locate is livesessions.Locate unless a test replaces it.
	locate func(home, cursorStore, agent, id, path string) (livesessions.Location, error)

	once     sync.Once
	redactor *bundle.Redactor
	initErr  error
}

// Target is a session to capture.
type Target struct {
	Agent, ID string
	// Path is the transcript path the session's hooks named, if any.
	Path               string
	Repository, Branch string
	// Ended is a session that sent its end event: the daily attempt limit
	// does not hold it back.
	Ended bool
}

// Options change one capture.
type Options struct {
	// DryRun stops before the call and writes nothing.
	DryRun bool
	// Force skips the gate, the throttle and the unchanged check, not the
	// caps.
	Force bool
	// Backend and Model, when set, replace the configured ones.
	Backend, Model string
	// DebugDir, when set, receives the rendered transcript (transcript.txt),
	// the user message sent (user.txt) and the raw reply (reply.json), for
	// testing by hand. Each capture first removes the previous capture's
	// files. The daemon never sets it.
	DebugDir string
}

// Report is what a capture did.
type Report struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	// Mode is "full" or "extend".
	Mode    string `json:"mode,omitempty"`
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	Turns   int    `json:"turns"`
	// Tokens is the estimated prompt, MaxTokens the bound reserved for.
	Tokens    int64    `json:"estimated_tokens"`
	MaxTokens int64    `json:"max_tokens"`
	Reserved  float64  `json:"reserved_usd"`
	Cost      *float64 `json:"cost_usd,omitempty"`
	Decisions int      `json:"decisions"`
}

func (c *Capturer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Capturer) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Capturer) prices() *pricing.List {
	if c.Prices != nil {
		return c.Prices()
	}
	return pricing.Active()
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// throttled is why the session may not be called now, empty when it may: at
// most one attempt per MinInterval, none within FailureDelay of a failure,
// and after MaxPerDay attempts in a day only once it ended.
func throttled(st Stats, ended bool, now time.Time) string {
	switch {
	case !st.LastFail.IsZero() && now.Sub(st.LastFail) < FailureDelay:
		return "failed_recently"
	case !st.LastStart.IsZero() && now.Sub(st.LastStart) < MinInterval:
		return "attempted_recently"
	case st.Count >= MaxPerDay && !ended:
		return "daily_attempts"
	}
	return ""
}

// covered reports whether the session's row covers its transcript as
// fingerprinted, so there is nothing to do: an extraction, a lasting skip, or
// a transient skip recorded today.
func covered(row Session, fingerprint string, day time.Time) bool {
	if row.Fingerprint != fingerprint {
		return false
	}
	return row.Coverage == CoverageExtracted || !transient[row.SkipReason] || !row.UpdatedAt.Before(day)
}

// Prune prunes the store (Store.Prune).
func (c *Capturer) Prune(ctx context.Context) error { return c.Store.Prune(ctx) }

// Capture runs the pipeline for one session. An error is one the store or the
// registry returned, or a failed call; what was skipped is in the report.
func (c *Capturer) Capture(ctx context.Context, t Target, o Options) (Report, error) {
	c.once.Do(func() {
		c.redactor, c.initErr = bundle.NewRedactor()
		if c.Extractor == nil {
			c.Extractor = newExtractor()
		}
	})
	if c.initErr != nil {
		return Report{}, c.initErr
	}
	if o.DebugDir != "" {
		// What a previous capture rendered, sent and got back is not this
		// one's.
		for _, stale := range []string{"transcript.txt", "user.txt", "reply.json"} {
			if err := os.Remove(filepath.Join(o.DebugDir, stale)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Report{}, err
			}
		}
	}
	now := c.now()
	day := startOfDay(now)
	report := Report{}
	meta := Meta{Agent: t.Agent, ID: t.ID, Repository: t.Repository, Branch: t.Branch, Path: t.Path}
	skip := func(reason string) (Report, error) {
		report.Outcome, report.Reason = OutcomeSkipped, reason
		if o.DryRun {
			return report, nil
		}
		return report, c.Store.Skip(ctx, meta, reason)
	}

	prefs, err := c.Registry.Preferences(ctx)
	if err != nil {
		return report, err
	}
	var settings repos.Settings
	if t.Repository != "" {
		var picked bool
		if settings, picked, err = c.Registry.RepositorySettings(ctx, t.Repository); err != nil {
			return report, err
		}
		if !picked && !o.Force {
			return Report{Outcome: OutcomeDisabled, Reason: "not_picked"}, nil
		}
	}
	if !o.Force && (t.Repository == "" || !settings.DecisionsEnabled(prefs)) {
		return Report{Outcome: OutcomeDisabled}, nil
	}
	if !o.Force {
		st, err := c.Store.Stats(ctx, t.Agent, t.ID, day)
		if err != nil {
			return report, err
		}
		if reason := throttled(st, t.Ended, now); reason != "" {
			return Report{Outcome: OutcomeThrottled, Reason: reason}, nil
		}
	}

	head, found, err := c.Store.Session(ctx, t.Agent, t.ID)
	if err != nil {
		return report, err
	}
	// A session whose hooks named no transcript (a Codex one is then looked up
	// by id, a scan of its whole store) is read from the path the last capture
	// confirmed, checked again; when that path no longer holds it, it is
	// looked up as the hooks name it. A path is stored only once the
	// transcript there is the session's.
	locate := c.locate
	if locate == nil {
		locate = livesessions.Locate
	}
	candidates := []string{t.Path}
	if t.Path == "" && found && head.Path != "" {
		candidates = []string{head.Path, ""}
		meta.Path = head.Path
	}
	var loc livesessions.Location
	var graph *session.Graph
	var firstErr error
	for i, path := range candidates {
		last := i == len(candidates)-1
		if loc, err = locate(c.home(), c.cursorStore(), t.Agent, t.ID, path); err != nil {
			firstErr = cmp.Or(firstErr, err)
			if last {
				return Report{Outcome: OutcomeUnavailable, Reason: firstErr.Error()}, nil
			}
			// The stored path was just shown wrong: a skip must not keep it.
			meta.Path = ""
			continue
		}
		if meta.Fingerprint, err = loc.Version(); err != nil {
			return Report{Outcome: OutcomeUnavailable, Reason: "The transcript file is not there any more."}, nil
		}
		size, err := loc.Size()
		if err != nil {
			return Report{Outcome: OutcomeUnavailable, Reason: "The transcript file is not there any more."}, nil
		}
		if size > MaxTranscriptBytes {
			return skip(ReasonTooLarge)
		}
		if found && !o.Force && covered(head, meta.Fingerprint, day) {
			// The file the head covers, found again by the hooks' path after
			// the stored one failed: the path the fingerprint names is
			// repaired.
			if i > 0 && !o.DryRun {
				return Report{Outcome: OutcomeUnchanged}, c.Store.SetPath(ctx, t.Agent, t.ID, loc.Path())
			}
			return Report{Outcome: OutcomeUnchanged}, nil
		}
		graph, err = loc.Graph()
		if err != nil || graph == nil {
			c.logger().Info("session transcript not read for decision capture", "agent", t.Agent, "error", err)
			return skip(ReasonUnreadable)
		}
		if isSession(graph, t.ID) {
			break
		}
		meta.Path = ""
		if last {
			return skip(ReasonMismatch)
		}
	}
	meta.Path = loc.Path()
	if sessions.IsGenerationRun(graph) {
		return skip(ReasonGenerationRun)
	}
	rendered, err := decisions.Render(ctx, graph, c.redactor, decisions.WithVendor(string(loc.Vendor)))
	if err != nil {
		c.logger().Info("session transcript not rendered for decision capture", "agent", t.Agent, "error", err)
		return skip(ReasonUnreadable)
	}
	report.Turns = len(rendered.Turns)
	if err := debugFile(o.DebugDir, "transcript.txt", []byte(rendered.Text())); err != nil {
		return report, err
	}
	hash := decisions.ContentHash(rendered)
	options := decisions.ExtractOptions{MaxInputTokens: MaxInputTokens, MaxOutputTokens: MaxOutputTokens}
	report.Mode = "full"
	if found && head.ContentHash != "" {
		if head.ContentHash == hash {
			report.Outcome, report.Decisions = OutcomeHit, len(head.Decisions)
			if o.DryRun {
				return report, nil
			}
			return report, c.Store.Covered(ctx, meta)
		}
		options.Existing = head.Decisions
		options.Cutoff, _ = decisions.ExtensionCutoff(rendered, head.PrefixHash)
		report.Mode = "extend"
	}

	report.Tokens = c.Extractor.EstimateTokens(rendered, options)
	report.MaxTokens = c.Extractor.MaxTokens(rendered, options)
	if report.Tokens > MaxInputTokens {
		return skip(ReasonOverTokenCap)
	}

	extraction := Extraction{Meta: meta, ContentHash: hash, PrefixHash: decisions.PrefixHash(rendered),
		PromptVersion: c.Extractor.PromptVersion(), TurnTimes: turnTimes(graph)}
	if len(rendered.Turns) == 0 {
		// No turns, no call: the existing decisions stand.
		report.Outcome = OutcomeExtracted
		if o.DryRun {
			report.Outcome = OutcomeDryRun
			return report, nil
		}
		extraction.Decisions = decisions.Identified(options.Existing)
		report.Decisions = len(extraction.Decisions)
		return report, c.Store.Save(ctx, extraction)
	}
	cfg := settings.Resolve(prefs.Resolve(c.Config))
	requested := cfg.Backend
	if o.Backend != "" {
		requested = config.Backend(o.Backend)
	}
	backends := c.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	if report.Backend, err = resolveBackend(ctx, requested, backends); err != nil {
		c.logger().Info("decision capture backend unavailable", "backend", requested, "error", err)
		return skip(ReasonUnavailable)
	}
	report.Model = decisionModel(c.prices(), report.Backend, prefs.Decisions.Model)
	if o.Model != "" {
		report.Model = o.Model
	}
	extraction.Backend, extraction.Model = report.Backend, report.Model
	if report.Backend == agent.BackendCursorAgent {
		return skip(ReasonUnsupported)
	}
	reserved, ok := reserve(c.prices(), report.Backend, report.Model, report.MaxTokens)
	if !ok {
		return skip(ReasonUnpriced)
	}
	report.Reserved = reserved
	st, err := c.Store.Stats(ctx, t.Agent, t.ID, day)
	if err != nil {
		return report, err
	}
	if st.Spent+reserved > SessionDailyCap {
		return skip(ReasonOverCostCap)
	}
	if limit := prefs.Decisions.DailyCap; limit != nil && st.AllSpent+reserved > *limit {
		return skip(ReasonDailyCap)
	}
	if o.DryRun {
		report.Outcome = OutcomeDryRun
		return report, nil
	}

	selected, err := generate.Select(ctx, config.Backend(report.Backend), report.Model, backends, io.Discard)
	if err != nil {
		c.logger().Info("decision capture backend unavailable", "backend", report.Backend, "error", err)
		return skip(ReasonUnavailable)
	}
	completer, ok := selected.Backend.(agent.Completer)
	if !ok {
		return skip(ReasonUnsupported)
	}
	attempt, err := c.Store.Begin(ctx, Attempt{Agent: t.Agent, ID: t.ID, Repository: t.Repository, Backend: report.Backend, Model: report.Model, Mode: report.Mode, Reserved: reserved})
	if err != nil {
		return report, err
	}
	model := &completerModel{completer: completer, redactor: c.redactor}
	result, callErr := c.Extractor.Extract(ctx, model, rendered, options)
	if model.called {
		_ = debugFile(o.DebugDir, "user.txt", []byte(model.user))
		_ = debugFile(o.DebugDir, "reply.json", model.raw)
	}
	// The ledger and the store outlive a cancelled capture: the call may be
	// paid for.
	write := context.WithoutCancel(ctx)
	spent := cost(c.prices(), model.model, model.usage)
	if !model.called {
		spent = new(0.0)
	}
	report.Cost = spent
	usage := decisionUsage(model.usage)
	if callErr != nil && ctx.Err() != nil {
		// Cut short by the daemon stopping: not the session's failure.
		report.Outcome, report.Reason = OutcomeAbandoned, "cancelled"
		if err := c.Store.Settle(write, attempt, AttemptAbandoned, report.Reason, spent, usage); err != nil {
			return report, err
		}
		return report, ctx.Err()
	}
	if callErr != nil {
		report.Outcome, report.Reason = OutcomeFailed, failureReason(callErr)
		if err := c.Store.Settle(write, attempt, AttemptFailed, report.Reason, spent, usage); err != nil {
			return report, err
		}
		failures, err := c.Store.Failed(write, meta)
		if err != nil {
			return report, err
		}
		if failures >= MaxFailures {
			if err := c.Store.Skip(write, meta, ReasonFailed); err != nil {
				return report, err
			}
		}
		return report, fmt.Errorf("extracting the decisions of %s session %s: %w", t.Agent, t.ID, callErr)
	}
	if err := c.Store.Settle(write, attempt, AttemptSucceeded, "", spent, usage); err != nil {
		return report, err
	}
	extraction.Decisions = decisions.Identified(result.Decisions)
	extraction.Dropped, extraction.Usage, extraction.Cost = result.Dropped, result.Usage, spent
	if result.Model != "" {
		extraction.Model = result.Model
	}
	report.Outcome, report.Decisions = OutcomeExtracted, len(extraction.Decisions)
	return report, c.Store.Save(write, extraction)
}

// debugFile writes data to name in dir, a private directory it creates, as a
// private file replacing any earlier one; nothing without a dir.
func debugFile(dir, name string, data []byte) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = file.Chmod(0o600)
	if _, writeErr := file.Write(data); err == nil {
		err = writeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// isSession reports whether the graph's root session is the agent's session
// id: the mapper gives it an ID of its own and records the agent's.
func isSession(graph *session.Graph, id string) bool {
	root := graph.Root()
	if root == nil {
		return false
	}
	for _, v := range graph.VendorIDs {
		if v.OurID == root.ID && v.OurType == session.EntitySession && v.VendorID == id {
			return true
		}
	}
	return root.ID == id
}

// failureReason names a failed call for the ledger, without its text.
func failureReason(err error) string {
	var billed *decisions.BilledError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &billed):
		return "unusable_reply"
	}
	return "call_failed"
}

func turnTimes(graph *session.Graph) map[int]time.Time {
	times := map[int]time.Time{}
	root := graph.Root()
	for _, input := range graph.UserInputs {
		if input.SessionID == root.ID && input.TriggeredAt != nil {
			times[input.Ordinal] = *input.TriggeredAt
		}
	}
	return times
}

func (c *Capturer) home() string {
	if c.Home != "" {
		return c.Home
	}
	return transcript.UserHome()
}

func (c *Capturer) cursorStore() string {
	if c.CursorStore != "" {
		return c.CursorStore
	}
	return cursortranscript.DefaultStorePath()
}
