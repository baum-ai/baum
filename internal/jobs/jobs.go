// Package jobs owns the persistent local generation queue. Only the authenticated
// UI instance opens it; model calls remain in process and inherit cancellation.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
)

type Item struct {
	Clone string `json:"clone"`
	PR    int    `json:"pr"`
}
type Prepared struct {
	ConfigDigest string `json:"config_digest"`
	PriceVersion string `json:"price_version"`
	Item
	Repository                    string   `json:"repository"`
	HeadSHA                       string   `json:"head_sha"`
	Backend                       string   `json:"backend"`
	Model                         string   `json:"model"`
	MaxTokens                     int      `json:"max_tokens,omitempty"`
	MaxCalls                      int      `json:"max_calls,omitempty"`
	EstimateUSD                   *float64 `json:"estimate_usd"`
	RequiresIsolationConfirmation bool     `json:"requires_isolation_confirmation"`
	BlockedReason                 string   `json:"blocked_reason,omitempty"`
}

// sameConfirmedInputs reports whether two confirmations authorize the same
// run: clone, head, backend, model, budgets and caps, isolation, price list
// version and estimate. Only then is a resubmission idempotent.
func (p Prepared) sameConfirmedInputs(o Prepared) bool {
	if (p.EstimateUSD == nil) != (o.EstimateUSD == nil) || (p.EstimateUSD != nil && *p.EstimateUSD != *o.EstimateUSD) {
		return false
	}
	p.EstimateUSD, o.EstimateUSD = nil, nil
	return p == o
}

type Estimate struct {
	ID           string     `json:"id"`
	Items        []Prepared `json:"items"`
	EstimateUSD  *float64   `json:"estimate_usd"`
	PriceVersion string     `json:"price_version"`
	ExpiresAt    time.Time  `json:"expires_at"`
}
type Job struct {
	Accounting *agent.Accounting `json:"accounting,omitempty"`
	CostReason string            `json:"cost_reason,omitempty"`
	ID         string            `json:"id"`
	Prepared
	State         string    `json:"state"`
	Phase         string    `json:"phase"`
	Calls         int       `json:"calls"`
	Tokens        int       `json:"tokens"`
	Repairs       int       `json:"repairs"`
	ActualCostUSD *float64  `json:"actual_cost_usd"`
	Reason        string    `json:"reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Progress struct {
	Accounting             *agent.Accounting
	CostReason             string
	Phase                  string
	Calls, Tokens, Repairs int
}
type Executor interface {
	Prepare(context.Context, Item) (Prepared, string, error)
	Execute(context.Context, Prepared, func(Progress)) (*float64, error)
}
type Queue struct {
	mu        sync.Mutex
	path      string
	executor  Executor
	jobs      []Job
	estimates map[string]Estimate
	cancels   map[string]context.CancelFunc
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	changed   func(Job)
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

// Directory is the persistent state directory holding the default journal:
// $XDG_STATE_HOME/baum, or ~/.local/state/baum.
func Directory() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "baum"), nil
}

// DefaultPath is the queue file Open uses for an empty path: jobs.json in
// Directory.
func DefaultPath() (string, error) {
	dir, e := Directory()
	if e != nil {
		return "", e
	}
	return filepath.Join(dir, "jobs.json"), nil
}

func Open(path string, executor Executor, concurrency int, changed func(Job)) (*Queue, error) {
	if executor == nil || concurrency < 1 || concurrency > 16 {
		return nil, errors.New("invalid runner configuration")
	}
	if path == "" {
		var e error
		if path, e = DefaultPath(); e != nil {
			return nil, e
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("job state directory must be private (0700)")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{path: path, executor: executor, estimates: map[string]Estimate{}, cancels: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, changed: changed}
	raw, e := os.ReadFile(path)
	if e == nil {
		if len(raw) > 32<<20 {
			cancel()
			return nil, errors.New("job history too large")
		}
		if e = json.Unmarshal(raw, &q.jobs); e != nil {
			cancel()
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		cancel()
		return nil, e
	}
	for i := range q.jobs {
		if q.jobs[i].State == "running" {
			if q.jobs[i].Accounting != nil {
				q.jobs[i].Accounting.Usage.Unreported = true
				q.jobs[i].CostReason = "usage_incomplete"
			}
			q.jobs[i].State = "interrupted"
			q.jobs[i].Reason = "interrupted"
			q.jobs[i].Phase = "finished"
			q.jobs[i].UpdatedAt = time.Now().UTC()
		}
	}
	if e = q.save(); e != nil {
		cancel()
		return nil, e
	}
	for range concurrency {
		q.wg.Add(1)
		go q.worker()
	}
	q.signal()
	return q, nil
}
func (q *Queue) save() error {
	raw, e := json.Marshal(q.jobs)
	if len(raw) > 32<<20 {
		return errors.New("job history full")
	}
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(q.path), 0700); e != nil {
		return e
	}
	return generate.WriteFileAtomic(q.path, raw)
}
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (q *Queue) Close() { q.cancel(); q.wg.Wait() }
func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, len(q.jobs))
	copy(out, q.jobs)
	for i := range out {
		out[i].Accounting = out[i].Accounting.Clone()
	}
	return out
}
func (q *Queue) Estimate(ctx context.Context, items []Item, mode string) (Estimate, error) {
	if len(items) < 1 || len(items) > 50 || (mode != "single" && mode != "batch") || (mode == "single" && len(items) != 1) {
		return Estimate{}, errors.New("invalid generation selection")
	}
	estimate := Estimate{ID: id(), Items: []Prepared{}, ExpiresAt: time.Now().Add(5 * time.Minute).UTC()}
	total := 0.0
	known := true
	seen := map[string]bool{}
	for _, item := range items {
		p, version, e := q.executor.Prepare(ctx, item)
		if e != nil {
			return Estimate{}, e
		}
		key := fmt.Sprintf("%s#%d", p.Repository, p.PR)
		if seen[key] {
			continue
		}
		seen[key] = true
		if mode == "batch" && p.RequiresIsolationConfirmation {
			p.BlockedReason = "isolation_confirmation_required"
		}
		if p.BlockedReason == "unknown_model_price" {
			known = false
		}
		if p.BlockedReason == "" {
			if p.EstimateUSD == nil {
				known = false
				p.BlockedReason = "unknown_model_price"
			} else {
				total += *p.EstimateUSD
			}
		}
		estimate.Items = append(estimate.Items, p)
		if estimate.PriceVersion == "" {
			estimate.PriceVersion = version
		} else if estimate.PriceVersion != version {
			return Estimate{}, errors.New("price list changed; estimate again")
		}
	}
	if known {
		estimate.EstimateUSD = &total
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, v := range q.estimates {
		if time.Now().After(v.ExpiresAt) {
			delete(q.estimates, k)
		}
	}
	if len(q.estimates) >= 128 {
		return Estimate{}, errors.New("too many pending estimates")
	}
	q.estimates[estimate.ID] = estimate
	return estimate, nil
}
func (q *Queue) Submit(ctx context.Context, estimateID string, confirmed, isolation bool) ([]Job, error) {
	q.mu.Lock()
	e, ok := q.estimates[estimateID]
	q.mu.Unlock()
	if !ok || time.Now().After(e.ExpiresAt) || !confirmed {
		return nil, errors.New("a current confirmed estimate is required")
	}
	prepared := []Prepared{}
	for _, p := range e.Items {
		if p.BlockedReason != "" {
			continue
		}
		if p.RequiresIsolationConfirmation && !isolation {
			return nil, errors.New("separate isolation confirmation is required")
		}
		current, version, err := q.executor.Prepare(ctx, p.Item)
		if err != nil {
			return nil, err
		}
		if current.ConfigDigest != p.ConfigDigest || current.HeadSHA != p.HeadSHA || current.Backend != p.Backend || current.Model != p.Model || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation || current.BlockedReason != "" || version != e.PriceVersion || current.EstimateUSD == nil || p.EstimateUSD == nil || *current.EstimateUSD != *p.EstimateUSD {
			return nil, errors.New("generation inputs changed; estimate again")
		}
		prepared = append(prepared, current)
	}
	if len(prepared) == 0 {
		return nil, errors.New("no eligible pull requests")
	}
	q.mu.Lock()
	var notifications []Job
	defer func() {
		q.mu.Unlock()
		for _, j := range notifications {
			q.notify(j)
		}
	}()
	if _, ok := q.estimates[estimateID]; !ok || time.Now().After(e.ExpiresAt) || q.ctx.Err() != nil {
		return nil, errors.New("estimate expired or already submitted")
	}
	before := append([]Job(nil), q.jobs...)
	out := []Job{}
	changes := []Job{}
	for _, p := range prepared {
		existing := -1
		for i, j := range q.jobs {
			if j.Repository == p.Repository && j.PR == p.PR && (j.State == "queued" || j.State == "running") {
				existing = i
				break
			}
		}
		if existing >= 0 {
			j := &q.jobs[existing]
			if j.State == "running" {
				q.jobs = before
				return nil, errors.New("a job is already running for this pull request")
			}
			if j.sameConfirmedInputs(p) {
				out = append(out, *j)
				continue
			}
			j.State = "cancelled"
			j.Reason = "superseded"
			j.Phase = "finished"
			j.UpdatedAt = time.Now().UTC()
			changes = append(changes, *j)
		}
		now := time.Now().UTC()
		j := Job{ID: id(), Prepared: p, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}
		q.jobs = append(q.jobs, j)
		changes = append(changes, j)
		out = append(out, j)
	}
	if err := q.save(); err != nil {
		q.jobs = before
		return nil, err
	}
	notifications = changes
	delete(q.estimates, estimateID)
	q.signal()
	return out, nil
}
func (q *Queue) Cancel(jobID string) (Job, error) {
	q.mu.Lock()
	var notification *Job
	defer func() {
		q.mu.Unlock()
		if notification != nil {
			q.notify(*notification)
		}
	}()
	for i := range q.jobs {
		j := &q.jobs[i]
		if j.ID != jobID {
			continue
		}
		if j.State == "running" {
			if cancel := q.cancels[j.ID]; cancel != nil {
				cancel()
			}
			result := *j
			result.Accounting = result.Accounting.Clone()
			return result, nil
		}
		if j.State == "queued" {
			old := *j
			j.State = "cancelled"
			j.Phase = "finished"
			j.UpdatedAt = time.Now().UTC()
			if e := q.save(); e != nil {
				*j = old
				return Job{}, e
			}
			copy := *j
			notification = &copy
		}
		result := *j
		result.Accounting = result.Accounting.Clone()
		return result, nil
	}
	return Job{}, os.ErrNotExist
}
func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		for {
			q.mu.Lock()
			if q.ctx.Err() != nil {
				q.mu.Unlock()
				return
			}
			i := -1
			for n, j := range q.jobs {
				if j.State == "queued" {
					i = n
					break
				}
			}
			if i < 0 {
				q.mu.Unlock()
				break
			}
			old := q.jobs[i]
			q.jobs[i].State = "running"
			q.jobs[i].Phase = "preparing"
			q.jobs[i].UpdatedAt = time.Now().UTC()
			if e := q.save(); e != nil {
				q.jobs[i] = old
				q.mu.Unlock()
				break
			}
			j := q.jobs[i]
			ctx, cancel := context.WithCancel(q.ctx)
			q.cancels[j.ID] = cancel
			q.mu.Unlock()
			q.signal()
			q.notify(j)
			cost, err := q.executor.Execute(ctx, j.Prepared, func(p Progress) {
				q.mu.Lock()
				q.jobs[i].Phase = p.Phase
				q.jobs[i].Calls = p.Calls
				q.jobs[i].Tokens = p.Tokens
				q.jobs[i].Repairs = p.Repairs
				q.jobs[i].Accounting = p.Accounting.Clone()
				q.jobs[i].CostReason = p.CostReason
				q.jobs[i].UpdatedAt = time.Now().UTC()
				// Save every reported usage update before publishing it. A crash
				// may lose in-flight provider usage, never the reports we received.
				if e := q.save(); e != nil {
					cancel()
					q.mu.Unlock()
					return
				}
				current := q.jobs[i]
				current.Accounting = current.Accounting.Clone()
				q.mu.Unlock()
				q.notify(current)
			})
			q.mu.Lock()
			current := &q.jobs[i]
			current.ActualCostUSD = cost
			if cost == nil && current.CostReason == "" {
				current.CostReason = "usage_not_reported"
			}
			current.Phase = "finished"
			current.UpdatedAt = time.Now().UTC()
			current.State = "completed"
			if err != nil {
				current.State = "failed"
				current.Reason = string(generate.CategoryOf(err))
				if current.Reason == "" {
					current.Reason = "internal"
				}
			}
			if err != nil && ctx.Err() != nil {
				current.State = "cancelled"
				current.Reason = "interrupted"
				if q.ctx.Err() != nil {
					current.State = "interrupted"
				}
			}
			cancel()
			delete(q.cancels, j.ID)
			saveErr := q.save()
			result := *current
			q.mu.Unlock()
			q.notify(result)
			if saveErr != nil {
				return
			}
		}
	}
}
func (q *Queue) notify(j Job) {
	if q.changed != nil {
		j.Accounting = j.Accounting.Clone()
		q.changed(j)
	}
}
