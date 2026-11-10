package livesessions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/hookevents"
)

// named is e as a hook whose environment names the Claude Code session id.
func named(e hookevents.Event, id string) hookevents.Event {
	e.Agent, e.Session = "claude-code", &hookevents.Session{ID: id}
	return e
}

func rewrite(dir string, at time.Time, lines ...string) hookevents.Event {
	e := hookevents.New(hookevents.KindGit, "", "post-rewrite", dir, "test")
	e.Time = at
	e.Git = &hookevents.Git{Args: []string{"amend"}, Lines: lines}
	return e
}

func lookup(t *testing.T, store *commitattribution.Store, shas ...string) []commitattribution.Observation {
	t.Helper()
	got, err := store.Lookup(context.Background(), []string{"acme/demo"}, shas)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The environment names the session even when another open session's process
// is among the hook's ancestors and would own the commit by the process tree.
func TestEnvironmentNamedCommitLinksToTheNamedSession(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newWorkLinker())
	apply(tr, prompt("s-1", t0, claude))
	apply(tr, prompt("s-2", t0, other))
	e := named(commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude), "s-2")
	if k, owner := ownerOf(tr, e); owner == nil || k.id != "s-1" {
		t.Fatalf("the process tree would name %v", k)
	}
	apply(tr, e)
	for _, s := range tr.Sessions() {
		switch {
		case s.ID == "s-2" && (s.PR != 12 || s.LinkMethod != MethodCommit):
			t.Errorf("named session: %+v", s)
		case s.ID == "s-1" && s.PR != 0:
			t.Errorf("process-tree session took the commit: %+v", s)
		}
	}
}

func ownerOf(tr *Tracker, e hookevents.Event) (key, *entry) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.ownerLocked(e)
}

func TestEnvironmentNamedCommitWithoutTrackedSessionIsRecorded(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	e := named(commit(workDir("trees/fix"), "fix", t0, claude), "gone-session")
	e.Terminal = nil
	apply(tr, e)
	if len(tr.Sessions()) != 0 {
		t.Fatalf("a session was invented: %+v", tr.Sessions())
	}
	got := lookup(t, store, pushed)
	if len(got) != 1 || got[0].SessionID != "gone-session" || got[0].Kind != commitattribution.Commit || got[0].Repository != "acme/demo" {
		t.Fatalf("observations: %+v", got)
	}
}

func TestEnvironmentNamedPushWithoutTrackedSessionIsRecorded(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	e := named(gitHook("pre-push", workDir("main"), t0, claude, pushOf("fix")), "gone-session")
	apply(tr, e)
	if got := lookup(t, store, pushed); len(got) != 1 || got[0].Kind != commitattribution.Push {
		t.Fatalf("observations: %+v", got)
	}
}

func TestUnknownEnvironmentAgentFallsBackToTheProcessTree(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newWorkLinker())
	apply(tr, prompt("s-1", t0, claude))
	e := commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude)
	e.Agent, e.Session = "not an agent", &hookevents.Session{ID: "s-9"}
	apply(tr, e)
	if s := only(t, tr); s.PR != 12 {
		t.Fatalf("process-tree session: %+v", s)
	}
	e = commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude)
	e.Terminal = nil
	e.Agent, e.Session = "not an agent", &hookevents.Session{ID: "s-9"}
	if matchWork(e) {
		t.Fatal("an unknown agent without ancestors matched")
	}
}

func TestPostRewriteRecordsTheNewCommits(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	oldSHA, one, two := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	apply(tr, prompt("s-1", t0, claude))
	// A post-commit already recorded the first new commit.
	first := commit(workDir("trees/fix"), "fix", t0, claude)
	first.Git.Head = one
	apply(tr, first)
	apply(tr, named(rewrite(workDir("trees/fix"), t0.Add(time.Minute),
		oldSHA+" "+one, oldSHA+" "+two+" extra", "malformed", oldSHA+" nothex", oldSHA+" "+strings.Repeat("0", 40), oldSHA+" "+two), "s-1"))
	got := lookup(t, store, one, two)
	if len(got) != 2 {
		t.Fatalf("observations: %+v", got)
	}
	for _, o := range got {
		if o.SessionID != "s-1" || o.Kind != commitattribution.Commit {
			t.Errorf("observation: %+v", o)
		}
	}
	// By the process tree, too.
	if matchWork(rewrite(workDir("trees/fix"), t0)) {
		t.Error("a rewrite naming nobody matched")
	}
	ptree := rewrite(workDir("trees/fix"), t0.Add(2*time.Minute), oldSHA+" "+pushed)
	ptree.Terminal = &hookevents.Terminal{Processes: []hookevents.Process{claude, tmux}}
	apply(tr, ptree)
	if got := lookup(t, store, pushed); len(got) != 1 || got[0].SessionID != "s-1" {
		t.Fatalf("process-tree rewrite: %+v", got)
	}
}
