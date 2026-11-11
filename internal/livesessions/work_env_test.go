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

// named is e as a hook whose environment names the Claude Code session id and
// whose agent process is the process of pid.
func named(e hookevents.Event, id string, pid int) hookevents.Event {
	e.Agent, e.Session = "claude-code", &hookevents.Session{ID: id, AgentPID: pid}
	return e
}

// ancestors replaces the hook's ancestors, nearest first.
func ancestors(e hookevents.Event, processes ...hookevents.Process) hookevents.Event {
	e.Terminal = &hookevents.Terminal{Processes: processes}
	return e
}

var (
	shell = hookevents.Process{PID: 9001, Start: "90", Name: "sh"}
	// wrapper is a process the hook's agent runs under that no session's hooks
	// ran under.
	wrapper = hookevents.Process{PID: 4300, Start: "79", Name: "claude"}
)

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

// The environment names the session when the agent it names is among the hook's
// ancestors, even where the process tree would name another open session
// further out.
func TestEnvironmentNamedCommitLinksToTheNamedSession(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newWorkLinker())
	apply(tr, prompt("s-1", t0, claude))
	apply(tr, prompt("s-2", t0, other))
	e := named(ancestors(commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude), shell, wrapper, claude, tmux), "s-2", wrapper.PID)
	if _, owner, _ := ownerOf(tr, e); owner == nil {
		t.Fatal("the process tree would name nobody")
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

// A session whose agent runs nearer the hook than the named one made the commit:
// the environment is inherited by what the named agent starts.
func TestNearerTrackedSessionWinsOverTheEnvironment(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newWorkLinker())
	apply(tr, prompt("claude-session", t0, claude))
	codex := prompt("codex-session", t0, other)
	codex.Agent = "codex"
	apply(tr, codex)
	e := named(ancestors(commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude), shell, other, claude, tmux), "claude-session", claude.PID)
	apply(tr, e)
	for _, s := range tr.Sessions() {
		switch {
		case s.ID == "codex-session" && s.PR != 12:
			t.Errorf("nearer session: %+v", s)
		case s.ID == "claude-session" && s.PR != 0:
			t.Errorf("environment session took the commit: %+v", s)
		}
	}
}

// An environment whose agent is not among the ancestors only inherited its
// variables: the process tree decides.
func TestEnvironmentWithoutItsAgentAmongTheAncestorsIsIgnored(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	apply(tr, prompt("s-1", t0, claude))
	e := named(commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude), "s-9", 31337)
	if _, _, ok := namedSession(e); ok {
		t.Fatal("a pid that is no ancestor named a session")
	}
	apply(tr, e)
	if s := only(t, tr); s.ID != "s-1" || s.PR != 12 {
		t.Fatalf("process-tree session: %+v", s)
	}
	for _, o := range lookup(t, store, pushed) {
		if o.SessionID == "s-9" {
			t.Fatalf("observation for the inherited session: %+v", o)
		}
	}
	// Nor does it make a hook that has no ancestors count.
	e = named(commit(workDir("trees/fix"), "fix", t0, claude), "s-9", claude.PID)
	e.Terminal = nil
	if matchWork(e) {
		t.Fatal("an environment without ancestors matched")
	}
}

func ownerOf(tr *Tracker, e hookevents.Event) (key, *entry, int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.ownerLocked(e)
}

func TestEnvironmentNamedCommitWithoutTrackedSessionIsRecorded(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	e := named(commit(workDir("trees/fix"), "fix", t0, claude), "gone-session", claude.PID)
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
	e := named(gitHook("pre-push", workDir("main"), t0, claude, pushOf("fix")), "gone-session", claude.PID)
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
	e.Agent, e.Session = "not an agent", &hookevents.Session{ID: "s-9", AgentPID: claude.PID}
	apply(tr, e)
	if s := only(t, tr); s.PR != 12 {
		t.Fatalf("process-tree session: %+v", s)
	}
}

func TestPostRewriteRecordsTheNewCommits(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	oldSHA, one, two := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	unchanged := strings.Repeat("4", 40)
	apply(tr, prompt("s-1", t0, claude))
	// A post-commit already recorded the first new commit.
	first := commit(workDir("trees/fix"), "fix", t0, claude)
	first.Git.Head = one
	apply(tr, first)
	apply(tr, named(ancestors(rewrite(workDir("trees/fix"), t0.Add(time.Minute),
		oldSHA+" "+one, oldSHA+" "+two+" extra", "malformed", oldSHA+" nothex", oldSHA+" "+strings.Repeat("0", 40), oldSHA+" "+two,
		// A rebase replays commits it did not change.
		unchanged+" "+unchanged), claude, tmux), "s-1", claude.PID))
	got := lookup(t, store, one, two)
	if len(got) != 2 {
		t.Fatalf("observations: %+v", got)
	}
	for _, o := range got {
		if o.SessionID != "s-1" || o.Kind != commitattribution.Commit {
			t.Errorf("observation: %+v", o)
		}
	}
	if got := lookup(t, store, unchanged); len(got) != 0 {
		t.Errorf("an unchanged commit was credited: %+v", got)
	}
	if matchWork(rewrite(workDir("trees/fix"), t0)) {
		t.Error("a rewrite naming nobody matched")
	}
	// By the process tree, too.
	ptree := ancestors(rewrite(workDir("trees/fix"), t0.Add(2*time.Minute), oldSHA+" "+pushed), claude, tmux)
	apply(tr, ptree)
	if got := lookup(t, store, pushed); len(got) != 1 || got[0].SessionID != "s-1" {
		t.Fatalf("process-tree rewrite: %+v", got)
	}
}

// A rewrite of several commits no post-commit recorded records each of them,
// though they share the event and its empty branch.
func TestPostRewriteRecordsEveryUnseenCommit(t *testing.T) {
	c := &clock{t0}
	store := &commitattribution.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	tr := newTracker(c, newWorkLinker())
	tr.Attributions = store
	oldOne, oldTwo := strings.Repeat("5", 40), strings.Repeat("6", 40)
	one, two := strings.Repeat("7", 40), strings.Repeat("8", 40)
	apply(tr, prompt("s-1", t0, claude))
	apply(tr, named(ancestors(rewrite(workDir("trees/fix"), t0.Add(time.Minute),
		oldOne+" "+one, oldTwo+" "+two), claude, tmux), "s-1", claude.PID))
	got := lookup(t, store, one, two)
	if len(got) != 2 {
		t.Fatalf("observations: %+v", got)
	}
	for _, o := range got {
		if o.SessionID != "s-1" || o.Kind != commitattribution.Commit {
			t.Errorf("observation: %+v", o)
		}
	}
}
