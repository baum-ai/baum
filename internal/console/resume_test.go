//go:build unix

package console

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateResumesASessionInTheDirectoryItWorkedIn(t *testing.T) {
	dir := resolveExisting(t.TempDir())
	asked := ""
	f := newFixture(t, func(o *Options) {
		o.SessionDirectory = func(_ context.Context, agentName, id string) (string, error) {
			asked = agentName + ":" + id
			return dir, nil
		}
	})
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil {
		t.Fatal(err)
	}
	if asked != "claude-code:"+testSession || c.Directory != dir || c.Session != testSession || !c.Resumed {
		t.Fatalf("asked %q, conversation %+v", asked, c)
	}
	// Continuing it again opens the same conversation.
	again, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil || again.ID != c.ID {
		t.Fatalf("again: %+v %v", again, err)
	}
	// The directory is the person's: their other sessions there stay theirs,
	// and the resumed one is the conversation's.
	if got, ok := f.service.Owns("claude-code", "someone", dir); ok {
		t.Errorf("a person's own session was claimed for %s", got)
	}
	if got, ok := f.service.Owns("claude-code", testSession, dir); !ok || got != c.ID {
		t.Errorf("the resumed session: %q %v", got, ok)
	}
	// The first turn resumes the session, in its directory.
	if _, err := f.service.Send(ctx, c.ID, "carry on", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	if args := f.read("arguments.txt"); !strings.Contains(args, "--resume\n"+testSession) {
		t.Errorf("arguments: %s", args)
	}
	if pwd := strings.TrimSpace(f.read("pwd.txt")); pwd != dir {
		t.Errorf("pwd = %s", pwd)
	}
	// It survives a restart as resumed.
	_ = f.service.CloseStore()
	f.open()
	if _, ok := f.service.Owns("claude-code", "someone", dir); ok {
		t.Error("a person's own session was claimed after a restart")
	}
}

func TestCreateRefusesASessionItCannotResume(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	for name, find := range map[string]func(context.Context, string, string) (string, error){
		"no transcript": func(context.Context, string, string) (string, error) {
			return "", errors.New("Codex has no transcript of this session on this computer.")
		},
		"directory gone": func(context.Context, string, string) (string, error) { return gone, nil },
	} {
		f := newFixture(t, func(o *Options) { o.SessionDirectory = find })
		_, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Resume: "s1"})
		var invalidErr *InvalidError
		if !errors.As(err, &invalidErr) {
			t.Errorf("%s: %v", name, err)
		}
		if list, _ := f.service.List(context.Background()); len(list) != 0 {
			t.Errorf("%s: a conversation was saved", name)
		}
	}
	f := newFixture(t, nil)
	if _, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Resume: "s1"}); err == nil {
		t.Error("resumed without a way to find the session")
	}
}

func TestAClosedResumedConversationGivesItsSessionBack(t *testing.T) {
	dir := resolveExisting(t.TempDir())
	f := newFixture(t, func(o *Options) {
		o.SessionDirectory = func(context.Context, string, string) (string, error) { return dir, nil }
	})
	ctx := context.Background()
	first, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Close(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.service.Owns("claude-code", testSession, dir); ok {
		t.Fatalf("a closed conversation still claims the session: %s", got)
	}
	// Continued again, the new conversation has it, after a restart too.
	second, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil || second.ID == first.ID {
		t.Fatalf("%+v %v", second, err)
	}
	_ = f.service.CloseStore()
	f.open()
	if got, ok := f.service.Owns("claude-code", testSession, dir); !ok || got != second.ID {
		t.Errorf("after a restart: %q %v", got, ok)
	}
}

func TestTwoRequestsToContinueASessionMakeOneConversation(t *testing.T) {
	dir := resolveExisting(t.TempDir())
	f := newFixture(t, func(o *Options) {
		o.SessionDirectory = func(context.Context, string, string) (string, error) {
			time.Sleep(20 * time.Millisecond)
			return dir, nil
		}
	})
	var wg sync.WaitGroup
	ids := make([]string, 2)
	for i := range ids {
		wg.Go(func() {
			c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
			if err != nil {
				t.Error(err)
			}
			ids[i] = c.ID
		})
	}
	wg.Wait()
	if ids[0] != ids[1] {
		t.Errorf("two conversations: %v", ids)
	}
}

func TestAResumedConversationKeepsItsSessionFromAnOlderClosedOne(t *testing.T) {
	dir := resolveExisting(t.TempDir())
	f := newFixture(t, func(o *Options) {
		o.SessionDirectory = func(context.Context, string, string) (string, error) { return dir, nil }
	})
	ctx := context.Background()
	// A panel conversation that ran the session, closed.
	older, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, older.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(older.ID)
	if _, err := f.service.Close(ctx, older.ID); err != nil {
		t.Fatal(err)
	}
	// Updated later than the closed one, as the store orders them.
	time.Sleep(5 * time.Millisecond)
	resumed, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil || resumed.ID == older.ID {
		t.Fatalf("%+v %v", resumed, err)
	}
	_ = f.service.CloseStore()
	f.open()
	if got, ok := f.service.Owns("claude-code", testSession, dir); !ok || got != resumed.ID {
		t.Errorf("after a restart: %q %v", got, ok)
	}
	// Closed, it gives the session back for good.
	if _, err := f.service.Close(ctx, resumed.ID); err != nil {
		t.Fatal(err)
	}
	_ = f.service.CloseStore()
	f.open()
	if got, ok := f.service.Owns("claude-code", testSession, dir); ok {
		t.Errorf("claimed again by %s", got)
	}
}

func TestATurnWaitsWhileATerminalRunsTheSession(t *testing.T) {
	dir := resolveExisting(t.TempDir())
	var running atomic.Bool
	f := newFixture(t, func(o *Options) {
		o.SessionDirectory = func(context.Context, string, string) (string, error) { return dir, nil }
		o.SessionRunning = func(agent, id string, _ time.Time) bool { return running.Load() && id == testSession }
	})
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Resume: testSession})
	if err != nil {
		t.Fatal(err)
	}
	// The person resumed it in their terminal too.
	running.Store(true)
	if _, err := f.service.Send(ctx, c.ID, "hello", nil); !errors.Is(err, ErrRunningElsewhere) {
		t.Fatalf("sent while a terminal runs the session: %v", err)
	}
	if got, _ := f.service.Get(ctx, c.ID); got.State != StateIdle {
		t.Errorf("state %s", got.State)
	}
	// Once they exit it, the panel goes on.
	running.Store(false)
	if _, err := f.service.Send(ctx, c.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
}
