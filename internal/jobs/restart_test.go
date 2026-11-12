package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// noneStarts fails if the executor starts a job within a short wait.
func noneStarts(t *testing.T, started <-chan Prepared, what string) {
	t.Helper()
	select {
	case p := <-started:
		t.Fatalf("%s: PR %d started", what, p.PR)
	case <-time.After(100 * time.Millisecond):
	}
}

func jobState(q *Queue, id string) string {
	for _, j := range q.List() {
		if j.ID == id {
			return j.State
		}
	}
	return ""
}

// A restart waits for the running job only: the queued backlog, and a job
// submitted while it waits, stay queued and never start in this process.
func TestDrainRunningWaitsOnlyForRunningJobs(t *testing.T) {
	ctx := context.Background()
	f := &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	running, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 1}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	var queued []Job
	for _, pr := range []int{2, 3} {
		jobs, err := q.Generate(ctx, []Item{{Clone: "clone", PR: pr}}, "single", false)
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, jobs...)
	}
	if q.DrainRunning() {
		t.Fatal("drained while a job runs")
	}
	// Room for every job: the hold, not the limit, keeps them queued.
	if err := q.SetConcurrency(4); err != nil {
		t.Fatal(err)
	}
	// Accepted while the restart waits, and queued behind the hold.
	late, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 4}}, "single", false)
	if err != nil {
		t.Fatal("refused while waiting for running jobs:", err)
	}
	queued = append(queued, late...)
	noneStarts(t, f.started, "while held")
	releaseJob(t, f)
	waitState(t, q, running[0].ID, "completed")
	if !q.DrainRunning() {
		t.Fatal("not drained once no job runs")
	}
	if _, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 5}}, "single", false); !errors.Is(err, ErrRestarting) {
		t.Fatalf("submitted after the drain: %v", err)
	}
	noneStarts(t, f.started, "after the drain")
	for _, j := range queued {
		if state := jobState(q, j.ID); state != "queued" {
			t.Fatalf("PR %d is %s, want queued", j.PR, state)
		}
		if state := journalState(q, j.ID); state != "queued" {
			t.Fatalf("PR %d journaled %s, want queued", j.PR, state)
		}
	}
}

// A restart that led nowhere starts the held jobs again.
func TestResumeStartsHeldJobs(t *testing.T) {
	ctx := context.Background()
	q, f := newQueue(t)
	running, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 1}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	held, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 2}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	if q.DrainRunning() {
		t.Fatal("drained while a job runs")
	}
	releaseJob(t, f)
	waitState(t, q, running[0].ID, "completed")
	noneStarts(t, f.started, "while held")
	if !q.DrainRunning() {
		t.Fatal("not drained once no job runs")
	}
	q.Resume()
	select {
	case p := <-f.started:
		if p.PR != 2 {
			t.Fatalf("PR %d started", p.PR)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held job did not start after Resume")
	}
	releaseJob(t, f)
	waitState(t, q, held[0].ID, "completed")
}

// Queued confirmed and automatic jobs survive the restart and run in the next
// process, confirmed first, each exactly once.
func TestQueuedJobsRunOnceAfterTheRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateDir(t), "jobs.json")
	heads := map[int]string{1: "a1", 2: "b1", 3: "c1"}
	before := &autoExecutor{heads: heads, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(path, before, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	q.SetAutomaticGate(func(Job) Admission { return Admission{} })
	running, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 1}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	<-before.started
	automatic, err := q.SubmitAutomatic(ctx, auto(2, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := q.Generate(ctx, []Item{{Clone: "clone", PR: 3}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	if q.DrainRunning() {
		t.Fatal("drained while a job runs")
	}
	before.release <- struct{}{}
	waitState(t, q, running[0].ID, "completed")
	if !q.DrainRunning() {
		t.Fatal("not drained once no job runs")
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	noneStarts(t, before.started, "in the old process")

	after := &autoExecutor{heads: heads, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{})}
	next, err := Open(path, after, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	next.SetAutomaticGate(func(Job) Admission { return Admission{} })
	for _, want := range []Job{manual[0], automatic} {
		select {
		case p := <-after.started:
			if p.PR != want.PR {
				t.Fatalf("PR %d started, want %d", p.PR, want.PR)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("PR %d did not start after the restart", want.PR)
		}
		after.release <- struct{}{}
		waitState(t, next, want.ID, "completed")
	}
	noneStarts(t, after.started, "after the backlog")
	jobs := next.List()
	if len(jobs) != 3 {
		t.Fatalf("%d jobs after the restart: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if j.State != "completed" {
			t.Fatalf("PR %d is %s", j.PR, j.State)
		}
	}
}

// DrainRunning and Submit race: a job accepted first stays queued and never
// starts once the queue drained; a later one is refused; a job that started
// first keeps the queue from draining.
func TestDrainRunningAndSubmitNeverStartAJob(t *testing.T) {
	for range 50 {
		q, f := newQueue(t)
		e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
		var drained bool
		var submitted []Job
		var submitErr error
		var wg sync.WaitGroup
		wg.Go(func() { drained = q.DrainRunning() })
		wg.Go(func() { submitted, submitErr = q.Submit(context.Background(), e.ID, true, false) })
		wg.Wait()
		// Submit prepares outside the lock, so the drain usually wins;
		// every outcome must still keep the invariants.
		switch {
		case drained && submitErr != nil && !errors.Is(submitErr, ErrRestarting):
			t.Fatalf("drained, and submit failed: %v", submitErr)
		case drained && submitErr == nil:
			noneStarts(t, f.started, "drained")
			if state := jobState(q, submitted[0].ID); state != "queued" {
				t.Fatalf("drained, and the job is %s", state)
			}
		case !drained && submitErr != nil:
			t.Fatalf("neither drained nor submitted: %v", submitErr)
		case !drained:
			<-f.started
			close(f.release)
		}
		q.Close()
	}
}

// pausedMembers pauses the preparation of a group's member reviews, between
// the group's own preparation and the queuing of its members.
type pausedMembers struct {
	*fakeExecutor
	entered, resume chan struct{}
}

func (p *pausedMembers) Prepare(ctx context.Context, item Item) (Prepared, string, error) {
	if item.Group == "" && item.PR != 0 {
		select {
		case p.entered <- struct{}{}:
			<-p.resume
		default:
		}
	}
	return p.fakeExecutor.Prepare(ctx, item)
}

// A restart does not drain while a person's group generation queues its
// members' reviews and then its own job, so it never accepts only half of
// it; one asked for after the drain is refused whole.
func TestDrainRunningWaitsForAGroupGeneration(t *testing.T) {
	ctx := context.Background()
	f := &pausedMembers{
		fakeExecutor: &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{}), awaiting: []int{1, 2}},
		entered:      make(chan struct{}),
		resume:       make(chan struct{}),
	}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), f, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	generated := make(chan error, 1)
	go func() {
		_, err := q.Generate(ctx, []Item{{Clone: "clone", Group: "g1"}}, "single", false)
		generated <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the members were never prepared")
	}
	if q.DrainRunning() {
		t.Fatal("drained in the middle of a group generation")
	}
	close(f.resume)
	if err := <-generated; err != nil {
		t.Fatal(err)
	}
	if !q.DrainRunning() {
		t.Fatal("not drained after the group generation")
	}
	jobs := q.List()
	if len(jobs) != 3 {
		t.Fatalf("%d jobs, want the group and its two members: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if j.State != "queued" {
			t.Fatalf("PR %d group %q is %s", j.PR, j.Group, j.State)
		}
	}
	noneStarts(t, f.started, "held")
	if _, err := q.Generate(ctx, []Item{{Clone: "clone", Group: "g2"}}, "single", false); !errors.Is(err, ErrRestarting) {
		t.Fatalf("group generated after the drain: %v", err)
	}
	if n := len(q.List()); n != 3 {
		t.Fatalf("%d jobs after a refused group generation", n)
	}
}
