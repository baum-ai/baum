package pricing

// The core pricing files and baseline in this package are copied from
// the monorepo pricing package. Keep their rates, signature key and
// normalization tests synchronized when updating either module.

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
)

// Source says where the active price list came from. There is no on-disk
// cache: the list is the one compiled in or the last one Refresh fetched in
// this process.
type Source struct {
	// Origin is "embedded" or "fetched".
	Origin  string
	Version string
	// FetchedAt is when Refresh installed the active list; zero for the
	// embedded list.
	FetchedAt time.Time
	// RefreshError is why the last Refresh failed; empty after a success or
	// before any attempt.
	RefreshError string
}

// Origins of the active price list.
const (
	OriginEmbedded = "embedded"
	OriginFetched  = "fetched"
)

var refreshState struct {
	mu        sync.Mutex
	list      *List
	fetchedAt time.Time
	err       string
}

// CurrentSource describes the active price list and the last Refresh. It is
// safe to call while Refresh runs.
func CurrentSource() Source {
	active := Active()
	source := Source{Origin: OriginEmbedded, Version: active.Version()}
	if active != Embedded() {
		source.Origin = OriginFetched
	}
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	if refreshState.list != nil && refreshState.list == active {
		source.FetchedAt = refreshState.fetchedAt
	}
	source.RefreshError = refreshState.err
	return source
}

// noteRefresh records the outcome of one Refresh: the list it installed, if
// any, and its error.
func noteRefresh(installed *List, err error) {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	if err != nil {
		refreshState.err = err.Error()
		return
	}
	refreshState.err = ""
	if installed != nil {
		refreshState.list, refreshState.fetchedAt = installed, time.Now()
	}
}

// Refresh downloads only public signed data, without credentials or redirects.
// A failure leaves the compiled or previously verified list active.
func Refresh(ctx context.Context) error { return refresh(ctx, nil) }

// refresh is Refresh over transport; nil is the default transport.
func refresh(ctx context.Context, transport http.RoundTripper) (err error) {
	var installed *List
	defer func() { noteRefresh(installed, err) }()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("pricing redirect refused") }}
	get := func(url string) ([]byte, error) {
		r, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if e != nil {
			return nil, e
		}
		response, e := client.Do(r)
		if e != nil {
			return nil, e
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 200 {
			return nil, errors.New("pricing download unavailable")
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
		if len(raw) > 1<<20 {
			return nil, errors.New("pricing list too large")
		}
		return raw, e
	}
	raw, e := get(PublishedURL)
	if e != nil {
		return e
	}
	signature, e := get(PublishedURL + ".sig")
	if e != nil {
		return e
	}
	list, e := ParsePublished(raw, signature, PublishedKey())
	if e != nil {
		return e
	}
	if !list.Full() {
		return errors.New("full price list required")
	}
	if Newest(Active(), list) == list {
		Install(list)
		installed = list
	}
	return nil
}

// Estimate is a planning estimate from configured charged-token and call
// budgets, not a dollar ceiling. It reserves one budget-sized cache read per
// call, and uses the highest listed rate over all tiers and speeds.
func (l *List) Estimate(model string, maxTokens, maxCalls int) *float64 {
	if model == "" || maxTokens <= 0 || maxCalls <= 0 {
		return nil
	}
	m := l.Normalize(model, "")
	entry, ok := l.entry(m.Canonical)
	if !ok {
		return nil
	}
	charged, read := 0.0, 0.0
	for _, r := range []RateSet{entry.Base(), entry.Above.RateSet(), entry.Fast, entry.FastAbove.RateSet()} {
		charged = math.Max(charged, math.Max(math.Max(r.Input, r.Output), math.Max(r.CacheWrite, r.CacheWrite1h)))
		read = math.Max(read, r.CacheRead)
	}
	cost := float64(maxTokens) * (charged + float64(maxCalls)*read) / 1e6
	if math.IsInf(cost, 0) || math.IsNaN(cost) {
		return nil
	}
	return &cost
}

// OutcomeCost keeps unknown or ambiguous usage unknown. Agent usage is already
// disjoint, including Codex: it must not apply Codex's raw-session subtraction.
func (l *List) OutcomeCost(model string, outcome agent.Outcome) *float64 {
	if outcome.Accounting != nil {
		cost, _ := l.AccountingCost(outcome.Accounting)
		return cost
	}
	u := outcome.Usage
	if u.Unreported || u.IsZero() || u.Speed == nil || (u.CacheCreate > 0 && u.CacheCreate1h == nil) {
		return nil
	}
	usage := Usage{Vendor: VendorClaudeCode, InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheCreate, Speed: Speed(*u.Speed)}
	if u.CacheCreate1h != nil {
		usage.CacheWrite1hTokens = *u.CacheCreate1h
	}
	if u.MinPrompt != nil && u.MaxPrompt != nil {
		usage.PromptRangeKnown = true
		usage.MinPromptTokens = *u.MinPrompt
		usage.MaxPromptTokens = *u.MaxPrompt
	}
	value, ok := l.Cost(l.Normalize(model, ""), usage)
	if !ok {
		return nil
	}
	return &value
}

// AccountingCost prices attempts separately so a repair using a different model
// cannot reprice the whole run at its final model's rate.
func (l *List) AccountingCost(a *agent.Accounting) (*float64, string) {
	if a == nil || len(a.Attempts) == 0 {
		return nil, "usage_not_reported"
	}
	if a.Usage.Unreported {
		return nil, "usage_incomplete"
	}
	sum := 0.0
	for _, attempt := range a.Attempts {
		if attempt.ModelSource == "configured" {
			return nil, "actual_model_not_reported"
		}
		u := attempt.Usage
		if u.Unreported || u.IsZero() {
			return nil, "usage_not_reported"
		}
		if attempt.Model == "" {
			return nil, "model_not_reported"
		}
		if u.Speed == nil {
			return nil, "speed_not_reported"
		}
		if u.CacheCreate > 0 && u.CacheCreate1h == nil {
			return nil, "cache_ttl_not_reported"
		}
		cost := l.OutcomeCost(attempt.Model, agent.Outcome{Usage: u})
		if cost == nil {
			return nil, "model_or_prompt_rates_unknown"
		}
		sum += *cost
	}
	return &sum, ""
}
