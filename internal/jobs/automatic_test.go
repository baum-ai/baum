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
	prepared   int
	started    chan Prepared
	release    chan struct{}
}

func (f *autoExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	p := Prepared{ConfigDigest: "digest", Item: item, Repository: "org/repo", HeadSHA: f.heads[item.PR], Backend: f.backend, Model: "claude-sonnet-4-5", EstimateUSD: f.price, RequiresIsolationConfirmation: f.restricted}
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
		return &cost, nil
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
