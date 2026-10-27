package decisionlog

import (
	"context"
	"encoding/json"
	"io"
	"math"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
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

// reserve is the most a call with at most inputTokens of prompt and
// MaxOutputTokens of reply costs at the model's rates, as hosted extraction
// reserves; false when the list would not price it.
func reserve(list *pricing.List, backend, model string, inputTokens int64) (float64, bool) {
	v := vendor(backend)
	rates, ok := list.RatesFor(list.Normalize(model, v), pricing.Usage{Vendor: v, InputTokens: inputTokens, OutputTokens: MaxOutputTokens, SingleRequest: true})
	if !ok {
		return 0, false
	}
	return (float64(inputTokens)*rates.Input + float64(MaxOutputTokens)*rates.Output) / 1_000_000, true
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
	model     string
	usage     agent.Usage
}

func (m *completerModel) Complete(ctx context.Context, req decisions.ModelRequest) (decisions.ModelResponse, error) {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		return decisions.ModelResponse{}, err
	}
	m.called = true
	out, err := m.completer.Complete(ctx, agent.CompleteRequest{System: req.System, User: req.User, Schema: schema, MaxOutputTokens: int(req.MaxOutputTokens)})
	m.model, m.usage = out.Model, out.Usage
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
// default (DefaultDecisionModel).
func decisionModel(backend string, model *string) string {
	if model != nil && *model != "" {
		return *model
	}
	return DefaultDecisionModel(backend)
}
