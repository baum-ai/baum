//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/config"
)

// A session the agent cannot find any more is forgotten: the turn fails with
// the reason, the token is replaced by one bound to no session, and the next
// turn starts a new session instead of resuming the lost one.
func TestALostSessionStartsAgain(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	before := f.liveOf(c.ID)
	oldConsumer := before.consumer
	if err := os.WriteFile(filepath.Join(f.log, "gone"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "still there?", nil); err != nil {
		t.Fatal(err)
	}
	if e := f.finished(c.ID); e.State != StateFailed {
		t.Fatalf("finished %+v", e)
	}
	got, _ := f.service.Get(ctx, c.ID)
	if got.Session != "" || got.Error != ReasonSessionGone {
		t.Fatalf("after the lost session %+v", got)
	}
	if _, ok := f.opts.Registry.Consumer(oldConsumer); ok {
		t.Error("the token bound to the lost session is still live")
	}
	now := f.liveOf(c.ID)
	if consumer, ok := f.opts.Registry.Consumer(now.consumer); !ok || consumer.Session != "" {
		t.Fatalf("the new token %+v %v", consumer, ok)
	}
	if err := os.Remove(filepath.Join(f.log, "gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "start again", nil); err != nil {
		t.Fatal(err)
	}
	if e := f.finished(c.ID); e.State != StateIdle {
		t.Fatalf("finished %+v", e)
	}
	if args := f.read("arguments.txt"); strings.Contains(args, "--resume") {
		t.Fatalf("the new turn resumed: %s", args)
	}
	got, _ = f.service.Get(ctx, c.ID)
	if got.Session != testSession {
		t.Fatalf("the new session was not kept: %+v", got)
	}
	if consumer, _ := f.opts.Registry.Consumer(f.liveOf(c.ID).consumer); consumer.Session != agentcontext.SessionKey(AgentClaudeCode, testSession) {
		t.Fatalf("the new token was not bound: %+v", consumer)
	}
}

// A page the MCP server would refuse is refused when a conversation opens.
func TestCreateRefusesAnInvalidPage(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: &agentcontext.PageContext{Page: "nowhere"}})
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("an unknown page: %v", err)
	}
	if list, _ := f.service.List(context.Background()); len(list) != 0 {
		t.Fatalf("a refused conversation was saved: %+v", list)
	}
}

// Once shutdown begins, no conversation opens and no message is sent.
func TestShutdownRefusesNewWork(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	f.service.Shutdown()
	if _, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode}); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("create after shutdown: %v", err)
	}
	if _, err := f.service.Send(ctx, c.ID, "hello", nil); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("send after shutdown: %v", err)
	}
	if len(f.service.live) != 0 {
		t.Errorf("tokens outlived shutdown: %d", len(f.service.live))
	}
}

// The text a turn keeps is bounded, keeping its end.
func TestProgressKeepsTheEndOfALongAnswer(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Publish = nil })
	l := &live{progress: Progress{Conversation: "c"}, revoke: func() {}}
	f.service.mu.Lock()
	f.service.live["c"] = l
	f.service.mu.Unlock()
	f.service.textDelta("c", l, strings.Repeat("a", agent.MaxConverseBytes))
	f.service.textDelta("c", l, "é the end")
	progress, ok := f.service.Progress("c")
	if !ok || len(progress.Text) > agent.MaxConverseBytes || !strings.HasSuffix(progress.Text, "é the end") {
		t.Fatalf("progress is %d bytes ending %q", len(progress.Text), progress.Text[max(0, len(progress.Text)-12):])
	}
	f.service.transcript("c", l, agent.TranscriptEntry{Kind: "message", Text: strings.Repeat("b", agent.MaxConverseBytes) + "!"})
	if progress, _ := f.service.Progress("c"); len(progress.Text) > agent.MaxConverseBytes || !strings.HasSuffix(progress.Text, "b!") {
		t.Fatalf("progress is %d bytes", len(progress.Text))
	}
	if got := tail("aé", 1); got != "" {
		t.Errorf("tail cut a rune: %q", got)
	}
}

// The agent's backend is built once for an agent, model, executable and
// agent environment, whichever conversation and access use it.
func TestBackendsAreBuiltOnce(t *testing.T) {
	var mu sync.Mutex
	builds := 0
	f := newFixture(t, func(o *Options) {
		o.Backend = func(agentName, model string, cfg config.Config) (agent.Converser, string, error) {
			mu.Lock()
			builds++
			mu.Unlock()
			return localBackend(agentName, model, cfg)
		}
	})
	ctx := context.Background()
	for _, mode := range []string{ModeFull, ModeWorkspace} {
		c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := f.service.Send(ctx, c.ID, "hello", nil); err != nil {
				t.Fatal(err)
			}
			f.finished(c.ID)
		}
	}
	other, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Model: new("other")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, other.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(other.ID)
	mu.Lock()
	defer mu.Unlock()
	if builds != 2 {
		t.Fatalf("built %d backends, want one per model", builds)
	}
}
