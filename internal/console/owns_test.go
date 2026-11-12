//go:build unix

package console

import (
	"context"
	"path/filepath"
	"testing"
)

// A session belongs to a conversation by its id once the agent named it, or
// by a directory baum made; a person's own worktree a conversation runs in
// is claimed by session only. The index survives a restart.
func TestOwnsMatchesSessionsAndBaumsDirectories(t *testing.T) {
	r := newRepository(t)
	existing := filepath.Join(filepath.Dir(r.clone), "feature-worktree")
	git(t, r.clone, "worktree", "add", "-q", existing, "feature")
	f := pullFixture(t, r)
	ctx := context.Background()
	neutral, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
	if err != nil || theirs.Directory != existing {
		t.Fatalf("%+v %v", theirs, err)
	}
	for _, cwd := range []string{neutral.Directory, filepath.Join(neutral.Directory, "sub")} {
		if got, ok := f.service.Owns("claude-code", "someone", cwd); !ok || got != neutral.ID {
			t.Errorf("%s: %q %v", cwd, got, ok)
		}
	}
	if _, ok := f.service.Owns("claude-code", "someone", existing); ok {
		t.Error("a person's own session in their worktree was claimed")
	}
	if _, ok := f.service.Owns("claude-code", "someone", filepath.Dir(neutral.Directory)); ok {
		t.Error("a directory above the conversation's was claimed")
	}
	if _, err := f.service.Send(ctx, theirs.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	f.finished(theirs.ID)
	if got, ok := f.service.Owns("claude-code", testSession, existing); !ok || got != theirs.ID {
		t.Errorf("the conversation's own session: %q %v", got, ok)
	}
	if _, ok := f.service.Owns("codex", testSession, ""); ok {
		t.Error("another agent's session of the same id was claimed")
	}
	if err := f.service.CloseStore(); err != nil {
		t.Fatal(err)
	}
	f.open()
	if got, ok := f.service.Owns("claude-code", testSession, ""); !ok || got != theirs.ID {
		t.Errorf("after a restart: %q %v", got, ok)
	}
	if got, ok := f.service.Owns("claude-code", "", neutral.Directory); !ok || got != neutral.ID {
		t.Errorf("after a restart, by directory: %q %v", got, ok)
	}
}
