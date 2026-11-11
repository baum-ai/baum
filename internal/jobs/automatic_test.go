package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb"
)

// autoExecutor prepares per-PR heads and lets a test choose what a person
// would have to confirm.
type autoExecutor struct {
	mu         sync.Mutex
	heads      map[int]string
	backend    string
	restricted bool
	draft      bool
	prepared   int
	started    chan Prepared
	release    chan struct{}
	fail       error
	// noClone fails Execute as Local does once the job's clone is gone.
	noClone bool
	// members are each group's member heads, as Local prepares them.
	members map[string]string
}

func (f *autoExecutor) Prepare(_ context.Context, item Item) (Prepared, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	p := Prepared{ConfigDigest: "digest", Item: item, Repository: "org/repo", HeadSHA: f.heads[item.PR], Backend: f.backend, Model: "claude-sonnet-4-5", RequiresIsolationConfirmation: f.restricted, Draft: f.draft, Members: f.members[item.Group]}
	return p, "2026-10-01", nil
}

func (f *autoExecutor) Execute(ctx context.Context, p Prepared, _ func(Progress)) (*float64, error) {
	// Like Local's recheck: an automatic job of a PR now a draft is refused.
	f.mu.Lock()
	draft := f.draft
	f.mu.Unlock()
	if p.RefuseDraft && draft {
		return nil, &NotAutomaticError{ReasonDraft}
	}
	if f.noClone {
		return nil, fmt.Errorf("execute: %w", ErrNeedsClone)
	}
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
	f := &autoExecutor{heads: map[int]string{1: "a1", 2: "b1", 3: "c1"}, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	// Like the daemon, which installs the gate right after Open: an automatic
	// job does not start before one is set.
	q.SetAutomaticGate(func(Job) Admission { return Admission{} })
	return q, f
}

func auto(pr int, head string) AutomaticRequest {
	return AutomaticRequest{Item: Item{Clone: "clone", PR: pr}, Repository: "org/repo", Head: head, Trigger: TriggerNewHead}
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
	if !j.Automatic || j.Trigger != TriggerNewHead || j.HeadSHA != "a1" || j.ConfigDigest != "digest" {
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
// authors without known write access; nor a head that moved since the
// trigger. A model without a price, cursor-agent included, runs.
func TestAutomaticRefusesWhatNeedsAPerson(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	f.restricted = true
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); reason(err) != ReasonIsolation {
		t.Fatalf("restricted PR: %v", err)
	}
	f.restricted = false
	f.backend = agent.BackendCursorAgent
	if _, err := q.SubmitAutomatic(ctx, auto(1, "stale")); !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("changed head: %v", err)
	}
	if len(q.List()) != 0 {
		t.Fatalf("refused requests queued jobs: %+v", q.List())
	}
	j, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil || j.Backend != agent.BackendCursorAgent {
		t.Fatalf("cursor-agent: %+v %v", j, err)
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
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, err = q.Submit(ctx, e.ID, true, false); err == nil {
		t.Fatal("confirmed job accepted beside a running automatic one")
	}
	f.release <- struct{}{}
	waitState(t, q, second.ID, "completed")
	f.heads[2] = "b1"
	e = estimate(t, q, "single", Item{Clone: "clone", PR: 2})
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

// An automatic run a daemon stop interrupted may run its head once more;
// a confirmed run, a failure and a cancellation never run it again.
func TestAutomaticInterruptedRunMayRunOnceMore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateDir(t), "jobs.json")
	f := &autoExecutor{heads: map[int]string{1: "a1", 2: "b1", 3: "c1", 4: "d1"}, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	open := func() *Queue {
		q, err := Open(path, f, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(q.Close)
		q.SetAutomaticGate(func(Job) Admission { return Admission{} })
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
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
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
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
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
	q.SetAutomaticGate(func(j Job) Admission {
		mu.Lock()
		defer mu.Unlock()
		return Admission{Reason: refuse[j.PR]}
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
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 4})
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
	// The confirmed job goes ahead of the automatic one.
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, confirmed[0].ID, "completed")
	waitState(t, q, queued[1].ID, "cancelled")
	for _, j := range q.List() {
		if j.ID == queued[1].ID && j.Reason != "repository_automatic_off" {
			t.Fatalf("reason %+v", j)
		}
	}
}

// A queued automatic job in the journal does not start on restart before the
// daemon installs the gate; once the gate refuses it, it is cancelled instead.
func TestAutomaticJournaledJobWaitsForTheGate(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "jobs.json")
	now := time.Now().UTC()
	raw, err := json.Marshal([]Job{{ID: "a", Prepared: Prepared{Item: Item{Clone: "clone", PR: 1}, Repository: "org/repo", HeadSHA: "a1"}, Automatic: true, Trigger: TriggerNewHead, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	if err = appdb.Write(path, "state", "jobs", raw); err != nil {
		t.Fatal(err)
	}
	f := &autoExecutor{started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(path, f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	select {
	case <-f.started:
		t.Fatal("automatic job started before a gate was installed")
	case <-time.After(200 * time.Millisecond):
	}
	done := make(chan Job, 1)
	q.SetAutomaticGate(func(Job) Admission { return Admission{Reason: "automatic_off"} })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := q.List(); len(l) == 1 && l[0].State == "cancelled" {
			done <- l[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case j := <-done:
		if j.Reason != "automatic_off" {
			t.Fatalf("reason %q", j.Reason)
		}
	default:
		t.Fatal("job was not cancelled once the gate refused it")
	}
	select {
	case <-f.started:
		t.Fatal("refused job started")
	default:
	}
}

// noStart fails if the executor starts a job within a short wait.
func noStart(t *testing.T, f *autoExecutor, what string) {
	t.Helper()
	select {
	case p := <-f.started:
		t.Fatalf("%s: PR %d started", what, p.PR)
	case <-time.After(200 * time.Millisecond):
	}
}

// A PR converted back to draft while its automatic job waited is not run:
// the recheck before any model call cancels the job with ReasonDraft, which
// does not use up its head. A confirmed run of a draft still runs.
func TestAutomaticJobOfAPRTurnedDraftDoesNotRun(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	setDraft := func(draft bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.draft = draft
	}
	running, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if p := <-f.started; !p.RefuseDraft {
		t.Fatalf("automatic job executed without the draft recheck %+v", p)
	}
	waiting, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	setDraft(true)
	f.release <- struct{}{}
	waitState(t, q, running.ID, "completed")
	if got := waitState(t, q, waiting.ID, "cancelled"); got.Reason != ReasonDraft || got.Calls != 0 {
		t.Fatalf("draft job %+v", got)
	}
	noStart(t, f, "PR turned draft")
	if refused := q.Refused(); len(refused) != 1 || refused[0].ID != waiting.ID {
		t.Fatalf("refused %+v", refused)
	}
	// A confirmed run of the draft is not rechecked for it.
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 3})
	manual, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if p := <-f.started; p.RefuseDraft {
		t.Fatalf("confirmed job executed with the draft recheck %+v", p)
	}
	f.release <- struct{}{}
	waitState(t, q, manual[0].ID, "completed")
	// Ready again at the same head: not a duplicate.
	setDraft(false)
	again, err := q.SubmitAutomatic(ctx, AutomaticRequest{Item: Item{Clone: "clone", PR: 2}, Repository: "org/repo", Head: "b1", Trigger: TriggerReadyForReview})
	if err != nil {
		t.Fatalf("head stopped as a draft: %v", err)
	}
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, again.ID, "completed")
}

// An automatic job whose clone was removed while it waited never runs: it
// fails with the needs_clone reason instead of an opaque internal error.
func TestAutomaticJobWithoutACloneFailsNeedsClone(t *testing.T) {
	q, f := newAutoQueue(t)
	f.noClone = true
	j, err := q.SubmitAutomatic(context.Background(), auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	done := waitState(t, q, j.ID, "cancelled")
	if done.Reason != ReasonNeedsClone {
		t.Fatalf("job %+v", done)
	}
	select {
	case p := <-f.started:
		t.Fatalf("job ran: %+v", p)
	default:
	}
	// Its head is not used up.
	if refused := q.Refused(); len(refused) != 1 || refused[0].ID != j.ID {
		t.Fatalf("refused %+v", refused)
	}
	f.noClone = false
	if _, err = q.SubmitAutomatic(context.Background(), auto(1, "a1")); err != nil {
		t.Fatalf("resubmit with a clone: %v", err)
	}
}

// A job an older baum cancelled for its daily cap loads as an ordinary
// cancelled job: nothing offers its head again, and its estimate is dropped.
func TestAutomaticJobCancelledForTheOldDailyCapLoads(t *testing.T) {
	path := filepath.Join(privateDir(t), "jobs.json")
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	old := `[{"id":"old","clone":"clone","pr":1,"repository":"org/repo","head_sha":"a1","backend":"claude","model":"m","estimate_usd":2,"automatic":true,"trigger":"new_head","state":"cancelled","phase":"finished","reason":"daily_cap","calls":0,"tokens":0,"repairs":0,"actual_cost_usd":null,"created_at":"` + at + `","updated_at":"` + at + `"}]`
	if err := appdb.Write(path, "state", "jobs", []byte(old)); err != nil {
		t.Fatal(err)
	}
	f := &autoExecutor{heads: map[int]string{1: "a1"}, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(path, f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	q.SetAutomaticGate(func(Job) Admission { return Admission{} })
	if list := q.List(); len(list) != 1 || list[0].State != "cancelled" || list[0].Reason != "daily_cap" {
		t.Fatalf("loaded %+v", list)
	}
	if refused := q.Refused(); len(refused) != 0 {
		t.Fatalf("refused %+v", refused)
	}
	if _, err := q.SubmitAutomatic(context.Background(), auto(1, "a1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("the old head ran again: %v", err)
	}
}

// A group job runs its member heads once, and is busy or a duplicate only
// against jobs of the same group.
func TestAutomaticGroupJobsAreKeyedByGroupAndMemberHeads(t *testing.T) {
	q, f := newAutoQueue(t)
	f.mu.Lock()
	f.members = map[string]string{"g1": "1@a1,2@b1", "g2": "2@b1,3@c1"}
	f.mu.Unlock()
	group := func(id, heads string) AutomaticRequest {
		return AutomaticRequest{Item: Item{Clone: "clone", Group: id}, Repository: "org/repo", Head: heads, Trigger: TriggerStackReviewed}
	}
	if _, err := q.SubmitAutomatic(context.Background(), group("g1", "1@a1,2@b0")); !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("other member heads: %v", err)
	}
	if _, err := q.SubmitAutomatic(context.Background(), group("g1", "1@a1,2@b1")); err != nil {
		t.Fatal(err)
	}
	<-f.started
	if _, err := q.SubmitAutomatic(context.Background(), group("g1", "1@a1,2@b1")); !errors.Is(err, ErrBusy) {
		t.Fatalf("the running group: %v", err)
	}
	// Another group and the members' own reviews are not busy.
	if _, err := q.SubmitAutomatic(context.Background(), group("g2", "2@b1,3@c1")); err != nil {
		t.Fatalf("another group: %v", err)
	}
	if _, err := q.SubmitAutomatic(context.Background(), auto(1, "a1")); err != nil {
		t.Fatalf("a member's review: %v", err)
	}
	f.release <- struct{}{}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the group job did not finish: %+v", q.List())
		}
		if _, err := q.SubmitAutomatic(context.Background(), group("g1", "1@a1,2@b1")); !errors.Is(err, ErrBusy) {
			break
		}
	}
	if _, err := q.SubmitAutomatic(context.Background(), group("g1", "1@a1,2@b1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("the same member heads again: %v", err)
	}
	close(f.release)
}

// loaded is the loaded job with this id.
func loaded(t *testing.T, q *Queue, id string) Job {
	t.Helper()
	for _, j := range q.List() {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("no job %s", id)
	return Job{}
}

// A job a person started goes ahead of every automatic job still waiting,
// whenever it was queued.
func TestPersonsJobsGoAheadOfWaitingAutomaticOnes(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	running, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	waiting, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 3})
	if _, err := q.Submit(ctx, e.ID, true, false); err != nil {
		t.Fatal(err)
	}
	f.release <- struct{}{}
	waitState(t, q, running.ID, "completed")
	if p := <-f.started; p.PR != 3 {
		t.Fatalf("started PR %d, want the person's PR 3", p.PR)
	}
	f.release <- struct{}{}
	if p := <-f.started; p.PR != 2 {
		t.Fatalf("started PR %d, want the automatic PR 2", p.PR)
	}
	f.release <- struct{}{}
	waitState(t, q, waiting.ID, "completed")
}

// A new head of a pull request whose automatic review waits in the queue
// replaces it in place: the waiting job takes the new head and keeps its id,
// its creation time and its place in line. A running job, or a person's
// queued one, keeps the pull request busy.
func TestAutomaticNewHeadReplacesItsWaitingReviewInPlace(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	q.SetClock(func() time.Time { return now })
	setHead := func(pr int, head string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.heads[pr] = head
	}
	running, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	first, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := q.SubmitAutomatic(ctx, auto(3, "c1")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	setHead(2, "b2")
	second, err := q.SubmitAutomatic(ctx, auto(2, "b2"))
	if err != nil {
		t.Fatalf("new head of a waiting review: %v", err)
	}
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) || second.HeadSHA != "b2" || second.State != "queued" {
		t.Fatalf("replacement %+v, want job %s at b2 created %v", second, first.ID, first.CreatedAt)
	}
	if j := loaded(t, q, first.ID); j.HeadSHA != "b2" || j.State != "queued" {
		t.Fatalf("waiting job %+v", j)
	}
	if n := len(q.List()); n != 3 {
		t.Fatalf("%d jobs, want the replacement to add none: %+v", n, q.List())
	}
	setHead(1, "a2")
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a2")); !errors.Is(err, ErrBusy) {
		t.Fatalf("new head of a running review: %v", err)
	}
	f.release <- struct{}{}
	waitState(t, q, running.ID, "completed")
	if p := <-f.started; p.PR != 2 || p.HeadSHA != "b2" {
		t.Fatalf("started %d@%s, want PR 2's new head in PR 2's place", p.PR, p.HeadSHA)
	}
	f.release <- struct{}{}
	waitState(t, q, second.ID, "completed")
	<-f.started
	// A person's queued review of PR 1 is never replaced.
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	manual, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	setHead(1, "a3")
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a3")); !errors.Is(err, ErrBusy) {
		t.Fatalf("new head beside a person's waiting review: %v", err)
	}
	f.release <- struct{}{}
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, manual[0].ID, "completed")
}

// A person starting the very review an automatic job waits to run makes
// that job theirs: it keeps its id, is no longer automatic and goes ahead
// of automatic work.
func TestPersonsStartPromotesTheWaitingAutomaticReview(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	running, err := q.SubmitAutomatic(ctx, auto(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	ahead, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := q.SubmitAutomatic(ctx, auto(3, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 3})
	out, err := q.Submit(ctx, e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != waiting.ID || out[0].Automatic || out[0].Trigger != "" {
		t.Fatalf("person's start %+v, want the waiting job made theirs", out)
	}
	if j := loaded(t, q, waiting.ID); j.Automatic || j.State != "queued" {
		t.Fatalf("promoted job %+v", j)
	}
	f.release <- struct{}{}
	waitState(t, q, running.ID, "completed")
	if p := <-f.started; p.PR != 3 {
		t.Fatalf("started PR %d, want the promoted PR 3", p.PR)
	}
	f.release <- struct{}{}
	<-f.started
	f.release <- struct{}{}
	waitState(t, q, ahead.ID, "completed")
}

// A group's waiting automatic job is never taken over by new member heads:
// it waits for its members, and its recheck follows their moves.
func TestAutomaticGroupWaitingJobIsNotTakenOver(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	if _, err := q.SubmitAutomatic(ctx, auto(3, "c1")); err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.mu.Lock()
	f.members = map[string]string{"g1": "1@a1,2@b1"}
	f.mu.Unlock()
	group := func(heads string) AutomaticRequest {
		return AutomaticRequest{Item: Item{Clone: "clone", Group: "g1"}, Repository: "org/repo", Head: heads, Trigger: TriggerStackReviewed}
	}
	waiting, err := q.SubmitAutomatic(ctx, group("1@a1,2@b1"))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.members["g1"] = "1@a2,2@b1"
	f.mu.Unlock()
	if _, err := q.SubmitAutomatic(ctx, group("1@a2,2@b1")); !errors.Is(err, ErrBusy) {
		t.Fatalf("new member heads of a waiting group job: %v", err)
	}
	if j := loaded(t, q, waiting.ID); j.Members != "1@a1,2@b1" || j.State != "queued" {
		t.Fatalf("waiting group job %+v", j)
	}
	close(f.release)
}

// A failed journal write leaves the queue as it was: a waiting automatic
// review keeps its head when a new head could not be saved, and stays
// automatic when a person's start of it could not be.
func TestTakeOverAndPromotionRollBackOnAFailedWrite(t *testing.T) {
	q, f := newAutoQueue(t)
	ctx := context.Background()
	if _, err := q.SubmitAutomatic(ctx, auto(1, "a1")); err != nil {
		t.Fatal(err)
	}
	<-f.started
	waiting, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	restore := failSaves(t, q, time.Millisecond)
	f.mu.Lock()
	f.heads[2] = "b2"
	f.mu.Unlock()
	if _, err := q.SubmitAutomatic(ctx, auto(2, "b2")); err == nil {
		t.Fatal("new head taken over without a journal write")
	}
	if j := loaded(t, q, waiting.ID); j.HeadSHA != "b1" || !j.Automatic || j.State != "queued" {
		t.Fatalf("after a failed take-over %+v", j)
	}
	f.mu.Lock()
	f.heads[2] = "b1"
	f.mu.Unlock()
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 2})
	if _, err := q.Submit(ctx, e.ID, true, false); err == nil {
		t.Fatal("promoted without a journal write")
	}
	if j := loaded(t, q, waiting.ID); !j.Automatic || j.Trigger != TriggerNewHead {
		t.Fatalf("after a failed promotion %+v", j)
	}
	restore()
	close(f.release)
	waitState(t, q, waiting.ID, "completed")
	if j := loaded(t, q, waiting.ID); j.HeadSHA != "b1" || !j.Automatic {
		t.Fatalf("finished %+v", j)
	}
}
