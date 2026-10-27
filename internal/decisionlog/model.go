package decisionlog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
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
	// SessionDailyCap bounds one session's spend per day, in US dollars.
	SessionDailyCap = 0.25
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

// cost prices a call's reported usage, whose buckets are disjoint
// (agent.Usage); nil when it cannot be priced.
func cost(list *pricing.List, model string, u agent.Usage) *float64 {
	if u.Unreported || u.IsZero() || model == "" {
		return nil
	}
	usage := pricing.Usage{Vendor: pricing.VendorClaudeCode, InputTokens: u.Input, OutputTokens: u.Output,
		CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheCreate, SingleRequest: true}
	if u.CacheCreate1h != nil {
		usage.CacheWrite1hTokens = *u.CacheCreate1h
	}
	if u.Speed != nil {
		usage.Speed = pricing.Speed(*u.Speed)
	}
	if u.MinPrompt != nil && u.MaxPrompt != nil && *u.MinPrompt != *u.MaxPrompt {
		usage.SingleRequest = false
		usage.PromptRangeKnown, usage.MinPromptTokens, usage.MaxPromptTokens = true, *u.MinPrompt, *u.MaxPrompt
	}
	value, ok := list.Cost(list.Normalize(model, ""), usage)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
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
}

func (m *completerModel) Complete(ctx context.Context, req decisions.ModelRequest) (decisions.ModelResponse, error) {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		return decisions.ModelResponse{}, err
	}
	m.called, m.user = true, req.User
	out, err := m.completer.Complete(ctx, agent.CompleteRequest{System: req.System, User: req.User, Schema: schema, MaxOutputTokens: int(req.MaxOutputTokens)})
	m.model, m.usage, m.raw = out.Model, out.Usage, out.Document
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

// caller makes the decision log's model calls, decision capture's and the
// feature log's (README.md, "Capture", steps 6 to 8): it resolves the
// backend and model, reserves the cost, holds the daily cap, which every
// stage's spend counts against, and records each call in the ledger.
type caller struct {
	store    *Store
	config   config.Config
	backends generate.Backends
	prices   *pricing.List
	redactor *bundle.Redactor
	log      *slog.Logger
	now      time.Time
}

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
	if p.Backend, err = resolveBackend(ctx, requested, p.backends); err != nil {
		c.log.Info("decision log backend unavailable", "backend", requested, "error", err)
		return p, ReasonUnavailable
	}
	p.Model = decisionModel(c.prices, p.Backend, prefs.Decisions.Model)
	if model != "" {
		p.Model = model
	}
	if p.Backend == agent.BackendCursorAgent {
		return p, ReasonUnsupported
	}
	reserved, ok := reserveFor(c.prices, p.Backend, p.Model, maxTokens, maxOutput)
	if !ok {
		return p, ReasonUnpriced
	}
	p.Reserved = reserved
	return p, ""
}

// overDailyCap reports whether reserved would take the day's spend of every
// stage past preferences.decisions.daily_cap; there is none when unset.
func (c caller) overDailyCap(ctx context.Context, prefs repos.Preferences, reserved float64) (bool, error) {
	limit := prefs.Decisions.DailyCap
	if limit == nil {
		return false, nil
	}
	st, err := c.store.Stats(ctx, "", "", startOfDay(c.now))
	return st.AllSpent+reserved > *limit, err
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
	return &completerModel{completer: completer, redactor: c.redactor}, id, "", err
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
	return state, spent, usage, c.store.Settle(ctx, id, state, reason, spent, usage)
}

// callResult is one feature stage call: its reply, or why none was made.
type callResult struct {
	JSON []byte
	// Skip is the reason no call was made (a backend, price or cap reason).
	Skip string
	// Called is set once the backend was called, so a failure may have been
	// billed.
	Called bool
	Cost   *float64
}

// complete makes one feature stage call with plan, overDailyCap, begin and
// finish; the attempt names its stage and item. An error is a failed call
// or a ledger write.
func (c caller) complete(ctx context.Context, prefs repos.Preferences, settings repos.Settings, backend, model string, a Attempt, req decisions.ModelRequest) (callResult, error) {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		return callResult{}, err
	}
	maxTokens := int64(len(req.System)+len(req.User)+len(schema)) + decisions.FramingTokens
	p, skip := c.plan(ctx, prefs, settings, backend, model, maxTokens, req.MaxOutputTokens)
	if skip != "" {
		return callResult{Skip: skip}, nil
	}
	if over, err := c.overDailyCap(ctx, prefs, p.Reserved); err != nil || over {
		return callResult{Skip: ReasonDailyCap}, err
	}
	m, id, skip, err := c.begin(ctx, p, a)
	if err != nil || skip != "" {
		return callResult{Skip: skip}, err
	}
	response, callErr := m.Complete(ctx, req)
	// The ledger outlives a cancelled call: it may be paid for.
	_, spent, _, err := c.finish(context.WithoutCancel(ctx), id, m, callErr, ctx.Err() != nil)
	result := callResult{JSON: response.JSON, Called: m.called, Cost: spent}
	if callErr != nil {
		return result, callErr
	}
	return result, err
}
