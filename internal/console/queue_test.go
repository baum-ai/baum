//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// prompts are the prompts the fake agent was given, oldest first.
func (f *fixture) prompts() []string {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.log, "prompts.txt"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for p := range strings.SplitSeq(string(body), "\n---\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (f *fixture) release() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.log, "release"), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// drainUntil reads the events up to one of kind on the conversation.
func (f *fixture) drainUntil(id, kind string) {
	f.t.Helper()
	for {
		e := <-f.events
		if e.Conversation == id && e.Kind == kind {
			return
		}
	}
}

// Messages sent while the agent answers are queued, and go together, in
// order, as the next turn once it has answered.
func TestMessagesQueuedWhileRunningGoAsTheNextTurn(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "hold on", nil); err != nil {
		t.Fatal(err)
	}
	jobs := agentcontext.PageContext{Page: agentcontext.PageJobs}
	first, err := f.service.Send(ctx, c.ID, " then this ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateRunning || len(first.Queued) != 1 || first.Queued[0].Text != "then this" || first.Queued[0].ID == "" {
		t.Fatalf("after queueing %+v", first)
	}
	f.drainUntil(c.ID, EventQueued)
	if _, err := f.service.Send(ctx, c.ID, "and that", &jobs); err != nil {
		t.Fatal(err)
	}
	removed, err := f.service.Send(ctx, c.ID, "not this", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RemoveQueued(ctx, c.ID, removed.Queued[2].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RemoveQueued(ctx, c.ID, removed.Queued[2].ID); !errors.Is(err, ErrQueuedNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	if _, err := f.service.SendQueued(ctx, c.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("sending the queue while running: %v", err)
	}
	f.release()
	f.finished(c.ID)
	f.finished(c.ID)
	got, err := f.service.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateIdle || got.Error != "" || len(got.Queued) != 0 {
		t.Fatalf("after the queued turn %+v", got)
	}
	if prompts := f.prompts(); len(prompts) != 2 || prompts[1] != "then this\n\nand that" {
		t.Fatalf("prompts %q", prompts)
	}
	// The queued turn is about the page its last message was sent from.
	if last := got.Pages[len(got.Pages)-1]; last.Page != agentcontext.PageJobs {
		t.Fatalf("pages %+v", got.Pages)
	}
	// Sent on its own, it leaves the page the person has open alone.
	f.service.mu.Lock()
	open := f.service.page
	f.service.mu.Unlock()
	if open != nil {
		t.Fatalf("the open page became %+v", open)
	}
}

// A stopped turn leaves the queue for the person: it survives a restart,
// goes before the next message they send, or is sent on its own.
func TestAStoppedTurnHoldsTheQueue(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "wait for it", nil); err != nil {
		t.Fatal(err)
	}
	f.waitFor("child.pid")
	if _, err := f.service.Send(ctx, c.ID, "first queued", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	got, _ := f.service.Get(ctx, c.ID)
	if got.State != StateIdle || got.Error != ReasonStopped || len(got.Queued) != 1 {
		t.Fatalf("after stop %+v", got)
	}
	if err := f.service.CloseStore(); err != nil {
		t.Fatal(err)
	}
	f.open()
	got, _ = f.service.Get(ctx, c.ID)
	if len(got.Queued) != 1 || got.Queued[0].Text != "first queued" {
		t.Fatalf("after a restart %+v", got)
	}
	if _, err := f.service.Send(ctx, c.ID, "and now this", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	prompts := f.prompts()
	if last := prompts[len(prompts)-1]; last != "first queued\n\nand now this" {
		t.Fatalf("prompt %q", last)
	}

	// Sent on its own.
	if _, err := f.service.SendQueued(ctx, c.ID); !errors.Is(err, ErrQueuedNotFound) {
		t.Fatalf("sending an empty queue: %v", err)
	}
	if _, err := f.service.Send(ctx, c.ID, "wait again", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "later", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	if _, err := f.service.SendQueued(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	prompts = f.prompts()
	if last := prompts[len(prompts)-1]; last != "later" {
		t.Fatalf("prompt %q", last)
	}
}

// Closing a conversation drops its queue; the queue is bounded.
func TestQueueLimitsAndClose(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "wait", nil); err != nil {
		t.Fatal(err)
	}
	for i := range MaxQueued {
		if _, err := f.service.Send(ctx, c.ID, "queued", nil); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	var bad *InvalidError
	if _, err := f.service.Send(ctx, c.ID, "one too many", nil); !errors.As(err, &bad) {
		t.Fatalf("over the limit: %v", err)
	}
	closed, err := f.service.Close(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != StateClosed || len(closed.Queued) != 0 {
		t.Fatalf("closed %+v", closed)
	}
	got, _ := f.service.Get(ctx, c.ID)
	if len(got.Queued) != 0 {
		t.Fatalf("stored %+v", got)
	}
}
