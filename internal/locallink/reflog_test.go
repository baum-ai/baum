package locallink

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
)

// reflogRepo is a clone with a main working tree and a base commit.
type reflogRepo struct {
	t    *testing.T
	main string
	base string
}

func newReflogRepo(t *testing.T) *reflogRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "Grace Hopper")
		t.Setenv("GIT_"+who+"_EMAIL", "grace@example.com")
	}
	r := &reflogRepo{t: t, main: filepath.Join(t.TempDir(), "widgets")}
	if err := os.MkdirAll(r.main, 0o755); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, r.main, "init", "-q", "-b", "main")
	r.base = r.commit(r.main, "a.txt", "Start", "10:00:00")
	return r
}

// at is a time on the day every test uses.
func at(clock string) time.Time {
	parsed, err := time.Parse(time.RFC3339, "2026-07-01T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return parsed
}

// git runs git in dir with the committer clock set to clock.
func (r *reflogRepo) git(dir, clock string, args ...string) string {
	r.t.Helper()
	r.t.Setenv("GIT_AUTHOR_DATE", at(clock).Format(time.RFC3339))
	r.t.Setenv("GIT_COMMITTER_DATE", at(clock).Format(time.RFC3339))
	return linktest.Git(r.t, dir, args...)
}

// commit writes file in dir and commits it at clock, with the quiet flag a
// transcript's output would not reveal.
func (r *reflogRepo) commit(dir, file, message, clock string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(message+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(dir, clock, "add", "-A")
	r.git(dir, clock, "commit", "-q", "-m", message)
	return r.git(dir, clock, "rev-parse", "HEAD")
}

// worktree adds a linked worktree on a new branch from the base commit.
func (r *reflogRepo) worktree(path, branch string) string {
	r.t.Helper()
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.main, ".claude", "worktrees", path)
	}
	r.git(r.main, "10:01:00", "worktree", "add", "-q", "-b", branch, path, r.base)
	return path
}

func (r *reflogRepo) commits(head string) []gitlocal.Commit {
	r.t.Helper()
	ctx := context.Background()
	repo, err := gitlocal.Open(ctx, r.main, gitlocal.Options{})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	list, err := repo.Commits(ctx, r.base, head)
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

// call is one shell tool call of a session.
type call struct {
	tool, input string
	start, end  time.Time
	// status is the outcome; the zero value is a success.
	status session.ToolStatus
}

func bash(command, start, end string) call {
	input, _ := json.Marshal(map[string]string{"command": command})
	return call{tool: "Bash", input: string(input), start: at(start), end: at(end)}
}

func execCommand(command, workdir, start, end string) call {
	input, _ := json.Marshal(map[string]string{"cmd": command, "workdir": workdir})
	return call{tool: "exec_command", input: string(input), start: at(start), end: at(end)}
}

func reflogSession(id, vendor, cwd string, calls ...call) sessions.Session {
	started, ended := at("09:00:00"), at("12:00:00")
	g := &session.Graph{Sessions: []session.Session{{ID: id, WorkingDir: &cwd, StartedAt: &started, EndedAt: &ended}}}
	for i, c := range calls {
		use := id + "-use-" + string(rune('a'+i))
		start, end := c.start, c.end
		status := c.status
		if status == "" {
			status = session.StatusOK
		}
		g.ToolUses = append(g.ToolUses, session.ToolUse{ID: use, SessionID: id, Name: c.tool, Verb: session.VerbExecute, Input: json.RawMessage(c.input), Status: status, Timestamp: &start})
		g.ToolResults = append(g.ToolResults, session.ToolResult{ID: use + "-result", ToolUseID: use, SessionID: id, Status: status, Timestamp: &end})
	}
	return sessions.Session{ID: id, Vendor: vendor, Source: "claude_code", Graph: g}
}

// withBranch records the branch the session ran on.
func withBranch(s sessions.Session, branch string) sessions.Session {
	s.Graph.Sessions[0].GitBranch = &branch
	return s
}

// withCwdAttribute records the session's working directory from the start.
func withCwdAttribute(s sessions.Session, cwd string) sessions.Session {
	value, _ := json.Marshal(cwd)
	s.Graph.Attributes = append(s.Graph.Attributes, session.SessionAttribute{ID: "attr", SessionID: s.ID, Key: "cwd", Value: value, ValidFrom: at("09:00:00")})
	return s
}

// resolveReflog applies the reflog evidence and matches, returning for each
// commit the sessions linked by commit_ref.
func resolveReflog(t *testing.T, r *reflogRepo, found []sessions.Session, commits []gitlocal.Commit) ([]sessions.Session, map[string][]string) {
	t.Helper()
	ctx := context.Background()
	found = addReflogCommits(ctx, r.main, found, commits)
	_, _, edges, err := linkSessions(ctx, found, commits)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string][]string{}
	for _, e := range edges {
		if e.Method == "commit_ref" {
			links[e.CommitSHA] = append(links[e.CommitSHA], e.RunID)
		}
	}
	return found, links
}

func assertLinks(t *testing.T, links map[string][]string, sha string, want ...string) {
	t.Helper()
	got := slices.Clone(links[sha])
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("commit %s linked to %v, want %v", sha[:7], got, want)
	}
}

func created(found []sessions.Session, id string) []string {
	for _, s := range found {
		if s.ID == id {
			return s.CreatedCommits
		}
	}
	return nil
}

func TestReflogLinksQuietCommitInLinkedWorktree(t *testing.T) {
	r := newReflogRepo(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	wt := r.worktree(outside, "feature")
	sha := r.commit(wt, "b.txt", "Quiet", "10:30:00")
	a := reflogSession("session-a", "claude-code", wt, bash("git commit -q -m Quiet", "10:29:58", "10:30:02"))
	b := reflogSession("session-b", "claude-code", r.main, bash("git commit -q -m Other", "10:29:58", "10:30:02"))
	found, links := resolveReflog(t, r, []sessions.Session{a, b}, r.commits(sha))
	assertLinks(t, links, sha, "session-a")
	if got := created(found, "session-b"); len(got) != 0 {
		t.Fatalf("session-b created %v", got)
	}
}

func TestReflogLinksAmend(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("amend", "feature")
	r.commit(wt, "b.txt", "First", "10:30:00")
	r.git(wt, "10:35:00", "commit", "-q", "--amend", "-m", "Amended")
	amended := r.git(wt, "10:35:00", "rev-parse", "HEAD")
	early := reflogSession("session-early", "claude-code", wt, bash("git commit -q -m First", "10:29:58", "10:30:02"))
	late := reflogSession("session-late", "claude-code", wt, bash("git commit --amend -q -m Amended", "10:34:58", "10:35:02"))
	_, links := resolveReflog(t, r, []sessions.Session{early, late}, r.commits(amended))
	assertLinks(t, links, amended, "session-late")
}

func TestReflogLinksRebasePick(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("rebase", "feature")
	r.commit(wt, "b.txt", "Feature work", "10:30:00")
	r.commit(r.main, "c.txt", "Main moves on", "10:40:00")
	r.git(wt, "10:50:00", "rebase", "-q", "main")
	rebased := r.git(wt, "10:50:00", "rev-parse", "HEAD")
	author := reflogSession("session-author", "claude-code", wt, bash("git commit -q -m Feature", "10:29:58", "10:30:02"))
	rebaser := reflogSession("session-rebaser", "claude-code", wt, bash("git rebase main", "10:49:58", "10:50:02"))
	commits := r.commits(rebased)
	_, links := resolveReflog(t, r, []sessions.Session{author, rebaser}, commits)
	assertLinks(t, links, rebased, "session-rebaser")
}

func TestReflogLinksCherryPick(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("pick", "feature")
	picked := r.commit(r.main, "c.txt", "Elsewhere", "10:20:00")
	r.git(wt, "10:30:00", "cherry-pick", picked)
	head := r.git(wt, "10:30:00", "rev-parse", "HEAD")
	s := reflogSession("session-picker", "claude-code", wt, bash("git cherry-pick "+picked[:7], "10:29:58", "10:30:02"))
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(head))
	assertLinks(t, links, head, "session-picker")
}

func TestReflogKeepsConcurrentWorktreesApart(t *testing.T) {
	r := newReflogRepo(t)
	one, two := r.worktree("one", "feature-one"), r.worktree("two", "feature-two")
	first := r.commit(one, "one.txt", "One", "10:30:00")
	second := r.commit(two, "two.txt", "Two", "10:30:01")
	a := reflogSession("session-one", "claude-code", one, bash("git commit -q -m One", "10:29:50", "10:30:10"))
	b := reflogSession("session-two", "claude-code", two, bash("git commit -q -m Two", "10:29:50", "10:30:10"))
	commits := append(r.commits(first), r.commits(second)...)
	_, links := resolveReflog(t, r, []sessions.Session{a, b}, commits)
	assertLinks(t, links, first, "session-one")
	assertLinks(t, links, second, "session-two")
}

func TestReflogSameWorktreePrefersTheOnlyGitWriter(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("shared", "feature")
	sha := r.commit(wt, "b.txt", "Shared", "10:30:00")
	writer := reflogSession("session-writer", "claude-code", wt, bash("git commit -q -m Shared", "10:29:50", "10:30:10"))
	reader := reflogSession("session-reader", "claude-code", wt, bash("go test ./...", "10:29:50", "10:30:10"))
	_, links := resolveReflog(t, r, []sessions.Session{reader, writer}, r.commits(sha))
	assertLinks(t, links, sha, "session-writer")
}

func TestReflogLeavesTwoGitWritersUnresolved(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("shared", "feature")
	sha := r.commit(wt, "b.txt", "Shared", "10:30:00")
	a := reflogSession("session-a", "claude-code", wt, bash("git commit -q -m Shared", "10:29:50", "10:30:10"))
	b := reflogSession("session-b", "claude-code", wt, bash("git commit -q -m Shared", "10:29:50", "10:30:10"))
	found, links := resolveReflog(t, r, []sessions.Session{a, b}, r.commits(sha))
	assertLinks(t, links, sha)
	if len(created(found, "session-a"))+len(created(found, "session-b")) != 0 {
		t.Fatal("an ambiguous commit was attributed")
	}
}

func TestReflogSurvivesRemovedWorktree(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("removed", "feature")
	sha := r.commit(wt, "b.txt", "Gone", "10:30:00")
	commits := r.commits(sha)
	r.git(r.main, "10:40:00", "worktree", "remove", "--force", wt)
	s := withBranch(reflogSession("session-a", "claude-code", wt, bash("git commit -q -m Gone", "10:29:58", "10:30:02")), "feature")
	_, links := resolveReflog(t, r, []sessions.Session{s}, commits)
	assertLinks(t, links, sha, "session-a")
}

func TestReflogIgnoresCommitOutsideEveryToolCall(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("manual", "feature")
	sha := r.commit(wt, "b.txt", "By hand", "10:30:00")
	s := reflogSession("session-a", "claude-code", wt, bash("git status", "10:20:00", "10:20:05"))
	found, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha)
	if got := created(found, "session-a"); len(got) != 0 {
		t.Fatalf("created %v", got)
	}
}

func TestReflogIgnoresMerges(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("merge", "feature")
	r.commit(wt, "b.txt", "Feature", "10:20:00")
	r.commit(r.main, "c.txt", "Main", "10:25:00")
	r.git(wt, "10:30:00", "merge", "-q", "--no-ff", "-m", "Merge main", "main")
	merge := r.git(wt, "10:30:00", "rev-parse", "HEAD")
	s := reflogSession("session-a", "claude-code", wt, bash("git commit -q -m x", "10:29:58", "10:30:02"))
	found, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(merge))
	assertLinks(t, links, merge)
	if got := created(found, "session-a"); slices.Contains(got, merge) {
		t.Fatalf("a merge was attributed: %v", got)
	}
}

func TestReflogOutranksHookObservation(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("hooked", "feature")
	sha := r.commit(wt, "b.txt", "Hooked", "10:30:00")
	guess := sessions.Session{ID: "session-hook", Vendor: "claude-code", Source: "claude_code", CreatedCommits: []string{sha}}
	real := reflogSession("session-real", "claude-code", wt, bash("git commit -q -m Hooked", "10:29:58", "10:30:02"))
	found, links := resolveReflog(t, r, []sessions.Session{guess, real}, r.commits(sha))
	assertLinks(t, links, sha, "session-real")
	if got := created(found, "session-hook"); len(got) != 0 {
		t.Fatalf("the hook guess kept %v", got)
	}
}

func TestReflogUsesAttributeAndCodexWorkdir(t *testing.T) {
	r := newReflogRepo(t)
	one, two := r.worktree("attr", "feature-one"), r.worktree("codex", "feature-two")
	first := r.commit(one, "one.txt", "One", "10:30:00")
	second := r.commit(two, "two.txt", "Two", "10:30:01")
	// The session began in the main checkout and moved into a worktree.
	claude := withCwdAttribute(reflogSession("session-claude", "claude-code", r.main, bash("git commit -q -m One", "10:29:50", "10:30:10")), one)
	codex := reflogSession("session-codex", "codex", r.main, execCommand("git commit -q -m Two", two, "10:29:50", "10:30:10"))
	_, links := resolveReflog(t, r, []sessions.Session{claude, codex}, append(r.commits(first), r.commits(second)...))
	assertLinks(t, links, first, "session-claude")
	assertLinks(t, links, second, "session-codex")
}

func TestReflogFingerprintTracksOnlyCreationsOfTheCommits(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("fingerprint", "feature")
	ctx := context.Background()
	sha := r.commit(wt, "b.txt", "More", "10:30:00")
	before := ReflogFingerprint(ctx, r.main, []string{sha})
	if before == "" || before != ReflogFingerprint(ctx, wt, []string{sha}) || before != ReflogFingerprint(ctx, r.main, []string{sha}) {
		t.Fatalf("fingerprint %q is empty or unstable", before)
	}
	// Moving HEAD and making another commit are not creations of sha.
	r.git(wt, "10:31:00", "checkout", "-q", "--detach")
	r.git(wt, "10:32:00", "checkout", "-q", "feature")
	r.commit(wt, "c.txt", "Unrelated", "10:33:00")
	if after := ReflogFingerprint(ctx, r.main, []string{sha}); after != before {
		t.Fatal("an unrelated reflog entry changed the fingerprint")
	}
	r.git(wt, "10:35:00", "commit", "-q", "--amend", "-m", "Amended")
	amended := r.git(wt, "10:35:00", "rev-parse", "HEAD")
	if ReflogFingerprint(ctx, r.main, []string{sha, amended}) == ReflogFingerprint(ctx, r.main, []string{sha}) {
		t.Fatal("a new creation did not change the fingerprint")
	}
}

func TestParseCreationFiltersMessages(t *testing.T) {
	sha := "1111111111111111111111111111111111111111"
	wanted := map[string]bool{sha: true}
	line := func(message string) string {
		return "0000000000000000000000000000000000000000 " + sha + " Grace Hopper <grace@example.com> 1782900000 +0000\t" + message
	}
	for message, keep := range map[string]bool{
		"commit: x": true, "commit (amend): x": true, "commit (initial): x": true, "cherry-pick: x": true,
		"revert: x": true, "rebase (pick): x": true, "rebase -i (squash): x": true,
		"am: x": true, "commit (cherry-pick): x": true, "cherry-pick: fast-forward": false, "pull -q --rebase . main (pick): x": true, "rebase -i (pick): x": true, "rebase (continue): x": true,
		"rebase (start): checkout main": false, "commit (merge): x": false, "merge main: Fast-forward": false, "checkout: moving from a to b": false,
		"rebase (finish): returning to refs/heads/a": false, "pull: Fast-forward": false, "reset: moving to HEAD~1": false,
	} {
		if _, ok := parseCreation(line(message), wanted); ok != keep {
			t.Errorf("%q kept = %v, want %v", message, ok, keep)
		}
	}
	if _, ok := parseCreation(line("commit: x"), map[string]bool{}); ok {
		t.Error("an unwanted commit was kept")
	}
}
