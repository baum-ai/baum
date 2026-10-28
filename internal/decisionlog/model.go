package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	v := vendor(backend)
	rates, ok := list.RatesFor(list.Normalize(model, v), pricing.Usage{Vendor: v, InputTokens: inputTokens, OutputTokens: MaxOutputTokens, SingleRequest: true})
	if !ok {
		return 0, false
	}
	reply := float64(MaxOutputTokens) * rates.Output
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
