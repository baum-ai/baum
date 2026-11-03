package livesessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// workRoot holds the directories the sessions below work in. A Git hook
// counts only in an absolute directory, which on Windows needs a volume.
var workRoot = filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "work")

// workDir is rel, a slash-separated path, under workRoot.
func workDir(rel string) string {
	return filepath.Join(workRoot, filepath.FromSlash(rel))
}

// workLinker links directories, commits, pushes and recorded work: the cwd of
// every session below is the main checkout, on main, with no pull request.
type workLinker struct {
	*linker
	// pushes are the pull requests of the branches, as the discovery cache
	// knows them; a branch missing has none yet.
	pushes map[string]int
}

func newWorkLinker() *workLinker {
	l := newLinker()
	l.links[workDir("main")] = Link{Repository: "acme/demo", Branch: "main", Status: "unmatched"}
	// The worktree an agent committed in, checked out on another branch
	// since: a commit links to the branch it was made on.
	l.links[workDir("trees/fix")] = Link{Repository: "acme/demo", Branch: "later", PR: 99, Method: "branch", Status: "resolved"}
	return &workLinker{linker: l, pushes: map[string]int{"fix": 12}}
}

func (l *workLinker) LinkCommit(_ context.Context, dir, branch, head string) (Link, bool) {
	if !strings.HasPrefix(dir, workRoot+string(filepath.Separator)) {
		return Link{}, false
	}
	return l.Relink(context.Background(), Session{Repository: "acme/demo", Branch: branch, HeadSHA: head})
}

func (l *workLinker) LinkPush(_ context.Context, dir, remote, branch, head string) (Link, bool) {
	if !strings.HasPrefix(dir, workRoot+string(filepath.Separator)) || remote != "origin" {
		return Link{}, false
	}
	return l.Relink(context.Background(), Session{Repository: "acme/demo", Branch: branch, HeadSHA: head})
}

func (l *workLinker) Relink(_ context.Context, s Session) (Link, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	link := Link{Repository: s.Repository, Branch: s.Branch, HeadSHA: s.HeadSHA, Status: "unmatched"}
	if pr := l.pushes[s.Branch]; pr != 0 {
		link.PR, link.Method, link.Status = pr, "branch", "resolved"
	}
	return link, true
}

var (
	claude = hookevents.Process{PID: 4100, Start: "77", Name: "claude"}
	other  = hookevents.Process{PID: 4200, Start: "78", Name: "claude"}
	tmux   = hookevents.Process{PID: 3890, Start: "5", Name: "tmux: server"}
	pushed = strings.Repeat("b", 40)
)

// prompt is a prompt of session id run by the agent process.
func prompt(id string, at time.Time, agent hookevents.Process) hookevents.Event {
	e := event(id, "user-prompt-submit", at, workDir("main"))
	e.Terminal = &hookevents.Terminal{Processes: []hookevents.Process{agent, tmux}}
	return e
}

// gitHook is a Git hook run in dir by a command of the agent process.
func gitHook(name, dir string, at time.Time, agent hookevents.Process, lines ...string) hookevents.Event {
	e := hookevents.New(hookevents.KindGit, "", name, dir, "test")
	e.Time = at
	e.Terminal = &hookevents.Terminal{Processes: []hookevents.Process{{PID: 9001, Start: "90", Name: "sh"}, {PID: 9000, Start: "90", Name: "git"}, {PID: 8999, Start: "89", Name: "bash"}, agent, tmux}}
	if name == "pre-push" {
		e.Git = &hookevents.Git{Args: []string{"origin"}, Lines: lines}
	}
	return e
}

// commit is a post-commit run in dir by a command of the agent process, of a
// commit on branch.
func commit(dir, branch string, at time.Time, agent hookevents.Process) hookevents.Event {
	e := gitHook("post-commit", dir, at, agent)
	e.Git = &hookevents.Git{Head: pushed, Branch: branch}
	return e
}

func pushOf(branch string) string {
	return "refs/heads/" + branch + " " + pushed + " refs/heads/" + branch + " " + strings.Repeat("0", 40)
}

// An agent that commits in another worktree of the clone links to the pull
// request of the branch it committed on, though its own directory has none
// and the worktree has checked out another branch since. A commit whose hook
// could not read it links nothing.
func TestSessionLinksToThePullRequestOfItsCommit(t *testing.T) {
	c := &clock{t0}
	tr := newTracker(c, newWorkLinker())
	apply(tr, prompt("s-1", t0, claude))
	if s := only(t, tr); s.PR != 0 || s.Branch != "main" {
		t.Fatalf("before the commit: %+v", s)
	}
	apply(tr, gitHook("post-commit", workDir("trees/fix"), t0.Add(30*time.Second), claude))
	if s := only(t, tr); s.PR != 0 {
		t.Fatalf("a commit without its branch and head linked: %+v", s)
	}
	if !apply(tr, commit(workDir("trees/fix"), "fix", t0.Add(time.Minute), claude)) {
		t.Fatal("the commit's pull request was not announced")
	}
	if s := only(t, tr); s.PR != 12 || s.Branch != "fix" || s.LinkMethod != MethodCommit {
		t.Fatalf("after the commit: %+v", s)
	}
}

// An agent that pushes a branch, then checks its directory back out on main,
// links to the branch's pull request once the cache has it, and keeps it.
func TestSessionLinksToThePullRequestOfItsPush(t *testing.T) {
	c := &clock{t0}
	l := newWorkLinker()
	tr := newTracker(c, l)
	apply(tr, prompt("s-1", t0, claude))
	apply(tr, gitHook("pre-push", workDir("main"), t0.Add(time.Minute), claude, pushOf("topic")))
	if s := only(t, tr); s.PR != 0 {
		t.Fatalf("linked before the pull request exists: %+v", s)
	}
	l.mu.Lock()
	l.pushes["topic"] = 33
	l.mu.Unlock()
	c.advance(RelinkEvery)
	tr.Sweep()
	tr.ResolvePending(context.Background())
	if s := only(t, tr); s.PR != 33 || s.Branch != "topic" || s.LinkMethod != MethodPush {
		t.Fatalf("after the pull request opened: %+v", s)
	}
	c.advance(RelinkEvery)
	apply(tr, prompt("s-1", c.now, claude))
	if s := only(t, tr); s.PR != 33 {
		t.Fatalf("the next lookup of the directory lost the push: %+v", s)
	}
}

// The directory's own pull request comes first, and Git run by another
// process, or by a process two sessions name, belongs to no session.
func TestWorkOnlyFillsAMissingLinkOfItsOwnSession(t *testing.T) {
	c := &clock{t0}
	l := newWorkLinker()
	l.pushes["topic"] = 33
	tr := newTracker(c, l)
	apply(tr, prompt("s-1", t0, claude))
	apply(tr, gitHook("pre-push", workDir("main"), t0.Add(time.Minute), other, pushOf("topic")))
	if s := only(t, tr); s.PR != 0 {
		t.Fatalf("another process's push linked: %+v", s)
	}
	apply(tr, prompt("s-2", t0.Add(2*time.Minute), claude))
	apply(tr, gitHook("pre-push", workDir("main"), t0.Add(3*time.Minute), claude, pushOf("topic")))
	for _, s := range tr.Sessions() {
		if s.PR != 0 {
			t.Fatalf("a push by a process two sessions name linked: %+v", s)
		}
	}

	cwd := newTracker(c, l)
	e := prompt("s-3", t0, claude)
	// newLinker's directory, which resolves to a pull request of its own.
	e.Session.Cwd = "/work/demo"
	apply(cwd, e)
	apply(cwd, gitHook("pre-push", workDir("main"), t0.Add(time.Minute), claude, pushOf("topic")))
	if s := only(t, cwd); s.PR != 7 || s.LinkMethod == MethodPush {
		t.Fatalf("the push replaced the directory's pull request: %+v", s)
	}
}

// A daemon that restarts keeps a session's pushes: from the record while it
// holds them, from the state file once it has rotated past them.
func TestWorkSurvivesARestart(t *testing.T) {
	c := &clock{t0}
	l := newWorkLinker()
	l.pushes["topic"] = 33
	src := &source{}
	for _, e := range []hookevents.Event{
		prompt("s-1", t0, claude),
		gitHook("pre-push", workDir("main"), t0.Add(time.Minute), claude, pushOf("topic")),
	} {
		src.records = append(src.records, record(e))
	}
	dir := t.TempDir()
	tr := &Tracker{Source: src, Linker: l, Now: c.Now, StateDir: dir}
	tr.Rebuild(context.Background())
	tr.ResolvePending(context.Background())
	if s := only(t, tr); s.PR != 33 {
		t.Fatalf("rebuilt from the record: %+v", s)
	}
	tr.persist()

	rotated := &source{records: src.records[:1]}
	again := &Tracker{Source: rotated, Linker: l, Now: c.Now, StateDir: dir}
	again.Rebuild(context.Background())
	// The directory is looked up again, on main.
	c.advance(RelinkEvery)
	again.Sweep()
	again.ResolvePending(context.Background())
	if s := only(t, again); s.PR != 33 || s.LinkMethod != MethodPush {
		t.Fatalf("restored from the state file: %+v", s)
	}
}
