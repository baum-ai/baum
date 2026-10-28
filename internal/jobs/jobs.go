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
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/filelock"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/privatefs"
)

type Item struct {
	Clone string `json:"clone"`
	PR    int    `json:"pr"`
	// Group names a review group instead of a pull request (PR is then 0):
	// the job generates the group's combined review. See
	// internal/reviewgroups.
	Group string `json:"group,omitempty"`
	// Kind is KindFix for a fix, empty for a review.
	Kind string `json:"kind,omitempty"`
}

// KindFix is the Item.Kind of a fix: an agent with write access to a
// temporary checkout of the pull request's branch resolves its merge
// conflicts, failing checks or unresolved review comments, and baum pushes
// the result and replies on the review threads it addressed (internal/fix). A
// fix is never automatic and runs only from a confirmed estimate.
const KindFix = "fix"

// KindFeatureBuild is the Item.Kind of a repository's first feature build:
// its pull request history placed in features (decisionlog's Build). Clone
// names the repository's clone and PR is 0. It runs only from a confirmed
// estimate, capped at the estimate's cap (what earlier runs spent and the
// estimate times 1.25, never less than the largest remaining call); one
// stopped at the cap, interrupted or failed is resumed by confirming it
// again.
const KindFeatureBuild = "feature_build"

// FeatureBuildEstimate is what a feature build would do and cost.
type FeatureBuildEstimate struct {
	Backend, Model string
	// PullRequests are those left to place.
	PullRequests int
	// DiscoveryUSD, when not zero, says the build runs feature discovery
	// first, besides EstimateUSD, with the token budget DiscoveryUSD buys at
	// the review model's base price (cache reads not counted: not a cap).
	DiscoveryUSD float64
	// EstimateUSD is the cost of what is left, nil when it cannot be
	// priced.
	EstimateUSD *float64
	// Reason is why it cannot run now.
	Reason string
}

// ReasonUnpricedCalls is the cost reason of a feature build some of whose
// calls could not be priced: its recorded cost is a lower bound.
const ReasonUnpricedCalls = "unpriced_calls"

// FeatureBuilder estimates and runs feature builds.
type FeatureBuilder interface {
	EstimateFeatureBuild(ctx context.Context, repository, dir string) (FeatureBuildEstimate, error)
	// BuildFeatures runs a build and returns the run's spend; a build that
	// stops before its version is current returns a *StoppedError.
	BuildFeatures(ctx context.Context, repository, dir string, progress func(Progress)) (float64, error)
}

// StoppedError is a feature build that stopped before it finished, for
// Reason (an unavailable backend or price, or "failed"); the
// job fails with that reason.
type StoppedError struct {
	Reason string
	// Err is the error that stopped it, if one did.
	Err error
}

func (e *StoppedError) Error() string {
	message := "the feature build stopped (" + e.Reason + ")"
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message + "; confirm it again to resume"
}

func (e *StoppedError) Unwrap() error { return e.Err }

// FixInputs are what a fix job was confirmed for: what it fixes, the branch
// it pushes to and the base branch it merges.
type FixInputs struct {
	Conflicts bool `json:"conflicts,omitempty"`
	Checks    bool `json:"checks,omitempty"`
	// Threads, when the fix addresses review comments, identifies the
	// threads and their comments (fix.Digest): a reply or an edit on GitHub
	// after the confirmation stops the fix.
	Threads string `json:"threads,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
}

// FixThread is a review thread a fix addresses, as the confirmation and the
// job show it. Author and Excerpt, the first line of the first comment, are
// the reviewer's.
type FixThread struct {
	Ref      string `json:"ref"`
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Outdated bool   `json:"outdated,omitempty"`
	Author   string `json:"author,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
}

// ErrFixNeedsConfirmation refuses a fix asked for without a confirmed
// estimate.
var ErrFixNeedsConfirmation = errors.New("a fix runs only from a confirmed estimate")

type Prepared struct {
	ConfigDigest string `json:"config_digest"`
	PriceVersion string `json:"price_version"`
	Item
	Repository  string   `json:"repository"`
	HeadSHA     string   `json:"head_sha"`
	Backend     string   `json:"backend"`
	Model       string   `json:"model"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	MaxCalls    int      `json:"max_calls,omitempty"`
	EstimateUSD *float64 `json:"estimate_usd"`
	// EstimateReason, when set, is why EstimateUSD is nil on purpose: the
	// backend is billed outside the price list (pricing.ReasonBilledByCursor).
	// Such an item runs only after its own acknowledgement.
	EstimateReason                string `json:"estimate_reason,omitempty"`
	RequiresIsolationConfirmation bool   `json:"requires_isolation_confirmation"`
	BlockedReason                 string `json:"blocked_reason,omitempty"`
	// Draft: GitHub reports the PR as a draft. Automatic jobs skip drafts;
	// a confirmed run does not.
	Draft bool `json:"draft,omitempty"`
	// GroupName is a group job's group name when it was prepared, and
	// Members its members' numbers and heads ("7@<sha>,9@<sha>"), which a
	// confirmation covers like a pull request's head.
	GroupName string `json:"group_name,omitempty"`
	Members   string `json:"members,omitempty"`
	// RefuseDraft is set by the queue, never stored, on the inputs of an
	// automatic job it executes: the executor refuses the run with
	// NotAutomaticError{ReasonDraft} if GitHub now reports a draft.
	RefuseDraft bool `json:"-"`
	// Fix is what a fix job (KindFix) does.
	Fix FixInputs `json:"fix,omitzero"`
	// FixThreads are the review threads a fix addresses (Fix.Threads).
	// FixThreadsCapped says there were more than fix.MaxThreads, and
	// FixThreadsUntrusted counts the threads left out because an account
	// without access to the repository started them.
	FixThreads          []FixThread `json:"fix_threads,omitempty"`
	FixThreadsCapped    bool        `json:"fix_threads_capped,omitempty"`
	FixThreadsUntrusted int         `json:"fix_threads_untrusted,omitempty"`
	// BuildPullRequests are a feature build's (KindFeatureBuild) pull
	// requests left to place, and BuildDiscoveryUSD, when not zero, the
	// dollars whose worth of tokens (at the review model's base price) the
	// feature discovery it runs first may use besides EstimateUSD.
	BuildPullRequests int     `json:"build_pull_requests,omitempty"`
	BuildDiscoveryUSD float64 `json:"build_discovery_usd,omitempty"`
}

// sameConfirmedInputs reports whether two confirmations authorize the same
// run: clone, head, backend, model, budgets and caps, isolation, price list
// version and estimate. Only then is a resubmission idempotent.
func (p Prepared) sameConfirmedInputs(o Prepared) bool {
	if (p.EstimateUSD == nil) != (o.EstimateUSD == nil) || (p.EstimateUSD != nil && *p.EstimateUSD != *o.EstimateUSD) {
		return false
	}
	p.EstimateUSD, o.EstimateUSD = nil, nil
	return reflect.DeepEqual(p, o)
}

// sameEstimate reports whether a fresh preparation carries the confirmed
// estimate: the same amount, or the same reason it has none.
func sameEstimate(current, confirmed Prepared) bool {
	if current.EstimateUSD == nil || confirmed.EstimateUSD == nil {
		return current.EstimateUSD == nil && confirmed.EstimateUSD == nil && current.EstimateReason == confirmed.EstimateReason
	}
	return *current.EstimateUSD == *confirmed.EstimateUSD
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
	// Automatic marks a job the daemon's scheduler queued without a person
	// confirming it; Trigger says why (TriggerNewHead or
	// TriggerReviewRequested).
	Automatic     bool     `json:"automatic,omitempty"`
	Trigger       string   `json:"trigger,omitempty"`
	State         string   `json:"state"`
	Phase         string   `json:"phase"`
	Calls         int      `json:"calls"`
	Tokens        int      `json:"tokens"`
	Repairs       int      `json:"repairs"`
	ActualCostUSD *float64 `json:"actual_cost_usd"`
	Reason        string   `json:"reason,omitempty"`
	// Detail is the failure in words, from a generation error, whose text is
	// redacted before it is built, or from a fix; other errors leave it
	// empty.
	Detail string `json:"detail,omitempty"`
	// Commit is the commit a fix pushed, CommitURL where GitHub shows it, and
	// Summary the agent's account of the fix, with secrets masked.
	Commit    string `json:"commit,omitempty"`
	CommitURL string `json:"commit_url,omitempty"`
	Summary   string `json:"summary,omitempty"`
	// ThreadOutcomes is what became of each review thread a fix addressed
	// (FixThreads), once its push succeeded.
	ThreadOutcomes []fix.ThreadOutcome `json:"thread_outcomes,omitempty"`
	// HasTranscript says a fix kept a transcript of its agent's run, which
	// GET /api/jobs/{id}/transcript serves.
	HasTranscript bool      `json:"has_transcript,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Progress struct {
	Accounting             *agent.Accounting
	CostReason             string
	Phase                  string
	Calls, Tokens, Repairs int
	// Commit, CommitURL, Summary and Threads, when set, are a fix's (see
	// Job).
	Commit, CommitURL, Summary string
	Threads                    []fix.ThreadOutcome
	// HasTranscript is set once a fix's transcript file exists.
	HasTranscript bool
}
type Executor interface {
	Prepare(context.Context, Item) (Prepared, string, error)
	Execute(context.Context, Prepared, func(Progress)) (*float64, error)
}
type Queue struct {
	mu   sync.Mutex
	path string
	// transcripts is where fix jobs keep their transcripts, beside path.
	transcripts string
	executor    Executor
	jobs        []Job
	estimates   map[string]Estimate
	cancels     map[string]context.CancelFunc
	wake        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	changed     func(Job)
	// clock, when set, stamps automatic jobs' CreatedAt; see SetClock.
	clock func() time.Time
	// lock keeps every other process from opening this journal.
	lock        *filelock.Lock
	releaseOnce sync.Once
	// draining refuses new jobs while the daemon restarts.
	draining bool
	// gate, when set, answers whether an automatic job may run now (and
	// under which daily cap); see SetAutomaticGate.
	gate func(Job) Admission
}

// ErrRestarting refuses a job while the daemon restarts as a newer version.
// Estimate and confirm again in a moment: the new daemon takes the job.
var ErrRestarting = errors.New("the baum daemon is restarting as a newer version; estimate and confirm again in a moment")

// lockWait is how long Open waits for a predecessor process to let go of the
// journal: one whose shutdown timed out exits right after.
const lockWait = 10 * time.Second

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
	// The lock file takes its permissions from the directory: on Windows only
	// a protected directory gives it the current-user DACL filelock requires.
	if err := privatefs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("job state directory: %w", err)
	}
	// One process owns a journal: two queues on one file would run confirmed
	// jobs twice and overwrite each other's records.
	lock, err := filelock.Wait(context.Background(), path+".lock", lockWait)
	if errors.Is(err, filelock.ErrLocked) {
		return nil, errors.New("another baum process is running this job queue")
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{path: path, transcripts: filepath.Join(filepath.Dir(path), "transcripts"), executor: executor, estimates: map[string]Estimate{}, cancels: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, changed: changed, lock: lock}
	opened := false
	defer func() {
		if !opened {
			_ = lock.Release()
		}
	}()
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
	opened = true
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
func (q *Queue) Close() { _ = q.Shutdown(context.Background()) }

// Shutdown cancels running jobs, which are recorded as interrupted, and waits
// for the workers until ctx ends. A job whose backend has not returned by then
// stays running in the journal, and the next Open records it as interrupted.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.cancel()
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		// Workers have saved their last records; another process may open
		// the journal now. A worker still running at the deadline keeps the
		// lock until this process exits.
		q.releaseOnce.Do(func() { _ = q.lock.Release() })
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Idle reports whether no job is queued or running.
func (q *Queue) Idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.idle()
}

func (q *Queue) idle() bool {
	for _, j := range q.jobs {
		if j.State == "queued" || j.State == "running" {
			return false
		}
	}
	return true
}

// Drain refuses new jobs with ErrRestarting and reports true, if no job is
// queued or running; otherwise it changes nothing and reports false. Submit
// checks under the same mutex, so no job is accepted after a true answer.
func (q *Queue) Drain() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.idle() {
		return false
	}
	q.draining = true
	return true
}

// Resume accepts jobs again after Drain.
func (q *Queue) Resume() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.draining = false
}
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
	return q.estimate(ctx, items, mode, false)
}

// estimate prepares items. In a batch, a fork or an author without known
// write access is left out unless isolation was already confirmed for it.
func (q *Queue) estimate(ctx context.Context, items []Item, mode string, isolation bool) (Estimate, error) {
	if len(items) < 1 || len(items) > 50 || (mode != "single" && mode != "batch") || (mode == "single" && len(items) != 1) {
		return Estimate{}, errors.New("invalid generation selection")
	}
	// A fix or a feature build is confirmed on its own.
	if items[0].Kind != "" && (mode != "single" || (items[0].Kind != KindFix && items[0].Kind != KindFeatureBuild)) {
		return Estimate{}, errors.New("invalid generation selection")
	}
	for _, item := range items[1:] {
		if item.Kind != "" {
			return Estimate{}, errors.New("invalid generation selection")
		}
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
		if p.Group != "" {
			key = "group:" + p.Group
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if mode == "batch" && p.RequiresIsolationConfirmation && !isolation {
			p.BlockedReason = "isolation_confirmation_required"
		}
		if p.BlockedReason == "unknown_model_price" {
			known = false
		}
		if p.BlockedReason == "" {
			if p.EstimateUSD == nil {
				// Billed elsewhere or not in the price list: the total is
				// unknown, and the item runs anyway.
				known = false
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

// Confirmation is what a person confirmed for an estimate: the estimate
// itself, running an agent on a fork or an author without known write access,
// and running items baum cannot estimate because another service bills them.
type Confirmation struct {
	Estimate, Isolation bool
}

// Submit is SubmitConfirmed with its two confirmations.
func (q *Queue) Submit(ctx context.Context, estimateID string, confirmed, isolation bool) ([]Job, error) {
	return q.SubmitConfirmed(ctx, estimateID, Confirmation{Estimate: confirmed, Isolation: isolation})
}

// ErrNeedsClone: the item names a clone that is not registered or not
// available. A picked repository without a clone is discovered but never
// generated; its reviews wait for a clone. Over HTTP it is the code
// ReasonNeedsClone; a queued job it stops fails with that reason.
var ErrNeedsClone = errors.New("a local clone of the repository is required")

// ReasonNeedsClone is the machine-readable code of ErrNeedsClone.
const ReasonNeedsClone = "needs_clone"

// ErrIsolationRequired is a direct generation that includes a fork or an
// author without known write access, which needs its own confirmation.
var ErrIsolationRequired = errors.New("isolation confirmation required")

// Generate prepares and queues items in one step: the user asked for the
// reviews, and only a fork or an author without known write access needs
// their confirmation (isolation) first.
func (q *Queue) Generate(ctx context.Context, items []Item, mode string, isolation bool) ([]Job, error) {
	for _, item := range items {
		if item.Kind != "" {
			return nil, ErrFixNeedsConfirmation
		}
	}
	e, err := q.estimate(ctx, items, mode, isolation)
	if err != nil {
		return nil, err
	}
	for _, p := range e.Items {
		if p.RequiresIsolationConfirmation && !isolation {
			return nil, ErrIsolationRequired
		}
	}
	return q.SubmitConfirmed(ctx, e.ID, Confirmation{Estimate: true, Isolation: isolation})
}

// SubmitConfirmed queues the unblocked items of a current estimate. An item
// with an EstimateReason needs Unpriced, as one needing isolation
// confirmation needs Isolation.
func (q *Queue) SubmitConfirmed(ctx context.Context, estimateID string, c Confirmation) ([]Job, error) {
	confirmed, isolation := c.Estimate, c.Isolation
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
		if current.ConfigDigest != p.ConfigDigest || current.HeadSHA != p.HeadSHA || current.Members != p.Members || current.Backend != p.Backend || current.Model != p.Model || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation || current.BlockedReason != "" || version != e.PriceVersion || !sameEstimate(current, p) || current.Fix != p.Fix {
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
	if q.draining {
		return nil, ErrRestarting
	}
	if _, ok := q.estimates[estimateID]; !ok || time.Now().After(e.ExpiresAt) || q.ctx.Err() != nil {
		return nil, errors.New("estimate expired or already submitted")
	}
	before := append([]Job(nil), q.jobs...)
	out := []Job{}
	changes := []Job{}
	for _, p := range prepared {
		existing := -1
		for i, j := range q.jobs {
			// A review and a fix of one pull request are separate jobs.
			if j.Repository == p.Repository && j.PR == p.PR && j.Group == p.Group && j.Kind == p.Kind && (j.State == "queued" || j.State == "running") {
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
				// Without a gate an automatic job waits: the daemon installs it
				// right after Open, and a journaled job must not start first.
				if j.State == "queued" && (!j.Automatic || q.gate != nil) {
					i = n
					break
				}
			}
			if i < 0 {
				q.mu.Unlock()
				break
			}
			// An automatic job turned off while it waited never starts.
			if refused, ok := q.refuseAutomatic(i); ok {
				q.mu.Unlock()
				q.notify(refused)
				continue
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
			ctx, cancel := context.WithCancel(context.WithValue(q.ctx, transcriptTargetKey{}, transcriptTarget{q.transcripts, j.ID}))
			q.cancels[j.ID] = cancel
			q.mu.Unlock()
			q.signal()
			q.notify(j)
			inputs := j.Prepared
			inputs.RefuseDraft = j.Automatic
			cost, err := q.executor.Execute(ctx, inputs, func(p Progress) {
				q.mu.Lock()
				q.jobs[i].Phase = p.Phase
				q.jobs[i].Calls = p.Calls
				q.jobs[i].Tokens = p.Tokens
				q.jobs[i].Repairs = p.Repairs
				q.jobs[i].Accounting = p.Accounting.Clone()
				q.jobs[i].CostReason = p.CostReason
				if p.Commit != "" {
					q.jobs[i].Commit, q.jobs[i].CommitURL = p.Commit, p.CommitURL
				}
				if p.Summary != "" {
					q.jobs[i].Summary = p.Summary
				}
				if p.Threads != nil {
					q.jobs[i].ThreadOutcomes = p.Threads
				}
				if p.HasTranscript {
					q.jobs[i].HasTranscript = true
				}
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
			var refused *NotAutomaticError
			var fixErr *fix.Error
			var stopped *StoppedError
			switch {
			case err != nil && errors.As(err, &stopped):
				current.State = "failed"
				current.Reason = stopped.Reason
				current.Detail = stopped.Error()
			case err != nil && errors.As(err, &fixErr):
				current.State = "failed"
				current.Reason = fixErr.Reason
				current.Detail = fixErr.Error()
				if len(current.Detail) > 500 {
					current.Detail = strings.ToValidUTF8(current.Detail[:500], "") + "…"
				}
			case err != nil && current.Automatic && errors.As(err, &refused):
				// The recheck found what automatic generation skips (a PR
				// turned draft) before any model call.
				current.State = "cancelled"
				current.Reason = refused.Reason
			case err != nil && errors.Is(err, ErrNeedsClone):
				// The clone was removed while the job waited: nothing ran. An
				// automatic job is cancelled like a refused one, so its head is
				// not used up and runs once a clone is back; a manual one fails.
				current.State = "failed"
				if current.Automatic {
					current.State = "cancelled"
				}
				current.Reason = ReasonNeedsClone
			case err != nil:
				current.State = "failed"
				current.Reason = string(generate.CategoryOf(err))
				if current.Reason == "" {
					current.Reason = "internal"
				}
				var failure *generate.Error
				if errors.As(err, &failure) {
					current.Detail = failure.Error()
					if len(current.Detail) > 500 {
						current.Detail = current.Detail[:500] + "…"
					}
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
