package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

type fakeExecutor struct {
	mu         sync.Mutex
	head       string
	digest     string
	restricted bool
	price      *float64
	// backend and reason, when set, replace claude and the estimate reason.
	backend, reason string
	started         chan Prepared
	release         chan struct{}
	// prepareErr, when set, fails Prepare.
	prepareErr error
}

func (f *fakeExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepareErr != nil {
		return Prepared{}, "", f.prepareErr
	}
	backend := f.backend
	if backend == "" {
		backend = "claude"
	}
	return Prepared{ConfigDigest: f.digest, Item: item, Repository: "org/repo", HeadSHA: f.head, Backend: backend, Model: "claude-sonnet-4-5", EstimateUSD: f.price, EstimateReason: f.reason, RequiresIsolationConfirmation: f.restricted}, "2026-10-01", nil
}
func (f *fakeExecutor) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	f.started <- p
	a := &agent.Accounting{}
	a.Add(agent.Result{Model: "reported-model", Calls: 2, Tokens: 10, Usage: agent.Usage{Input: 6, Output: 4, CacheRead: 100}}, false)
	progress(Progress{Phase: "repairing", Calls: 2, Tokens: 10, Repairs: 1, Accounting: a, CostReason: "speed_not_reported"})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return nil, nil
	}
}
func newQueue(t *testing.T) (*Queue, *fakeExecutor) {
	t.Helper()
	price := 2.0
	f := &fakeExecutor{head: "first", price: &price, started: make(chan Prepared, 10), release: make(chan struct{})}
	q, e := Open(filepath.Join(privateDir(t), "jobs.json"), f, 1, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(q.Close)
	return q, f
}
func estimate(t *testing.T, q *Queue, mode string, items ...Item) Estimate {
	t.Helper()
	e, err := q.Estimate(context.Background(), items, mode)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func waitState(t *testing.T, q *Queue, id, state string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range q.List() {
			if j.ID == id && j.State == state {
				return j
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job did not reach %s: %+v", state, q.List())
	return Job{}
}
func TestConfirmationCancellationDuringRepairAndRunningDedupe(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, err := q.Submit(context.Background(), e.ID, false, false); err == nil {
		t.Fatal("unconfirmed spend accepted")
	}
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	again := estimate(t, q, "single", Item{Clone: "other-clone", PR: 1})
	if _, err = q.Submit(context.Background(), again.ID, true, false); err == nil {
		t.Fatal("duplicate running job accepted")
	}
	if _, err = q.Cancel(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	j := waitState(t, q, jobs[0].ID, "cancelled")
	if j.Repairs != 1 || j.ActualCostUSD != nil {
		t.Fatalf("lost repair/cost state %+v", j)
	}
}
func TestQueuedReplacementBatchEstimateAndNoParallelPR(t *testing.T) {
	q, f := newQueue(t)
	first := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, e := q.Submit(context.Background(), first.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	batch := estimate(t, q, "batch", Item{Clone: "clone", PR: 2}, Item{Clone: "clone", PR: 3})
	if *batch.EstimateUSD != 4 {
		t.Fatal(batch)
	}
	queued, e := q.Submit(context.Background(), batch.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.head = "second"
	f.mu.Unlock()
	next := estimate(t, q, "single", Item{Clone: "second-clone", PR: 2})
	replacement, e := q.Submit(context.Background(), next.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, q, queued[0].ID, "cancelled")
	if replacement[0].HeadSHA != "second" {
		t.Fatal(replacement)
	}
	select {
	case <-f.started:
		t.Fatal("concurrency limit exceeded")
	default:
	}
}
func TestSameHeadWithChangedInputsSupersedesQueuedJob(t *testing.T) {
	q, f := newQueue(t)
	busy := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, e := q.Submit(context.Background(), busy.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	first := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	queued, e := q.Submit(context.Background(), first.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	again := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	same, e := q.Submit(context.Background(), again.ID, true, false)
	if e != nil || same[0].ID != queued[0].ID {
		t.Fatal("identical confirmation not idempotent", same, e)
	}
	f.mu.Lock()
	f.digest = "larger-budget"
	f.mu.Unlock()
	changed := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	replacement, e := q.Submit(context.Background(), changed.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	if replacement[0].ID == queued[0].ID || replacement[0].ConfigDigest != "larger-budget" || replacement[0].State != "queued" {
		t.Fatal("changed inputs returned the stale queued job", replacement)
	}
	if old := waitState(t, q, queued[0].ID, "cancelled"); old.Reason != "superseded" {
		t.Fatal(old)
	}
	close(f.release)
	if next := <-f.started; next.PR != 2 || next.ConfigDigest != "larger-budget" {
		t.Fatal("replacement did not run with the confirmed inputs", next)
	}
	waitState(t, q, replacement[0].ID, "completed")
}
func TestIsolationEnforcedAndExcludedFromBatch(t *testing.T) {
	q, f := newQueue(t)
	f.restricted = true
	one := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, e := q.Submit(context.Background(), one.ID, true, false); e == nil {
		t.Fatal("isolation confirmation bypassed")
	}
	if _, e := q.Submit(context.Background(), one.ID, true, true); e != nil {
		t.Fatal(e)
	}
	<-f.started
	batch := estimate(t, q, "batch", Item{Clone: "clone", PR: 2})
	if batch.Items[0].BlockedReason != "isolation_confirmation_required" {
		t.Fatal(batch)
	}
	if _, e := q.Submit(context.Background(), batch.ID, true, true); e == nil {
		t.Fatal("restricted batch accepted")
	}
}

// A moved head is refused; a model the price list does not know still runs,
// with no estimate.
func TestChangedHeadFailsClosedAndUnknownPriceRuns(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	f.mu.Lock()
	f.head = "changed"
	f.mu.Unlock()
	if _, err := q.Submit(context.Background(), e.ID, true, true); err == nil {
		t.Fatal("stale head accepted")
	}
	f.price = nil
	e = estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if e.Items[0].BlockedReason != "" || e.EstimateUSD != nil {
		t.Fatal(e)
	}
	if _, err := q.Submit(context.Background(), e.ID, true, true); err != nil {
		t.Fatal(err)
	}
}

// A backend billed outside the price list (cursor-agent) is not blocked: its
// estimate is unknown with a reason, and it runs on the usual confirmations.
func TestUnpricedBackendRuns(t *testing.T) {
	q, f := newQueue(t)
	f.mu.Lock()
	f.price, f.backend, f.reason = nil, agent.BackendCursorAgent, "billed_by_cursor_subscription"
	f.mu.Unlock()
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if e.EstimateUSD != nil || e.Items[0].BlockedReason != "" || e.Items[0].EstimateReason != "billed_by_cursor_subscription" {
		t.Fatalf("estimate = %+v", e)
	}
	if _, err := q.SubmitConfirmed(context.Background(), e.ID, Confirmation{}); err == nil {
		t.Fatal("an unpriced run was queued without confirming the estimate")
	}
	jobs, err := q.SubmitConfirmed(context.Background(), e.ID, Confirmation{Estimate: true})
	if err != nil || len(jobs) != 1 || jobs[0].EstimateReason != "billed_by_cursor_subscription" {
		t.Fatalf("jobs = %+v, err = %v", jobs, err)
	}
	<-f.started
	close(f.release)
	waitState(t, q, jobs[0].ID, "completed")

	// The fork rules still apply: isolation is confirmed separately.
	q, f = newQueue(t)
	f.mu.Lock()
	f.price, f.backend, f.reason, f.restricted = nil, agent.BackendCursorAgent, "billed_by_cursor_subscription", true
	f.mu.Unlock()
	e = estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, err := q.SubmitConfirmed(context.Background(), e.ID, Confirmation{Estimate: true}); err == nil {
		t.Fatal("a fork run was queued without isolation confirmation")
	}
	if _, err := q.SubmitConfirmed(context.Background(), e.ID, Confirmation{Estimate: true, Isolation: true}); err != nil {
		t.Fatal(err)
	}
	batch := estimate(t, q, "batch", Item{Clone: "clone", PR: 2})
	if batch.Items[0].BlockedReason != "isolation_confirmation_required" {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestRestartInterruptsRunningAndKeepsHistory(t *testing.T) {
	path := filepath.Join(privateDir(t), "jobs.json")
	raw, _ := json.Marshal([]Job{{ID: "old", State: "running", Phase: "generating"}, {ID: "completed", State: "completed"}})
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	price := 1.0
	f := &fakeExecutor{price: &price}
	q, e := Open(path, f, 1, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	jobs := q.List()
	if jobs[0].State != "interrupted" || jobs[0].Reason != "interrupted" || jobs[1].State != "completed" {
		t.Fatal(jobs)
	}
	raw, e = os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &jobs); e != nil || jobs[0].State != "interrupted" {
		t.Fatal("restart not persisted", e)
	}
}
func TestShutdownRecordsRunningJobInterrupted(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	waitState(t, q, jobs[0].ID, "running")
	if err = q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(q.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []Job
	if err = json.Unmarshal(raw, &saved); err != nil || saved[0].State != "interrupted" || saved[0].Reason != "interrupted" {
		t.Fatalf("journal %s, %v", raw, err)
	}
}
func TestShutdownStopsWaitingAtDeadline(t *testing.T) {
	stuck := &stuckExecutor{started: make(chan struct{}), release: make(chan struct{})}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), stuck, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(stuck.release); q.Close() })
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, err = q.Submit(context.Background(), e.ID, true, false); err != nil {
		t.Fatal(err)
	}
	<-stuck.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err = q.Shutdown(ctx); err == nil {
		t.Fatal("shutdown waited for a backend that ignores cancellation")
	}
}

// stuckExecutor ignores cancellation until released.
type stuckExecutor struct{ started, release chan struct{} }

func (s *stuckExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	price := 1.0
	return Prepared{Item: item, Repository: "org/repo", HeadSHA: "head", Backend: "claude", Model: "m", EstimateUSD: &price}, "v", nil
}
func (s *stuckExecutor) Execute(context.Context, Prepared, func(Progress)) (*float64, error) {
	close(s.started)
	<-s.release
	return nil, nil
}
func TestOneProcessOwnsTheJournal(t *testing.T) {
	path := filepath.Join(privateDir(t), "jobs.json")
	price := 1.0
	first, err := Open(path, &fakeExecutor{price: &price}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Now()
	if second, err := Open(path, &fakeExecutor{price: &price}, 1, nil); err == nil {
		second.Close()
		t.Fatal("a second queue opened the same journal")
	}
	if time.Since(opened) < lockWait {
		t.Fatal("gave up before the wait")
	}
	if !first.Idle() {
		t.Fatal("empty queue not idle")
	}
	first.Close()
	again, err := Open(path, &fakeExecutor{price: &price}, 1, nil)
	if err != nil {
		t.Fatal("journal still locked after Close:", err)
	}
	again.Close()
}

// Drain and Submit race: either the job is accepted and Drain reports work
// pending, or Drain wins and the job is refused, never both or neither.
func TestDrainAndSubmitNeverBothSucceed(t *testing.T) {
	for range 50 {
		q, f := newQueue(t)
		e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
		var drained bool
		var submitted []Job
		var submitErr error
		var wg sync.WaitGroup
		wg.Go(func() { drained = q.Drain() })
		wg.Go(func() { submitted, submitErr = q.Submit(context.Background(), e.ID, true, false) })
		wg.Wait()
		switch {
		case drained && (submitErr == nil || !errors.Is(submitErr, ErrRestarting) || len(q.List()) != 0):
			t.Fatalf("drained, yet submit %v, %v, %d jobs", submitted, submitErr, len(q.List()))
		case !drained && submitErr != nil:
			t.Fatalf("neither drained nor submitted: %v", submitErr)
		}
		if !drained {
			<-f.started
			close(f.release)
		}
		q.Close()
	}
}

func TestSubmitWhileRestartingIsAServiceUnavailableToRetry(t *testing.T) {
	q, _ := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if !q.Drain() {
		t.Fatal("empty queue not drained")
	}
	body := strings.NewReader(`{"estimate_id":"` + e.ID + `","confirm_estimate":true,"confirm_isolation":false}`)
	w := httptest.NewRecorder()
	q.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/jobs", body))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "restarting") {
		t.Fatalf("%d %q %q", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	q.Resume()
	if _, err := q.Submit(context.Background(), e.ID, true, false); err != nil {
		t.Fatal("not accepted after Resume:", err)
	}
}

// A predecessor that holds the journal while it exits is waited for.
func TestOpenWaitsForThePreviousOwner(t *testing.T) {
	path := filepath.Join(privateDir(t), "jobs.json")
	price := 1.0
	first, err := Open(path, &fakeExecutor{price: &price}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); first.Close() }()
	second, err := Open(path, &fakeExecutor{price: &price}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}
func TestQueuePersistenceFailureDoesNotStartSpend(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	q.mu.Lock()
	q.path = filepath.Join(q.path, "not-a-directory")
	q.mu.Unlock()
	if _, err := q.Submit(context.Background(), e.ID, true, false); err == nil {
		t.Fatal("unpersisted job accepted")
	}
	select {
	case <-f.started:
		t.Fatal("unpersisted job started")
	default:
	}
	if len(q.List()) != 0 {
		t.Fatal("unpersisted job leaked")
	}
}

type blockedPrepare struct {
	*fakeExecutor
	block    chan struct{}
	entered  chan struct{}
	muBlock  sync.Mutex
	blocking bool
}

func (f *blockedPrepare) Prepare(ctx context.Context, item Item) (Prepared, string, error) {
	f.muBlock.Lock()
	blocking := f.blocking
	f.muBlock.Unlock()
	if blocking {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		select {
		case <-f.block:
		case <-ctx.Done():
			return Prepared{}, "", ctx.Err()
		}
	}
	return f.fakeExecutor.Prepare(ctx, item)
}
func TestSlowPreflightDoesNotBlockCancellationAndConcurrentEstimateUse(t *testing.T) {
	price := 1.0
	base := &fakeExecutor{head: "first", price: &price, started: make(chan Prepared, 3), release: make(chan struct{})}
	f := &blockedPrepare{fakeExecutor: base, block: make(chan struct{}), entered: make(chan struct{}, 3)}
	q, e := Open(filepath.Join(privateDir(t), "jobs.json"), f, 1, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	first := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	running, e := q.Submit(context.Background(), first.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	<-base.started
	next := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	f.muBlock.Lock()
	f.blocking = true
	f.muBlock.Unlock()
	done := make(chan error, 2)
	for range 2 {
		go func() { _, e := q.Submit(context.Background(), next.ID, true, false); done <- e }()
	}
	<-f.entered
	<-f.entered
	cancelled := make(chan struct{})
	go func() { _, _ = q.Cancel(running[0].ID); close(cancelled) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("network preflight blocked cancellation")
	}
	close(f.block)
	one, two := <-done, <-done
	if (one == nil) == (two == nil) {
		t.Fatal("estimate consumed other than once", one, two)
	}
}

func TestQueuedMutationsNotifyAfterPersistenceOutsideMutex(t *testing.T) {
	q, f := newQueue(t)
	notifications := make(chan Job, 10)
	q.changed = func(job Job) {
		_ = q.List()
		raw, e := os.ReadFile(q.path)
		if e != nil {
			t.Error(e)
		}
		var saved []Job
		if e = json.Unmarshal(raw, &saved); e != nil {
			t.Error(e)
		}
		notifications <- job
	}
	first := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, e := q.Submit(context.Background(), first.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	queued := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	jobs, e := q.Submit(context.Background(), queued.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.Cancel(jobs[0].ID); e != nil {
		t.Fatal(e)
	}
	foundQueued, foundCancelled := false, false
	deadline := time.After(time.Second)
	for !foundQueued || !foundCancelled {
		select {
		case job := <-notifications:
			if job.ID == jobs[0].ID {
				foundQueued = foundQueued || job.State == "queued"
				foundCancelled = foundCancelled || job.State == "cancelled"
			}
		case <-deadline:
			t.Fatal("queued state changes not broadcast", foundQueued, foundCancelled)
		}
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	return dir
}

func TestUsageProgressIsDurableAndDetached(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	deadline := time.Now().Add(time.Second)
	for q.List()[0].Accounting == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := q.List()[0]
	if got.Accounting == nil {
		t.Fatal("missing usage")
	}
	got.Accounting.Usage.Input = 999
	if q.List()[0].Accounting.Usage.Input != 6 {
		t.Fatal("List leaked mutable accounting")
	}
	raw, err := os.ReadFile(q.path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []Job
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted[0].Accounting.Usage.CacheRead != 100 {
		t.Fatal("running usage not saved", string(raw))
	}
	if _, err = q.Cancel(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	terminal := waitState(t, q, jobs[0].ID, "cancelled")
	if terminal.Accounting.Usage.CacheRead != 100 || terminal.CostReason != "speed_not_reported" {
		t.Fatal("lost cancelled usage", terminal)
	}
	q.Close()
	reopened, err := Open(q.path, f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.List()[0].Accounting.Usage.Input != 6 {
		t.Fatal("lost usage after restart")
	}
}
