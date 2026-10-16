package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeExecutor struct {
	mu         sync.Mutex
	head       string
	digest     string
	restricted bool
	price      *float64
	started    chan Prepared
	release    chan struct{}
}

func (f *fakeExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Prepared{ConfigDigest: f.digest, Item: item, Repository: "org/repo", HeadSHA: f.head, Backend: "claude", Model: "claude-sonnet-4-5", EstimateUSD: f.price, RequiresIsolationConfirmation: f.restricted}, "2026-10-01", nil
}
func (f *fakeExecutor) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	f.started <- p
	progress(Progress{Phase: "repairing", Calls: 2, Tokens: 10, Repairs: 1})
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
	e := estimate(t, q, "single", Item{"clone", 1})
	if _, err := q.Submit(context.Background(), e.ID, false, false); err == nil {
		t.Fatal("unconfirmed spend accepted")
	}
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	again := estimate(t, q, "single", Item{"other-clone", 1})
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
	first := estimate(t, q, "single", Item{"clone", 1})
	if _, e := q.Submit(context.Background(), first.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	batch := estimate(t, q, "batch", Item{"clone", 2}, Item{"clone", 3})
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
	next := estimate(t, q, "single", Item{"second-clone", 2})
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
	busy := estimate(t, q, "single", Item{"clone", 1})
	if _, e := q.Submit(context.Background(), busy.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	first := estimate(t, q, "single", Item{"clone", 2})
	queued, e := q.Submit(context.Background(), first.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	again := estimate(t, q, "single", Item{"clone", 2})
	same, e := q.Submit(context.Background(), again.ID, true, false)
	if e != nil || same[0].ID != queued[0].ID {
		t.Fatal("identical confirmation not idempotent", same, e)
	}
	f.mu.Lock()
	f.digest = "larger-budget"
	f.mu.Unlock()
	changed := estimate(t, q, "single", Item{"clone", 2})
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
	one := estimate(t, q, "single", Item{"clone", 1})
	if _, e := q.Submit(context.Background(), one.ID, true, false); e == nil {
		t.Fatal("isolation confirmation bypassed")
	}
	if _, e := q.Submit(context.Background(), one.ID, true, true); e != nil {
		t.Fatal(e)
	}
	<-f.started
	batch := estimate(t, q, "batch", Item{"clone", 2})
	if batch.Items[0].BlockedReason != "isolation_confirmation_required" {
		t.Fatal(batch)
	}
	if _, e := q.Submit(context.Background(), batch.ID, true, true); e == nil {
		t.Fatal("restricted batch accepted")
	}
}
func TestUnknownPriceAndChangedHeadAreFailClosed(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{"clone", 1})
	f.mu.Lock()
	f.head = "changed"
	f.mu.Unlock()
	if _, err := q.Submit(context.Background(), e.ID, true, true); err == nil {
		t.Fatal("stale head accepted")
	}
	f.price = nil
	e = estimate(t, q, "single", Item{"clone", 1})
	if e.Items[0].BlockedReason != "unknown_model_price" || e.EstimateUSD != nil {
		t.Fatal(e)
	}
	if _, err := q.Submit(context.Background(), e.ID, true, true); err == nil {
		t.Fatal("unknown price accepted")
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
func TestQueuePersistenceFailureDoesNotStartSpend(t *testing.T) {
	q, f := newQueue(t)
	e := estimate(t, q, "single", Item{"clone", 1})
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
	first := estimate(t, q, "single", Item{"clone", 1})
	running, e := q.Submit(context.Background(), first.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	<-base.started
	next := estimate(t, q, "single", Item{"clone", 2})
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
	first := estimate(t, q, "single", Item{"clone", 1})
	if _, e := q.Submit(context.Background(), first.ID, true, false); e != nil {
		t.Fatal(e)
	}
	<-f.started
	queued := estimate(t, q, "single", Item{"clone", 2})
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
