package console

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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
