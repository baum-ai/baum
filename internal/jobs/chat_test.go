package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/store"
)

// fakeChat records how the turns' jobs read as ended.
type fakeChat struct {
	mu   sync.Mutex
	jobs map[string]string
}

func (f *fakeChat) Conversations(_ context.Context, _ string, _ int, ended store.Ended) ([]store.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = map[string]string{}
	for _, id := range []string{"gone", "running", "queued", "completed"} {
		if state, _, done := ended(id); done {
			f.jobs[id] = state
		}
	}
	return nil, nil
}

// A question a stopped daemon left queued or running reads as interrupted:
// nothing runs it any more, so its job ends interrupted and the turn's job
// counts as ended. A completed job whose turn is still pending reads as
// interrupted too, as does one the journal no longer holds.
func TestRestartLeavesAQuestionInterrupted(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "jobs.json")
	question := Prepared{Item: Item{Clone: "clone", PR: 7, Kind: KindChat}, Chat: ChatInputs{Conversation: "c1", Turn: 1, Version: 1}}
	raw, _ := json.Marshal([]Job{
		{ID: "running", State: "running", Phase: "answering", Prepared: question},
		{ID: "queued", State: "queued", Phase: "queued", Prepared: question},
		{ID: "completed", State: "completed", Phase: "finished", Prepared: question},
	})
	if err := appdb.Write(path, "state", "jobs", raw); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{started: make(chan Prepared, 1), release: make(chan struct{})}
	q, err := Open(path, executor, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	service := &fakeChat{}
	q.SetChat(service)
	for _, j := range q.List() {
		if j.ID == "completed" {
			continue
		}
		if j.State != "interrupted" || j.Reason != "interrupted" || j.Phase != "finished" {
			t.Errorf("job: %+v", j)
		}
	}
	if _, err := q.Conversations(context.Background(), "clone", 7); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gone": "interrupted", "running": "interrupted", "queued": "interrupted", "completed": "interrupted"}
	if len(service.jobs) != len(want) {
		t.Errorf("jobs: %+v", service.jobs)
	}
	for id, state := range want {
		if service.jobs[id] != state {
			t.Errorf("%s reads as %q", id, service.jobs[id])
		}
	}
	select {
	case p := <-executor.started:
		t.Errorf("a question ran: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}
	// The journal keeps them interrupted.
	q.Close()
	again, err := Open(path, executor, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, j := range again.List() {
		if j.ID != "completed" && j.State != "interrupted" {
			t.Errorf("reopened job: %+v", j)
		}
	}
}

// A chat item cannot be estimated or generated, and a queue without a chat
// service lists no conversations.
func TestChatRefusals(t *testing.T) {
	q, _ := newQueue(t)
	if _, err := q.Estimate(context.Background(), []Item{{Clone: "clone", PR: 7, Kind: KindChat}}, "single"); err == nil {
		t.Error("a chat item was estimated")
	}
	if _, err := q.Generate(context.Background(), []Item{{Clone: "clone", PR: 7, Kind: KindChat}}, "single", false); err == nil {
		t.Error("a chat item was generated")
	}
	if _, err := q.Conversations(context.Background(), "clone", 7); !errors.Is(err, ErrChatUnavailable) {
		t.Errorf("no chat service: %v", err)
	}
	if len(q.List()) != 0 {
		t.Errorf("jobs: %+v", q.List())
	}
}
