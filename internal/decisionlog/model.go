package decisionlog

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
)

// Limits of one capture.
const (
	// MaxInputTokens caps a transcript's estimated prompt.
	MaxInputTokens = 272_000
	// MaxOutputTokens caps the reply, and is reserved in full.
	MaxOutputTokens = 16_384
	// MaxTranscriptBytes caps the session's own files.
	MaxTranscriptBytes = 64 << 20
)

// newExtractor is the extractor of the published decision prompt.
func newExtractor() *decisions.Extractor {
	return decisions.NewPublicExtractor()
}

// DefaultDecisionModel is the model decision capture uses with backend when
// the preferences name none: a small, cheap one of the backend's provider.
func DefaultDecisionModel(backend string) string {
	switch backend {
	case agent.BackendCodex:
		return "gpt-6-luna"
	case agent.BackendClaude, agent.BackendAnthropic:
		return "claude-haiku-5-5"
	}
	return ""
}

// vendor is the price list's vendor of a backend's models.
func vendor(backend string) string {
	if backend == agent.BackendCodex {
		return pricing.VendorCodex
	}
	return pricing.VendorClaudeCode
}

// reserve is the most a completion with at most inputTokens of prompt and
// MaxOutputTokens of reply per call costs at the model's rates, as hosted
// extraction reserves one call; false when the list would not price it.
// Claude Code caches the prompt, so claude's first call is reserved at the
// higher of the input and cache-write rates. A further call the backend can
// make (maxCalls) resends the same prompt, which the first call cached, so it
// is reserved at the cache-read rate plus its reply.
func reserve(list *pricing.List, backend, model string, inputTokens int64) (float64, bool) {
	return reserveFor(list, backend, model, inputTokens, MaxOutputTokens)
}

// reserveFor is reserve for a call whose reply is at most outputTokens.
func reserveFor(list *pricing.List, backend, model string, inputTokens, outputTokens int64) (float64, bool) {
	v := vendor(backend)
	rates, ok := list.RatesFor(list.Normalize(model, v), pricing.Usage{Vendor: v, InputTokens: inputTokens, OutputTokens: outputTokens, SingleRequest: true})
	if !ok {
		return 0, false
	}
	reply := float64(outputTokens) * rates.Output
	input := rates.Input
	if backend == agent.BackendClaude {
		input = max(input, rates.CacheWrite)
	}
	first := float64(inputTokens)*input + reply
	further := float64(maxCalls(backend)-1) * (float64(inputTokens)*rates.CacheRead + reply)
	return (first + further) / 1_000_000, true
}

// cost prices a call's reported usage as review accounting does
// (pricing.List.OutcomeCost): nil when it cannot be priced or any part of it
// is unknown, such as an unreported speed or 1-hour cache-write split, or a
// prompt range that straddles a long-prompt tier.
func cost(list *pricing.List, model string, u agent.Usage) *float64 {
	return list.OutcomeCost(model, agent.Outcome{Usage: u})
}

// decisionUsage converts disjoint buckets to decisions.Usage, whose input
// includes the cached part.
func decisionUsage(u agent.Usage) decisions.Usage {
	return decisions.Usage{InputTokens: u.Input + u.CacheRead + u.CacheCreate, CachedInputTokens: u.CacheRead, OutputTokens: u.Output}
}

// completerModel is a decisions.Model over an agent.Completer. It masks
// secrets in the reply before the extractor reads it, and remembers whether a
// call was made and what it reported, for the ledger.
type completerModel struct {
	completer agent.Completer
	redactor  *bundle.Redactor
	called    bool
	// raw is the last reply as the provider returned it.
	raw   []byte
	user  string
	model string
	usage agent.Usage
	// rejections counts the replies the harness rejected against the
	// schema, detail the last one's reason.
	rejections int
	detail     string
	// attempt names the ledger row, for the log.
	attempt Attempt
	// timeout bounds each call; zero is CallTimeout.
	timeout time.Duration
}

func (m *completerModel) Complete(ctx context.Context, req decisions.ModelRequest) (decisions.ModelResponse, error) {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		return decisions.ModelResponse{}, err
	}
	m.called, m.user = true, req.User
	timeout := cmp.Or(m.timeout, CallTimeout)
	call, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := m.completer.Complete(call, agent.CompleteRequest{System: req.System, User: req.User, Schema: schema,
		MaxOutputTokens: int(req.MaxOutputTokens)})
	if err != nil && ctx.Err() == nil && errors.Is(call.Err(), context.DeadlineExceeded) {
		// The call's own bound, not the daemon stopping: a failure.
		err = fmt.Errorf("%w: no reply within %s: %w", context.DeadlineExceeded, timeout, err)
	}
	m.model, m.usage, m.raw, m.rejections, m.detail = out.Model, out.Usage, out.Document, out.Rejections, out.RejectionDetail
	billed := func(err error) error {
		return &decisions.BilledError{Model: out.Model, Usage: decisionUsage(out.Usage), Err: err}
	}
	if err != nil {
		if out.Usage.IsZero() {
			return decisions.ModelResponse{}, err
		}
		return decisions.ModelResponse{}, billed(err)
	}
	document, err := agent.RedactTaskOutput(ctx, m.redactor, out.Document, &agent.Outcome{})
	if err != nil {
		return decisions.ModelResponse{}, billed(err)
	}
	return decisions.ModelResponse{JSON: document, Model: out.Model, Usage: decisionUsage(out.Usage)}, nil
}

// resolveBackend names the backend capture uses: the requested one, or for
// auto the one reviews would use, which generate.Select picks (the first of
// generate.AutoOrder that passes its check; never cursor-agent).
func resolveBackend(ctx context.Context, requested config.Backend, backends generate.Backends) (string, error) {
	if requested != config.BackendAuto && requested != "" {
		return string(requested), nil
	}
	selected, err := generate.Select(ctx, config.BackendAuto, "", backends, io.Discard)
	if err != nil {
		return "", err
	}
	return selected.Backend.Name(), nil
}

// decisionModel is the preferences' decision model, else the backend's
// default (DefaultDecisionModel). A model of another provider's family than
// the backend's, such as a Claude model with codex, is ignored.
func decisionModel(list *pricing.List, backend string, model *string) string {
	if model == nil || *model == "" {
		return DefaultDecisionModel(backend)
	}
	family := list.Normalize(*model, "").Family
	want := pricing.FamilyClaude
	if backend == agent.BackendCodex {
		want = pricing.FamilyGPT
	}
	if family != pricing.FamilyUnknown && family != want {
		return DefaultDecisionModel(backend)
	}
	return *model
}

// maxCalls is the most model calls one completion of the backend can make:
// claude may try its structured reply once more after the harness rejected
// it; codex runs one turn and the anthropic backend sends one request.
func maxCalls(backend string) int {
	if backend == agent.BackendClaude {
		return claude.CompleteMaxTurns
	}
	return 1
}

// Effective is the backend and model decision capture uses.
type Effective struct {
	// Backend is the backend reviews resolve to, empty when none passes its
	// check.
	Backend string `json:"backend"`
	// Undetermined is a backend whose checks did not answer in time, apart
	// from none being usable.
	Undetermined bool `json:"backend_undetermined"`
	// Model is the decision model; empty without a backend and with
	// cursor-agent, which cannot capture.
	Model string `json:"effective_model"`
	// Ignored is a chosen model (preferences.decisions.model) that capture
	// does not use with Backend.
	Ignored bool `json:"model_ignored"`
}

// effectiveTimeout bounds the backend checks EffectiveModel runs for auto, so
// a slow CLI never holds Settings or the setup.
var effectiveTimeout = 5 * time.Second

// EffectiveModel resolves the backend and decision model as a capture does
// (resolveModel), from the config file with the app's preferences over it; a
// repository's own backend can still differ. Backends zero builds them from
// the configuration and list nil is pricing.Active. With auto it runs the
// backends' checks, for at most effectiveTimeout. No usable backend leaves
// the Effective empty; checks that do not answer in time leave it empty but
// Undetermined. Either way the model is unknown and nothing is ignored.
func EffectiveModel(ctx context.Context, file config.Config, prefs repos.Preferences, backends generate.Backends, list *pricing.List) Effective {
	cfg := prefs.Resolve(file)
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	if list == nil {
		list = pricing.Active()
	}
	ctx, cancel := context.WithTimeout(ctx, effectiveTimeout)
	defer cancel()
	backend, model, err := resolveModel(ctx, cfg.Backend, backends, list, prefs.Decisions.Model)
	if err != nil {
		return Effective{Undetermined: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	}
	// cursor-agent uses no model at all, so it ignores none in particular.
	chosen := prefs.Decisions.Model
	return Effective{Backend: backend, Model: model, Ignored: chosen != nil && *chosen != "" && model != "" && *chosen != model}
}

// resolveModel names the backend capture uses (resolveBackend) and its
// decision model (decisionModel); cursor-agent, which cannot capture, has
// none.
func resolveModel(ctx context.Context, requested config.Backend, backends generate.Backends, list *pricing.List, chosen *string) (string, string, error) {
	backend, err := resolveBackend(ctx, requested, backends)
	if err != nil {
		return "", "", err
	}
	if backend == agent.BackendCursorAgent {
		return backend, "", nil
	}
	return backend, decisionModel(list, backend, chosen), nil
}

// caller makes the decision log's model calls, decision capture's and the
// feature log's (README.md, "Capture", steps 6 to 8): it resolves the
// backend and model, reserves the cost and records each call in the ledger.
type caller struct {
	store    *Store
	config   config.Config
	backends generate.Backends
	prices   *pricing.List
	redactor *bundle.Redactor
	log      *slog.Logger
	now      time.Time
	// gate reads the preferences and the repository's settings before each
	// feature stage call, and whether calls may be made; nil is on, with
	// prefs and settings.
	gate     func(context.Context) (repos.Preferences, repos.Settings, bool, error)
	prefs    repos.Preferences
	settings repos.Settings
	// build, when set, counts a first build's calls and spend.
	build *buildSpend
	// begun is called once an attempt is recorded as pending.
	begun func()
	// timeout bounds each call; zero is CallTimeout.
	timeout time.Duration
}

// buildSpend is a first build's run's calls and known spend, which are also
// added to its version's spend, with its calls whose cost is unknown
// counted on the version.
type buildSpend struct {
	version  int64
	run      float64
	calls    int
	unpriced int
}

// add counts a call that was made and its cost, nil when unknown. A spend
// the version could not record is logged; the call and its ledger row
// stand.
func (b *buildSpend) add(ctx context.Context, s *Store, log *slog.Logger, cost *float64) {
	b.calls++
	query, args := `UPDATE feature_version SET unpriced_calls=unpriced_calls+1 WHERE id=?`, []any{b.version}
	if cost == nil {
		b.unpriced++
	} else {
		b.run += *cost
		query, args = `UPDATE feature_version SET spent_usd=spent_usd+? WHERE id=?`, []any{*cost, b.version}
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		log.Warn("decision log: build spend not recorded", "error", err)
	}
}

// Reasons a feature stage call was not made, besides the skip reasons.
const (
	ReasonDisabled    = "disabled"
	ReasonPreferences = "preferences_unreadable"
)

// Reasons a build stopped because a fetch could not sign in to the remote
// (see gitlocal.AuthError).
const (
	ReasonGitAuthSSH   = "git_auth_ssh"
	ReasonGitAuthHTTPS = "git_auth_https"
)

// planned is a call's backend, model and reservation.
type planned struct {
	Backend, Model string
	Reserved       float64
	backends       generate.Backends
}

// plan resolves the backend and model for the preferences and the
// repository's settings, backend and model replacing them when set, and
// reserves a prompt of at most maxTokens and a reply of at most maxOutput.
// A call it may not make returns its skip reason, with what was resolved.
func (c caller) plan(ctx context.Context, prefs repos.Preferences, settings repos.Settings, backend, model string, maxTokens, maxOutput int64) (planned, string) {
	cfg := settings.Resolve(prefs.Resolve(c.config))
	requested := cfg.Backend
	if backend != "" {
		requested = config.Backend(backend)
	}
	p := planned{backends: c.backends}
	if p.backends.Claude == nil {
		p.backends = generate.DefaultBackendsFor(cfg)
	}
	var err error
	if p.Backend, p.Model, err = resolveModel(ctx, requested, p.backends, c.prices, prefs.Decisions.Model); err != nil {
		c.log.Info("decision log backend unavailable", "backend", requested, "error", err)
		return p, ReasonUnavailable
	}
	if model != "" {
		p.Model = model
	}
	if p.Backend == agent.BackendCursorAgent {
		return p, ReasonUnsupported
	}
	// Prices only report: a model the list does not price runs, reserved at 0.
	p.Reserved, _ = reserveFor(c.prices, p.Backend, p.Model, maxTokens, maxOutput)
	return p, ""
}

// begin selects the planned backend's completer and records the attempt as
// pending with its reservation. A backend that no longer passes its check,
// or cannot complete, is a skip and records nothing.
func (c caller) begin(ctx context.Context, p planned, a Attempt) (*completerModel, int64, string, error) {
	selected, err := generate.Select(ctx, config.Backend(p.Backend), p.Model, p.backends, io.Discard)
	if err != nil {
		c.log.Info("decision log backend unavailable", "backend", p.Backend, "error", err)
		return nil, 0, ReasonUnavailable, nil
	}
	completer, ok := selected.Backend.(agent.Completer)
	if !ok {
		return nil, 0, ReasonUnsupported, nil
	}
	a.Backend, a.Model, a.Reserved = p.Backend, p.Model, p.Reserved
	id, err := c.store.Begin(ctx, a)
	if err == nil && c.begun != nil {
		c.begun()
	}
	return &completerModel{completer: completer, redactor: c.redactor, attempt: a, timeout: c.timeout}, id, "", err
}

// finish settles the attempt once its call returned: abandoned when the
// call was cut short (cancelled), failed with callErr, else succeeded. It
// returns the state, the spend (nil when it cannot be priced, zero when no
// call was made) and the usage.
func (c caller) finish(ctx context.Context, id int64, m *completerModel, callErr error, cancelled bool) (string, *float64, decisions.Usage, error) {
	spent := cost(c.prices, m.model, m.usage)
	if !m.called {
		spent = new(0.0)
	}
	usage := decisionUsage(m.usage)
	state, reason := AttemptSucceeded, ""
	switch {
	case callErr != nil && cancelled:
		state, reason = AttemptAbandoned, "cancelled"
	case callErr != nil:
		state, reason = AttemptFailed, failureReason(callErr)
	}
	if m.rejections > 0 {
		// The harness rejected a reply against the schema: counted with the
		// attempt, the reason in the private log.
		c.log.Info("decision log: structured reply rejected", "stage", cmp.Or(m.attempt.Stage, StageCapture), "item", m.attempt.Item,
			"attempt", id, "rejections", m.rejections, "detail", m.detail)
	}
	return state, spent, usage, c.store.settle(ctx, id, state, reason, spent, usage, m.rejections)
}

// callResult is one feature stage call: its reply, or why none was made.
type callResult struct {
	JSON []byte
	// Skip is the reason no call was made (a backend reason).
	Skip string
	// Called is set once the backend was called, so a failure may have been
	// billed.
	Called bool
	Cost   *float64
}

// complete makes one feature stage call with plan, begin and finish; the
// attempt names its stage and item. An error is a failed call or a ledger
// write.
func (c caller) complete(ctx context.Context, backend, model string, a Attempt, req decisions.ModelRequest) (callResult, error) {
	// The gate is read again before every call.
	prefs, settings, on, err := c.prefs, c.settings, true, error(nil)
	if c.gate != nil {
		prefs, settings, on, err = c.gate(ctx)
	}
	switch {
	case err != nil:
		return callResult{Skip: ReasonPreferences}, err
	case !on:
		return callResult{Skip: ReasonDisabled}, nil
	}
	p, skip := c.plan(ctx, prefs, settings, backend, model, decisions.MaxRequestTokens(req), req.MaxOutputTokens)
	if skip != "" {
		return callResult{Skip: skip}, nil
	}
	m, id, skip, err := c.begin(ctx, p, a)
	if err != nil || skip != "" {
		return callResult{Skip: skip}, err
	}
	response, callErr := m.Complete(ctx, req)
	// The ledger outlives a cancelled call: it may be paid for.
	_, spent, _, err := c.finish(context.WithoutCancel(ctx), id, m, callErr, ctx.Err() != nil)
	result := callResult{JSON: response.JSON, Called: m.called, Cost: spent}
	if b := c.build; b != nil && m.called {
		b.add(context.WithoutCancel(ctx), c.store, c.log, spent)
	}
	if err != nil {
		// The reply stands: a ledger that could not be written is not the
		// call's failure.
		c.log.Warn("decision log: model call not settled in the ledger", "stage", a.Stage, "error", err)
	}
	return result, callErr
}
