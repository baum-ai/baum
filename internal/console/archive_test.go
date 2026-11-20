//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *fixture) create(number int) Conversation {
	f.t.Helper()
	req := CreateRequest{Agent: AgentClaudeCode}
	if number > 0 {
		req.Page = pullPage(number)
	}
	c, err := f.service.Create(context.Background(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) listed(id string) bool {
	f.t.Helper()
	list, err := f.service.List(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// An empty directory baum made goes with the archive; the conversation leaves
// the list but can still be read.
func TestArchiveRemovesTheEmptyDirectory(t *testing.T) {
	f := newFixture(t, nil)
	c := f.create(0)
	got, err := f.service.Archive(context.Background(), c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == nil || got.State != StateClosed {
		t.Fatalf("archived %+v", got)
	}
	if exists(c.Directory) {
		t.Fatal("the directory stays")
	}
	if f.listed(c.ID) {
		t.Fatal("an archived conversation is listed")
	}
	if stored, err := f.service.Get(context.Background(), c.ID); err != nil || stored.ArchivedAt == nil {
		t.Fatalf("stored %+v %v", stored, err)
	}
	// Archiving again changes nothing.
	if again, err := f.service.Archive(context.Background(), c.ID, false); err != nil || !again.ArchivedAt.Equal(*got.ArchivedAt) {
		t.Fatalf("again %+v %v", again, err)
	}
	if _, err := f.service.Archive(context.Background(), "nope", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown conversation: %v", err)
	}
}

// Files in the empty directory are work: the archive is refused until the
// person says to discard them, and the conversation stays open meanwhile, so
// its agent can still be asked to keep them.
func TestArchiveAsksBeforeRemovingFiles(t *testing.T) {
	f := newFixture(t, nil)
	c := f.create(0)
	if err := os.WriteFile(filepath.Join(c.Directory, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Archive(context.Background(), c.ID, false)
	var unclean *UncleanError
	if !errors.As(err, &unclean) || unclean.Changed != 1 || unclean.Paths[0] != "notes.md" || unclean.Worktree {
		t.Fatalf("refusal %+v %v", unclean, err)
	}
	if !exists(c.Directory) || !f.listed(c.ID) {
		t.Fatal("a refused archive removed something")
	}
	if got, _ := f.service.Get(context.Background(), c.ID); got.State != StateIdle || got.ArchivedAt != nil {
		t.Fatalf("after the refusal %+v", got)
	}
	if _, err := f.service.Archive(context.Background(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	if exists(c.Directory) || f.listed(c.ID) {
		t.Fatal("the discarding archive left something")
	}
}

// A clean worktree baum added goes with its branch, and the clone keeps no
// record of it.
func TestArchiveRemovesACleanWorktreeAndItsBranch(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	c := f.create(9)
	if !c.CreatedWorktree || !c.CreatedBranch || c.Branch != "baum/pr-9" {
		t.Fatalf("created %+v", c)
	}
	if _, err := f.service.Archive(context.Background(), c.ID, false); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.opts.Root, "worktrees", c.ID)) {
		t.Fatal("the worktree stays")
	}
	if list := git(t, r.clone, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("the clone lists a worktree: %s", list)
	}
	if got := git(t, r.clone, "branch", "--list", "baum/pr-9"); got != "" {
		t.Fatalf("the branch stays: %s", got)
	}
}

// Uncommitted changes and commits no remote has are work: they are named,
// and removed only when the person says so.
func TestArchiveAsksBeforeLosingWorktreeWork(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	c := f.create(9)
	if err := os.WriteFile(filepath.Join(c.Directory, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, c.Directory, "commit", "-q", "--allow-empty", "-m", "local")
	_, err := f.service.Archive(context.Background(), c.ID, false)
	var unclean *UncleanError
	if !errors.As(err, &unclean) || !unclean.Worktree || unclean.Changed != 1 || unclean.Paths[0] != "a.txt" ||
		unclean.Unpushed != 1 || unclean.Branch != "baum/pr-9" {
		t.Fatalf("refusal %+v %v", unclean, err)
	}
	if !strings.Contains(err.Error(), "1 uncommitted changes and 1 commits on baum/pr-9") {
		t.Fatalf("message %q", err)
	}
	if !exists(c.Directory) {
		t.Fatal("a refused archive removed the worktree")
	}
	if _, err := f.service.Archive(context.Background(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	if exists(c.Directory) || git(t, r.clone, "branch", "--list", "baum/pr-9") != "" {
		t.Fatal("the discarding archive left the worktree or its branch")
	}
}

// A worktree that was the person's own stays, whatever it holds.
func TestArchiveLeavesThePersonsWorktree(t *testing.T) {
	r := newRepository(t)
	existing := filepath.Join(filepath.Dir(r.clone), "feature-worktree")
	git(t, r.clone, "worktree", "add", "-q", existing, "feature")
	f := pullFixture(t, r)
	c := f.create(7)
	if c.Directory != existing || c.CreatedWorktree {
		t.Fatalf("created %+v", c)
	}
	if err := os.WriteFile(filepath.Join(existing, "mine.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Archive(context.Background(), c.ID, false); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(existing, "mine.txt")) || git(t, r.clone, "branch", "--list", "feature") == "" {
		t.Fatal("the person's worktree or branch was touched")
	}
}

// A worktree another conversation works in stays until the last of them is
// archived; the branch the first one created then goes too.
func TestArchiveKeepsASharedWorktree(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	first := f.create(7)
	second := f.create(7)
	if second.Directory != first.Directory || second.CreatedWorktree || !first.CreatedBranch {
		t.Fatalf("created %+v and %+v", first, second)
	}
	if _, err := f.service.Archive(context.Background(), first.ID, false); err != nil {
		t.Fatal(err)
	}
	if !exists(first.Directory) {
		t.Fatal("a worktree another conversation works in was removed")
	}
	if _, err := f.service.Archive(context.Background(), second.ID, false); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.opts.Root, "worktrees", first.ID)) {
		t.Fatal("the worktree stays after the last conversation in it was archived")
	}
	if got := git(t, r.clone, "branch", "--list", first.Branch); got != "" {
		t.Fatalf("the branch baum created stays: %s", got)
	}
}
