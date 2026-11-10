package pricing

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb"
)

// Source says where the active price list came from: compiled in, fetched by
// Refresh in this process, or loaded by LoadPersisted from the list an earlier
// daemon run stored.
type Source struct {
	// Origin is "embedded", "fetched" or "cached".
	Origin  string
	Version string
	// FetchedAt is when Refresh installed the active list; zero for the
	// embedded and cached lists.
	FetchedAt time.Time
	// RefreshError is why the last Refresh failed; empty after a success or
	// before any attempt.
	RefreshError string
}

// Origins of the active price list.
const (
	OriginEmbedded = "embedded"
	OriginFetched  = "fetched"
	OriginCached   = "cached"
)

var refreshState struct {
	mu        sync.Mutex
	list      *List
	origin    string
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
		source.Origin, source.FetchedAt = refreshState.origin, refreshState.fetchedAt
	}
	source.RefreshError = refreshState.err
	return source
}

// noteRefresh records the outcome of one Refresh: the list it installed, if
// any, and its error. A list stays recorded when only storing it failed.
func noteRefresh(installed *List, err error) {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	if installed != nil {
		refreshState.list, refreshState.origin, refreshState.fetchedAt = installed, OriginFetched, time.Now()
	}
	refreshState.err = ""
	if err != nil {
		refreshState.err = err.Error()
	}
}

// noteCached records a stored list LoadPersisted installed.
func noteCached(installed *List) {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	refreshState.list, refreshState.origin, refreshState.fetchedAt = installed, OriginCached, time.Time{}
}

type persisted struct {
	List      []byte `json:"list"`
	Signature []byte `json:"signature"`
}

// LoadPersisted installs the list a previous Refresh stored in dir if its
// signature still verifies and it is newer than the active list. A missing
// file is not an error; any other failure leaves the active list unchanged.
func LoadPersisted(dir string) error { return loadPersisted(dir, PublishedKey()) }

func loadPersisted(dir string, key ed25519.PublicKey) error {
	raw, e := appdb.Read(filepath.Join(dir, "app.sqlite"), "cache", "pricing")
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if len(raw) > 4<<20 {
		return errors.New("persisted price list too large")
	}
	var stored persisted
	if e = json.Unmarshal(raw, &stored); e != nil {
		return e
	}
	list, e := verified(stored.List, stored.Signature, key)
	if e != nil {
		return e
	}
	if Newest(Active(), list) == list {
		Install(list)
		noteCached(list)
	}
	return nil
}

func verified(raw, signature []byte, key ed25519.PublicKey) (*List, error) {
	list, e := ParsePublished(raw, signature, key)
	if e != nil {
		return nil, e
	}
	if !list.Full() {
		return nil, errors.New("full price list required")
	}
	return list, nil
}

// Refresh downloads only public signed data, without credentials or redirects.
// A failure leaves the compiled or previously verified list active. When dir is
// not empty, a downloaded list that becomes active is stored there for
// LoadPersisted.
func Refresh(ctx context.Context, dir string) error {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("pricing redirect refused") }}
	return refresh(ctx, client, PublishedURL, PublishedKey(), dir)
}

func refresh(ctx context.Context, client *http.Client, published string, key ed25519.PublicKey, dir string) (err error) {
	var installed *List
	defer func() { noteRefresh(installed, err) }()
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
	raw, e := get(published)
	if e != nil {
		return e
	}
	signature, e := get(published + ".sig")
	if e != nil {
		return e
	}
	list, e := verified(raw, signature, key)
	if e != nil {
		return e
	}
	if Newest(Active(), list) != list {
		return nil
	}
	Install(list)
	installed = list
	if dir == "" {
		return nil
	}
	encoded, e := json.Marshal(persisted{List: raw, Signature: signature})
	if e != nil {
		return e
	}
	return appdb.Write(filepath.Join(dir, "app.sqlite"), "cache", "pricing", encoded)
}

// Cost reasons qualify an agent run's cost (AccountingCost). Every one but
// ReasonConfiguredModel makes a cost that is shown a lower bound: the run used
// at least that much. A cost is nil only with ReasonUsageNotReported or, when
// no attempt's model has rates, ReasonRatesUnknown.
const (
	ReasonUsageNotReported    = "usage_not_reported"
	ReasonUsageIncomplete     = "usage_incomplete"
	ReasonRatesUnknown        = "model_or_prompt_rates_unknown"
	ReasonSpeedNotReported    = "speed_not_reported"
	ReasonCacheTTLNotReported = "cache_ttl_not_reported"
	ReasonConfiguredModel     = "estimated_from_configured_model"
)

// lowerBoundReasons orders the lower-bound reasons by how much they can leave
// out; a run with several reports the first.
var lowerBoundReasons = []string{ReasonUsageIncomplete, ReasonRatesUnknown, ReasonSpeedNotReported, ReasonCacheTTLNotReported}

// OutcomeCost is what an outcome's usage costs at API prices, estimated as
// AccountingCost estimates; nil only when it reported no tokens or its model
// has no rates. Agent usage is already disjoint, including Codex: it must not
// apply Codex's raw-session subtraction.
func (l *List) OutcomeCost(model string, outcome agent.Outcome) *float64 {
	if outcome.Accounting != nil {
		cost, _ := l.AccountingCost(outcome.Accounting)
		return cost
	}
	if tokens(outcome.Usage) == 0 {
		return nil
	}
	cost, _, ok := l.estimate(model, outcome.Usage)
	if !ok {
		return nil
	}
	return &cost
}

// AccountingCost prices attempts separately so a repair using a different model
// cannot reprice the whole run at its final model's rate. It estimates rather
// than leave the cost unknown, because a rough figure is more use than none:
// an attempt with no tokens (Claude Code's <synthetic> error message) costs
// nothing, a call cut off or still running is priced from what it reported so
// far, and a missing speed, cache TTL or prompt tier takes the lowest rate it
// could have been billed at. The reason then names what was missing, and the
// cost is a lower bound. An attempt with no model, or one the list has no rates
// for, is left out of the sum. The cost is nil only when no attempt could be
// priced.
func (l *List) AccountingCost(a *agent.Accounting) (*float64, string) {
	if a == nil {
		return nil, ReasonUsageNotReported
	}
	reason := ""
	note := func(r string) {
		if i := slices.Index(lowerBoundReasons, r); i >= 0 && (reason == "" || i < slices.Index(lowerBoundReasons, reason)) {
			reason = r
		}
	}
	if a.Usage.Unreported {
		note(ReasonUsageIncomplete)
	}
	sum, priced, used, configured := 0.0, false, false, false
	for _, attempt := range a.Attempts {
		if attempt.Usage.Unreported {
			note(ReasonUsageIncomplete)
		}
		if tokens(attempt.Usage) == 0 {
			continue
		}
		used = true
		if attempt.Model == "" {
			note(ReasonRatesUnknown)
			continue
		}
		cost, why, ok := l.estimate(attempt.Model, attempt.Usage)
		if !ok {
			note(ReasonRatesUnknown)
			continue
		}
		note(why)
		sum, priced = sum+cost, true
		configured = configured || attempt.ModelSource == "configured"
	}
	switch {
	case !priced && used:
		return nil, ReasonRatesUnknown
	case !priced:
		return nil, ReasonUsageNotReported
	case reason == "" && configured:
		reason = ReasonConfiguredModel
	}
	return &sum, reason
}

// estimate prices one attempt's usage. why is empty when the list priced the
// usage as reported; otherwise it names what was missing or had no rate, and
// the cost is at the lowest rates the usage could have been billed at: standard
// speed, the 5-minute cache write and the base prompt tier. ok is false only
// for a model the list has no rates for.
func (l *List) estimate(model string, u agent.Usage) (cost float64, why string, ok bool) {
	usage := Usage{Vendor: VendorClaudeCode, InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheCreate, Speed: SpeedStandard}
	if u.Speed != nil {
		usage.Speed = Speed(*u.Speed)
	} else {
		why = ReasonSpeedNotReported
	}
	if u.CacheCreate1h != nil {
		usage.CacheWrite1hTokens = *u.CacheCreate1h
	} else if u.CacheCreate > 0 {
		why = cmp.Or(why, ReasonCacheTTLNotReported)
	}
	if u.MinPrompt != nil && u.MaxPrompt != nil {
		usage.PromptRangeKnown, usage.MinPromptTokens, usage.MaxPromptTokens = true, *u.MinPrompt, *u.MaxPrompt
	}
	normalized := l.Normalize(model, "")
	if value, priced := l.Cost(normalized, usage); priced {
		return value, why, true
	}
	usage.Speed = SpeedStandard
	usage.PromptRangeKnown, usage.MinPromptTokens, usage.MaxPromptTokens = true, 0, 0
	if value, priced := l.Cost(normalized, usage); priced {
		return value, ReasonRatesUnknown, true
	}
	usage.CacheWrite1hTokens = 0
	if value, priced := l.Cost(normalized, usage); priced {
		return value, ReasonRatesUnknown, true
	}
	return 0, ReasonRatesUnknown, false
}

func tokens(u agent.Usage) int64 { return u.Input + u.Output + u.CacheRead + u.CacheCreate }
