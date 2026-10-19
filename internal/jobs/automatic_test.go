package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// autoExecutor prepares per-PR heads and lets a test choose what a person
// would have to confirm.
type autoExecutor struct {
	mu         sync.Mutex
	heads      map[int]string
	price      *float64
	backend    string
	restricted bool
	draft      bool
	prepared   int
	started    chan Prepared
	release    chan struct{}
	fail       error
}

func (f *autoExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	p := Prepared{ConfigDigest: "digest", Item: item, Repository: "org/repo", HeadSHA: f.heads[item.PR], Backend: f.backend, Model: "claude-sonnet-4-5", EstimateUSD: f.price, RequiresIsolationConfirmation: f.restricted, Draft: f.draft}
	if f.price == nil && f.backend != agent.BackendCursorAgent {
		p.BlockedReason = "unknown_model_price"
	}
	return p, "2026-10-01", nil
}

func (f *autoExecutor) Execute(ctx context.Context, p Prepared, _ func(Progress)) (*float64, error) {
	f.started <- p
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		cost := 1.5
		return &cost, f.fail
	}
}

func newAutoQueue(t *testing.T) (*Queue, *autoExecutor) {
	t.Helper()
	price := 2.0
	f := &autoExecutor{heads: map[int]string{1: "a1", 2: "b1", 3: "c1"}, price: &price, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	return q, f
}

func auto(pr int, head string) AutomaticRequest {
	return AutomaticRequest{Item: Item{"clone", pr}, Repository: "org/repo", Head: head, Trigger: TriggerNewHead}
}

func reason(err error) string {
	var refused *NotAutomaticError
	if errors.As(err, &refused) {
		return refused.Reason
	}
	return ""
}

func TestAutomaticJobRunsThroughTheQueueMarkedAutomatic(t *testing.T) {
	q, f := newAutoQueue(t)
	j, err := q.SubmitAutomatic(context.Background(), auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if !j.Automatic || j.Trigger != TriggerNewHead || j.HeadSHA != "a1" || j.EstimateUSD == nil || *j.EstimateUSD != 2 || j.ConfigDigest != "digest" {
		t.Fatalf("job %+v", j)
	}
	if p := <-f.started; p.HeadSHA != "a1" {
		t.Fatalf("executed %+v", p)
	}
	close(f.release)
	done := waitState(t, q, j.ID, "completed")
	if !done.Automatic || done.Trigger != TriggerNewHead {
		t.Fatalf("finished job lost its trigger %+v", done)
	}
}

// What a person must confirm is never generated automatically: forks and
// authors without known write access, unpriced models, cursor-agent without
// the acknowledgement; and a head that moved since the trigger.
func TestAutomaticRefusesWhatNeedsAPerson(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	f.restricted = true
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); reason(err) != ReasonIsolation {
		t.Fatalf("restricted PR: %v", err)
	}
	f.restricted = false
	f.price = nil
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); reason(err) != "unknown_model_price" {
		t.Fatalf("unpriced model: %v", err)
	}
	f.backend = agent.BackendCursorAgent
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); reason(err) != ReasonCursorAgentConsent {
		t.Fatalf("cursor-agent without acknowledgement: %v", err)
	}
	price := 2.0
	f.price = &price
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); reason(err) != ReasonCursorAgentConsent {
		t.Fatalf("priced cursor-agent without acknowledgement: %v", err)
	}
	if _, err := q.SubmitAutomatic(ctx, auto(1, "stale")); !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("changed head: %v", err)
	}
	if len(q.List()) != 0 {
		t.Fatalf("refused requests queued jobs: %+v", q.List())
	}
	r := auto(1, "a1")
	r.AllowUnpriced = true
	j, err := q.SubmitAutomatic(ctx, r)
	if err != nil || j.Backend != agent.BackendCursorAgent {
		t.Fatalf("acknowledged cursor-agent: %+v %v", j, err)
	}
}

// One job per PR at a time, one job per head ever, manual or automatic.
func TestAutomaticOneJobPerPRAndDedupedByHead(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	first, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	prepared := f.prepared
	if _, err = q.SubmitAutomatic(ctx, auto(1, "a1")); !errors.Is(err, ErrBusy) {
		t.Fatalf("same head while running: %v", err)
	}
	f.heads[1] = "a2"
	if _, err = q.SubmitAutomatic(ctx, auto(1, "a2")); !errors.Is(err, ErrBusy) {
		t.Fatalf("new head while running: %v", err)
	}
	if f.prepared != prepared {
		t.Fatal("a busy PR was prepared against GitHub")
	}
	f.release <- struct{}{}
	waitState(t, q, first.ID, "completed")
	f.heads[1] = "a1"
	if _, err = q.SubmitAutomatic(ctx, auto(1, "a1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("finished head again: %v", err)
	}
	f.heads[1] = "a2"
	second, err := q.SubmitAutomatic(ctx, auto(1, "a2"))
	if err != nil {
		t.Fatalf("new head after the job: %v", err)
	}
	<-f.started
	// A confirmed job is refused while the automatic one runs, and an
	// automatic one never joins or supersedes a confirmed one.
	e := estimate(t, q, "single", Item{"clone", 1})
	if _, err = q.Submit(ctx, e.ID, true, false); err == nil {
		t.Fatal("confirmed job accepted beside a running automatic one")
	}
	f.release <- struct{}{}
	waitState(t, q, second.ID, "completed")
	f.heads[2] = "b1"
	e = estimate(t, q, "single", Item{"clone", 2})
	manual, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	if _, err = q.SubmitAutomatic(ctx, auto(2, "b1")); !errors.Is(err, ErrBusy) {
		t.Fatalf("automatic beside a running confirmed job: %v", err)
	}
	f.release <- struct{}{}
	waitState(t, q, manual[0].ID, "completed")
	if _, err = q.SubmitAutomatic(ctx, auto(2, "b1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("head a confirmed job generated: %v", err)
	}
}

func TestAutomaticRefusedWhileDrainingAndAcceptedAfterResume(t *testing.T) {
	q, _ := newAutoQueue(t)
	if !q.Drain() {
		t.Fatal("idle queue did not drain")
	}
	if _, err := q.SubmitAutomatic(context.Background(), auto(1, "a1")); !errors.Is(err, ErrRestarting) {
		t.Fatalf("while draining: %v", err)
	}
	// Without the repository the journal check runs after Prepare.
	r := auto(1, "a1")
	r.Repository = ""
	if _, err := q.SubmitAutomatic(context.Background(), r); !errors.Is(err, ErrRestarting) {
		t.Fatalf("while draining, prepared: %v", err)
	}
	q.Resume()
	if _, err := q.SubmitAutomatic(context.Background(), auto(1, "a1")); err != nil {
		t.Fatalf("after resume: %v", err)
	}
}

func TestAutomaticDailyCapCountsTodaysAutomaticJobs(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	day := func(r AutomaticRequest, start time.Time, limit float64) AutomaticRequest {
		r.Day, r.DailyCap = start, &limit
		return r
	}
	start := time.Now().Add(-time.Hour)
	if _, err := q.SubmitAutomatic(ctx, day(auto(1, "a1"), start, 0)); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("zero cap: %v", err)
	}
	first, err := q.SubmitAutomatic(ctx, day(auto(1, "a1"), start, 3))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	if spent, known := q.AutomaticSpend("org/repo", start); spent != 2 || !known {
		t.Fatalf("spend while running %v %v", spent, known)
	}
	if _, err = q.SubmitAutomatic(ctx, day(auto(2, "b1"), start, 3)); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("over the cap: %v", err)
	}
	f.release <- struct{}{}
	waitState(t, q, first.ID, "completed")
	// The reported cost replaces the estimate: 1.5 + 2 still exceeds 3.
	if spent, _ := q.AutomaticSpend("org/repo", start); spent != 1.5 {
		t.Fatalf("spend after completion %v", spent)
	}
	if _, err = q.SubmitAutomatic(ctx, day(auto(2, "b1"), start, 3)); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("over the cap after completion: %v", err)
	}
	// A confirmed job does not count against the automatic cap.
	e := estimate(t, q, "single", Item{"clone", 3})
	manual, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, manual[0].ID, "completed")
	if spent, _ := q.AutomaticSpend("org/repo", start); spent != 1.5 {
		t.Fatalf("confirmed job counted %v", spent)
	}
	// The next day starts from zero.
	tomorrow := start.Add(time.Hour).AddDate(0, 0, 1)
	if _, err = q.SubmitAutomatic(ctx, day(auto(2, "b1"), tomorrow, 3)); err != nil {
		t.Fatalf("next day: %v", err)
	}
	// Without a cap nothing limits spending.
	f.heads[3] = "c2"
	if _, err = q.SubmitAutomatic(ctx, auto(3, "c2")); err != nil {
		t.Fatalf("uncapped: %v", err)
	}
}

// An automatic run a daemon stop interrupted may run its head once more;
// a confirmed run, a failure and a cancellation never run it again.
func TestAutomaticInterruptedRunMayRunOnceMore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateDir(t), "jobs.json")
	price := 2.0
	f := &autoExecutor{heads: map[int]string{1: "a1", 2: "b1", 3: "c1", 4: "d1"}, price: &price, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	open := func() *Queue {
		q, err := Open(path, f, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(q.Close)
		return q
	}
	q := open()
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); err != nil {
		t.Fatal(err)
	}
	<-f.started
	q.Close()
	q = open()
	interrupted := q.Interrupted()
	if len(interrupted) != 1 || interrupted[0].PR != 1 || interrupted[0].State != "interrupted" {
		t.Fatalf("interrupted %+v", interrupted)
	}
	r := auto(1, "a1")
	r.Trigger = TriggerRetry
	retry, err := q.SubmitAutomatic(ctx, r)
	if err != nil || retry.Trigger != TriggerRetry {
		t.Fatalf("retry %+v %v", retry, err)
	}
	<-f.started
	q.Close()
	q = open()
	if got := q.Interrupted(); len(got) != 0 {
		t.Fatalf("a retried head offered again %+v", got)
	}
	if _, err = q.SubmitAutomatic(ctx, r); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second retry: %v", err)
	}
	// A confirmed run interrupted is not retried automatically.
	e := estimate(t, q, "single", Item{"clone", 2})
	if _, err = q.Submit(ctx, e.ID, true, false); err != nil {
		t.Fatal(err)
	}
	<-f.started
	q.Close()
	q = open()
	if _, err = q.SubmitAutomatic(ctx, auto(2, "b1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("interrupted confirmed run: %v", err)
	}
	// Failed and cancelled automatic runs are final for their head.
	f.fail = errors.New("backend failed")
	failed, err := q.SubmitAutomatic(ctx, auto(3, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, failed.ID, "failed")
	cancelled, err := q.SubmitAutomatic(ctx, auto(4, "d1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	if _, err = q.Cancel(cancelled.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, q, cancelled.ID, "cancelled")
	if got := q.Interrupted(); len(got) != 0 {
		t.Fatalf("failed or cancelled offered for retry %+v", got)
	}
	for _, r := range []AutomaticRequest{auto(3, "c1"), auto(4, "d1")} {
		if _, err = q.SubmitAutomatic(ctx, r); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("PR %d: %v", r.PR, err)
		}
	}
}

func TestAutomaticRefusesDrafts(t *testing.T) {
	q, f := newAutoQueue(t)
	f.draft = true
	if _, err := q.SubmitAutomatic(context.Background(), auto(1, "a1")); reason(err) != ReasonDraft {
		t.Fatalf("draft: %v", err)
	}
	// A confirmed run of a draft still works.
	e := estimate(t, q, "single", Item{"clone", 1})
	if _, err := q.Submit(context.Background(), e.ID, true, false); err != nil {
		t.Fatal(err)
	}
}

// The gate is asked under the queue's mutex when an automatic job is
// submitted and again before it starts; CancelDisallowedAutomatic cancels
// queued ones it refuses. Running and confirmed jobs are untouched.
func TestAutomaticGateStopsQueuedJobs(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	var mu sync.Mutex
	refuse := map[int]string{}
	q.SetAutomaticGate(func(j Job) string {
		mu.Lock()
		defer mu.Unlock()
		return refuse[j.PR]
	})
	set := func(pr int, reason string) {
		mu.Lock()
		defer mu.Unlock()
		refuse[pr] = reason
	}
	set(1, "automatic_off")
	var refused *NotAllowedError
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); !errors.As(err, &refused) || refused.Reason != "automatic_off" {
		t.Fatalf("submitted while off: %v", err)
	}
	set(1, "")
	running, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	queued := []Job{}
	for _, pr := range []int{2, 3} {
		j, err := q.SubmitAutomatic(ctx, auto(pr, f.heads[pr]))
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, j)
	}
	f.heads[4] = "d1"
	e := estimate(t, q, "single", Item{"clone", 4})
	confirmed, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range []int{1, 2, 4} {
		set(pr, "automatic_off")
	}
	cancelled := q.CancelDisallowedAutomatic()
	if len(cancelled) != 1 || cancelled[0].ID != queued[0].ID || cancelled[0].State != "cancelled" || cancelled[0].Reason != "automatic_off" {
		t.Fatalf("cancelled %+v", cancelled)
	}
	// PR 3 is refused only now: the worker cancels it instead of starting it.
	set(3, "repository_automatic_off")
	f.release <- struct{}{}
	waitState(t, q, running.ID, "completed")
	waitState(t, q, queued[1].ID, "cancelled")
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, confirmed[0].ID, "completed")
	for _, j := range q.List() {
		if j.ID == queued[1].ID && j.Reason != "repository_automatic_off" {
			t.Fatalf("reason %+v", j)
		}
	}
}
