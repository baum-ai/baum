package pricing

// The core pricing files and baseline in this package are copied from
// the monorepo pricing package. Keep their rates, signature key and
// normalization tests synchronized when updating either module.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
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

// persistedName holds the newest installed published list beside the job
// journal, with its signature, so queued jobs confirmed against it can resume.
const persistedName = "prices.signed.json"

type persisted struct {
	List      []byte `json:"list"`
	Signature []byte `json:"signature"`
}

// LoadPersisted installs the list a previous Refresh stored in dir if its
// signature still verifies and it is newer than the active list. A missing
// file is not an error; any other failure leaves the active list unchanged.
func LoadPersisted(dir string) error { return loadPersisted(dir, PublishedKey()) }

func loadPersisted(dir string, key ed25519.PublicKey) error {
	file, e := os.Open(filepath.Join(dir, persistedName))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer func() { _ = file.Close() }()
	raw, e := io.ReadAll(io.LimitReader(file, 4<<20+1))
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
	return writePrivate(filepath.Join(dir, persistedName), encoded)
}

func writePrivate(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".baum-prices-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
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
