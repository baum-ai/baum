package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/chat/chattest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/store"
)

// fakeChat stores turns in memory.
type fakeChat struct {
	mu       sync.Mutex
	turns    int
	beginErr error
	settled  []settledTurn
	jobs     map[string]string
}

type settledTurn struct {
	ref           chat.Ref
	state, reason string
	cost          *float64
}

func (f *fakeChat) Begin(_ context.Context, a chat.Ask, jobID string, _ store.Ended) (chat.Begun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		return chat.Begun{}, f.beginErr
	}
	f.turns++
	return chat.Begun{Ref: chat.Ref{Clone: a.Clone, PR: a.PR, Conversation: fmt.Sprintf("c%d", f.turns), Turn: 1}, Repository: "org/repo", Version: a.Version, Backend: "claude", Model: "m"}, nil
}

func (f *fakeChat) Conversations(_ context.Context, _ string, _ int, ended store.Ended) ([]store.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = map[string]string{}
	for _, id := range []string{"gone", "running"} {
		if state, _, done := ended(id); done {
			f.jobs[id] = state
		}
	}
	return nil, nil
}

func (f *fakeChat) Settle(_ context.Context, ref chat.Ref, state, reason string, cost *float64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled = append(f.settled, settledTurn{ref, state, reason, cost})
	return nil
}

func (f *fakeChat) settledTurns() []settledTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]settledTurn(nil), f.settled...)
}

// chatExecutor runs reviews like fakeExecutor and chat jobs with run.
type chatExecutor struct {
	*fakeExecutor
	run func(context.Context, Prepared, func(Progress)) (*float64, error)
}

func (e chatExecutor) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	if p.Kind == KindChat {
		return e.run(ctx, p, progress)
	}
	return e.fakeExecutor.Execute(ctx, p, progress)
}

func chatQueue(t *testing.T, run func(context.Context, Prepared, func(Progress)) (*float64, error), changed func(Job)) (*Queue, *fakeExecutor, *fakeChat) {
	t.Helper()
	price := 2.0
	f := &fakeExecutor{head: "first", price: &price, started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), chatExecutor{f, run}, 1, changed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	service := &fakeChat{}
	q.SetChat(service)
	return q, f, service
}

func ask(t *testing.T, q *Queue) ChatQueued {
	t.Helper()
	queued, err := q.Ask(context.Background(), chat.Ask{Clone: "clone", PR: 7, Version: 1, Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	return queued
}

// A question runs in its own lane while a review holds the only review
// worker, and its events go out when it is queued and when it settles.
func TestChatJobsRunBesideAReview(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	ran := make(chan Prepared, 1)
	q, f, service := chatQueue(t, func(_ context.Context, p Prepared, progress func(Progress)) (*float64, error) {
		progress(Progress{Phase: "answering", Calls: 1, Tokens: 5})
		ran <- p
		cost := 0.25
		return &cost, nil
	}, func(j Job) {
		if event, ok := ChatEvent(j); ok {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	review := estimate(t, q, "single", Item{Clone: "clone", PR: 7})
	if _, err := q.Submit(context.Background(), review.ID, true, false); err != nil {
		t.Fatal(err)
	}
	<-f.started
	queued := ask(t, q)
	if queued.Job.Kind != KindChat || queued.Job.Chat != (ChatInputs{Conversation: "c1", Turn: 1, Version: 1}) || queued.Job.State != "queued" || queued.Job.Repository != "org/repo" {
		t.Fatalf("queued: %+v", queued)
	}
	select {
	case p := <-ran:
		if p.Chat.Conversation != "c1" {
			t.Errorf("ran %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question waited behind the review")
	}
	j := waitFinished(t, q, queued.Job.ID)
	if j.State != "completed" || j.ActualCostUSD == nil || *j.ActualCostUSD != 0.25 || j.Calls != 1 {
		t.Errorf("job: %+v", j)
	}
	if len(service.settledTurns()) != 0 {
		t.Errorf("a completed turn was settled again: %+v", service.settledTurns())
	}
	close(f.release)
	// The completed event is recorded a moment after the job reads as done.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 2 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0]["state"] != "queued" || events[1]["state"] != "completed" || events[1]["conversation"] != "c1" || events[1]["turn"] != 1 || events[1]["pr"] != 7 {
		t.Errorf("events: %+v", events)
	}
	if raw, _ := json.Marshal(events[1]); len(raw) > 1024 {
		t.Errorf("event too large: %s", raw)
	}
}

// Cancelling a running or queued question settles its turn as cancelled.
func TestCancelSettlesTheTurn(t *testing.T) {
	started := make(chan string, 4)
	q, _, service := chatQueue(t, func(ctx context.Context, p Prepared, _ func(Progress)) (*float64, error) {
		started <- p.Chat.Conversation
		<-ctx.Done()
		return nil, &generate.Error{Category: generate.CategoryInterrupted, Err: ctx.Err()}
	}, nil)
	var running []ChatQueued
	for range ChatWorkers {
		running = append(running, ask(t, q))
		<-started
	}
	waiting := ask(t, q)
	if _, err := q.Cancel(waiting.Job.ID); err != nil {
		t.Fatal(err)
	}
	if j := waitState(t, q, waiting.Job.ID, "cancelled"); j.Phase != "finished" {
		t.Errorf("queued cancel: %+v", j)
	}
	if _, err := q.Cancel(running[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	if j := waitFinished(t, q, running[0].Job.ID); j.State != "cancelled" || j.Reason != "interrupted" {
		t.Errorf("running cancel: %+v", j)
	}
	settled := waitSettledTurns(t, service, 2)
	if len(settled) != 2 || settled[0].ref.Conversation != waiting.Conversation || settled[0].state != store.TurnCancelled ||
		settled[1].ref.Conversation != running[0].Conversation || settled[1].state != store.TurnCancelled || settled[1].reason != "interrupted" {
		t.Errorf("settled: %+v", settled)
	}
}

// A failed question's job and turn carry a reason from the fixed list.
func TestChatFailureReasons(t *testing.T) {
	for want, err := range map[string]error{
		ReasonNeedsClone: chat.ErrNeedsClone,
		"budget":         &generate.Error{Category: generate.CategoryBudget, Err: errors.New("generation budget exceeded: tokens")},
		"backend":        &generate.Error{Category: generate.CategoryBackend, Err: errors.New("the model provider was unavailable")},
		"validation":     &generate.Error{Category: generate.CategoryValidation, Err: errors.New("no")},
		"internal":       errors.New("disk full"),
	} {
		t.Run(want, func(t *testing.T) {
			q, _, service := chatQueue(t, func(context.Context, Prepared, func(Progress)) (*float64, error) {
				cost := 0.5
				return &cost, err
			}, nil)
			queued := ask(t, q)
			j := waitFinished(t, q, queued.Job.ID)
			if j.State != "failed" || j.Reason != want || j.ActualCostUSD == nil {
				t.Errorf("job: %+v", j)
			}
			// The turn is settled right after the job, before its event.
			settled := waitSettledTurns(t, service, 1)
			if len(settled) != 1 || settled[0].state != store.TurnFailed || settled[0].reason != want || settled[0].cost == nil || *settled[0].cost != 0.5 {
				t.Errorf("settled: %+v", settled)
			}
		})
	}
}

// A question is refused without a job when the conversation is busy or the
// daemon is restarting; a chat item cannot be estimated like a review.
func TestAskRefusals(t *testing.T) {
	q, _, service := chatQueue(t, func(context.Context, Prepared, func(Progress)) (*float64, error) { return nil, nil }, nil)
	service.beginErr = chat.ErrBusy
	if _, err := q.Ask(context.Background(), chat.Ask{}); !errors.Is(err, chat.ErrBusy) {
		t.Errorf("busy: %v", err)
	}
	service.beginErr = nil
	if !q.Drain() {
		t.Fatal("not idle")
	}
	if _, err := q.Ask(context.Background(), chat.Ask{}); !errors.Is(err, ErrRestarting) {
		t.Errorf("draining: %v", err)
	}
	q.Resume()
	if len(q.List()) != 0 {
		t.Errorf("jobs: %+v", q.List())
	}
	if _, err := q.Estimate(context.Background(), []Item{{Clone: "clone", PR: 7, Kind: KindChat}}, "single"); err == nil {
		t.Error("a chat item was estimated")
	}
	if _, err := q.Generate(context.Background(), []Item{{Clone: "clone", PR: 7, Kind: KindChat}}, "single", false); err == nil {
		t.Error("a chat item was generated")
	}
	plain, _ := newQueue(t)
	if _, err := plain.Ask(context.Background(), chat.Ask{}); !errors.Is(err, ErrChatUnavailable) {
		t.Errorf("no chat service: %v", err)
	}
}

// A question a stopped daemon left running reads as interrupted: its job
// is, and the turn's job counts as stale.
func TestRestartLeavesARunningQuestionInterrupted(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "jobs.json")
	raw, _ := json.Marshal([]Job{{ID: "running", State: "running", Prepared: Prepared{Item: Item{Clone: "clone", PR: 7, Kind: KindChat}, Chat: ChatInputs{Conversation: "c1", Turn: 1, Version: 1}}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	price := 2.0
	q, err := Open(path, &fakeExecutor{price: &price, started: make(chan Prepared, 1), release: make(chan struct{})}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	service := &fakeChat{}
	q.SetChat(service)
	if j := q.List()[0]; j.State != "interrupted" {
		t.Errorf("job: %+v", j)
	}
	if _, err := q.Conversations(context.Background(), "clone", 7); err != nil {
		t.Fatal(err)
	}
	if len(service.jobs) != 2 || service.jobs["running"] != "interrupted" || service.jobs["gone"] != "interrupted" {
		t.Errorf("jobs: %+v", service.jobs)
	}
}

// End to end through the local executor: the job keeps the agent's
// transcript, records its cost and the turn's answer.
func TestLocalChatJobKeepsTranscriptAndCost(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), &Local{Registry: f.Registry, Chat: f.Service}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.SetChat(f.Service)
	queued, err := q.Ask(context.Background(), chat.Ask{Clone: f.Clone.ID, PR: assembletest.Number, Version: 1, Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	j := waitFinished(t, q, queued.Job.ID)
	if j.State != "completed" || !j.HasTranscript || j.CostReason == "" || j.Calls != 1 || j.Accounting == nil {
		t.Fatalf("job: %+v", j)
	}
	lines, _, err := readTranscript(transcriptPath(q.transcripts, j.ID), 0)
	if err != nil || len(lines) != 2 || lines[1].Text != "Reading the code." {
		t.Errorf("transcript: %+v (%v)", lines, err)
	}
	list, err := q.Conversations(context.Background(), f.Clone.ID, assembletest.Number)
	if err != nil || len(list) != 1 || list[0].Turns[0].State != store.TurnCompleted || list[0].Turns[0].JobID != j.ID || list[0].Turns[0].CostReason != j.CostReason {
		t.Errorf("conversations: %+v (%v)", list, err)
	}
}

func waitSettledTurns(t *testing.T, service *fakeChat, n int) []settledTurn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if settled := service.settledTurns(); len(settled) >= n {
			return settled
		}
		time.Sleep(time.Millisecond)
	}
	return service.settledTurns()
}
