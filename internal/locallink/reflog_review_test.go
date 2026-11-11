package locallink

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestReflogIgnoresFailedAndRejectedCalls(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("failed", "feature")
	manual := r.commit(wt, "b.txt", "By hand", "10:30:00")
	failed := bash("git commit -q -m x", "10:29:00", "10:31:00")
	failed.status = session.StatusError
	rejected := bash("git commit -q -m y", "10:29:00", "10:31:00")
	rejected.status = session.StatusInterrupted
	hooked := sessions.Session{ID: "session-hook", Vendor: "claude-code", Source: "claude_code", CreatedCommits: []string{manual}}
	sessionsFound := []sessions.Session{
		reflogSession("session-failed", "claude-code", wt, failed),
		reflogSession("session-rejected", "claude-code", wt, rejected),
		hooked,
	}
	found, links := resolveReflog(t, r, sessionsFound, r.commits(manual))
	assertLinks(t, links, manual, "session-hook")
	if got := created(found, "session-hook"); len(got) != 1 {
		t.Fatalf("the hook observation was removed: %v", got)
	}
}

func TestReflogFastForwardCherryPickDoesNotClaimTheCommit(t *testing.T) {
	r := newReflogRepo(t)
	one, two := r.worktree("one", "feature-one"), r.worktree("two", "feature-two")
	picked := r.commit(one, "x.txt", "By hand", "10:20:00")
	r.git(two, "10:30:00", "cherry-pick", "--ff", picked)
	if head := r.git(two, "10:30:00", "rev-parse", "HEAD"); head != picked {
		t.Fatalf("cherry-pick --ff made a new commit")
	}
	s := reflogSession("session-picker", "claude-code", two, bash("git cherry-pick --ff "+picked[:7], "10:29:58", "10:30:02"))
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(picked))
	assertLinks(t, links, picked)
}

func TestCreatorsOfUsesOnlyTheEarliestCreation(t *testing.T) {
	sha := "1111111111111111111111111111111111111111"
	log := reflog{
		roots: []string{"/one", "/two"},
		creations: []creation{
			{sha: sha, at: at("10:20:00"), head: true, worktree: "/one", kind: "commit"},
			{sha: sha, at: at("10:20:00"), branch: "one", kind: "commit"},
			{sha: sha, at: at("10:30:00"), head: true, worktree: "/two", kind: "cherry-pick"},
		},
	}
	windows := []callWindow{{session: "picker", start: at("10:29:58"), end: at("10:30:02"), root: "/two"}}
	if got := creatorsOf(log, windows); len(got) != 0 {
		t.Fatalf("creators = %v", got)
	}
	windows = append(windows, callWindow{session: "author", start: at("10:19:58"), end: at("10:20:02"), root: "/one"})
	if got := creatorsOf(log, windows); got[sha] != "author" || len(got) != 1 {
		t.Fatalf("creators = %v", got)
	}
}

// unmapped is s as LoadReusing returns a session whose digest the book
// holds: without its graph.
func unmapped(book *digestBook, s sessions.Session) sessions.Session {
	s.Key = "key-" + s.ID
	book.digest(s)
	s.Graph, s.Unmapped, s.Transcript = nil, true, true
	return s
}

func TestReflogUsesTheCallsOfAReusedSession(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("reused", "feature")
	sha := r.commit(wt, "b.txt", "Mine", "10:30:00")
	creator := reflogSession("session-creator", "claude-code", wt, bash("git commit -q -m Mine", "10:29:58", "10:30:02"))
	other := reflogSession("session-other", "claude-code", wt, bash("git commit -q -m Theirs", "10:29:59", "10:30:01"))
	other.Key = "key-session-other"
	ctx := context.Background()

	t.Run("alone, it keeps its link", func(t *testing.T) {
		book := newDigestBook(newDigestCache(DefaultDigestBytes))
		found := addReflogCommits(ctx, nil, r.main, []sessions.Session{unmapped(book, creator)}, r.commits(sha), book)
		if !slices.Contains(created(found, "session-creator"), sha) {
			t.Fatalf("the reused creator lost its reflog link: %v", created(found, "session-creator"))
		}
	})
	t.Run("beside a mapped session, both still fit", func(t *testing.T) {
		book := newDigestBook(newDigestCache(DefaultDigestBytes))
		found := addReflogCommits(ctx, nil, r.main, []sessions.Session{unmapped(book, creator), other}, r.commits(sha), book)
		if got := created(found, "session-other"); slices.Contains(got, sha) {
			t.Fatalf("the mapped session took a commit the reused one may have made: %v", got)
		}
	})
}

// codeModeCall is a code-mode program whose commands all printed a
// successful result, as `text(await tools.exec_command(...))` does.
func codeModeCall(source, start, end string) call {
	input, _ := json.Marshal(source)
	return call{tool: "exec", input: string(traceprocessing.NormalizeCodexExecInput("exec", input)), start: at(start), end: at(end),
		output: nestedResults(strings.Count(source, "exec_command("))}
}

// nestedResults is the printed output of n code-mode commands that finished
// with status 0.
func nestedResults(n int) string {
	var lines []string
	for i := range n {
		lines = append(lines, fmt.Sprintf(`{"chunk_id":"c%d","wall_time_seconds":0.5,"exit_code":0,"original_token_count":0,"output":""}`, i))
	}
	return strings.Join(lines, "\n")
}

func TestReflogCodeModeNeedsEachCommandsOutcome(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("outcome", "feature")
	sha := r.commit(wt, "b.txt", "By someone else", "10:30:00")
	source := `text(await tools.exec_command({cmd:"git commit -q -m Mine", workdir:"` + wt + `"}));`
	for name, output := range map[string]string{
		"a failed commit":   `{"chunk_id":"a","exit_code":1,"output":"nothing to commit"}`,
		"no printed result": "",
		"escaped once more": `{\"chunk_id\":\"a\",\"exit_code\":128,\"output\":\"fatal\"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := codeModeCall(source, "10:29:58", "10:30:02")
			c.output = output
			s := reflogSession("session-codex", "codex", wt, c)
			_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
			assertLinks(t, links, sha)
		})
	}
	t.Run("a successful commit", func(t *testing.T) {
		skipWindowsWorkdir(t)
		s := reflogSession("session-codex", "codex", wt, codeModeCall(source, "10:29:58", "10:30:02"))
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
		assertLinks(t, links, sha, "session-codex")
	})
}

func TestReflogPollsOfAnotherCommandDoNotExtendAProgram(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("tests", "feature")
	late := r.commit(wt, "b.txt", "By hand during the tests", "10:31:00")
	source := `text(await tools.exec_command({cmd:"git commit -q -m Mine", workdir:"` + wt + `"}));
text(await tools.exec_command({cmd:"just test", workdir:"` + wt + `", yield_time_ms:1000}));`
	program := codeModeCall(source, "10:29:58", "10:30:02")
	program.output = `{"chunk_id":"a","exit_code":0,"output":"[feature abc] Mine"}` + "\n" +
		`{"chunk_id":"b","session_id":777,"output":"running"}`
	poll := call{tool: "write_stdin", input: `{"session_id":777,"chars":""}`, start: at("10:30:30"), end: at("10:31:30"),
		output: "Chunk ID: c\nProcess exited with code 0\n"}
	s := reflogSession("session-codex", "codex", wt, program, poll)
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(late))
	assertLinks(t, links, late)
}

func TestReflogYieldedCommandThatFailedGivesNoWindow(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("slow", "feature")
	sha := r.commit(wt, "b.txt", "By hand", "10:29:30")
	start := call{tool: "exec_command", input: `{"cmd":"git commit -q -m x","workdir":"` + wt + `"}`, start: at("10:29:00"), end: at("10:29:02"),
		output: "Chunk ID: a\nProcess running with session ID 4242\n"}
	failed := call{tool: "write_stdin", input: `{"session_id":4242,"chars":""}`, start: at("10:29:40"), end: at("10:29:42"),
		output: "Chunk ID: b\nProcess exited with code 1\n"}
	s := reflogSession("session-slow", "codex", wt, start, failed)
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha)
}

func TestReflogCodeModeCommandsKeepTheirOwnWorkdir(t *testing.T) {
	skipWindowsWorkdir(t)
	t.Run("a command in another workdir does not place the commit", func(t *testing.T) {
		r := newReflogRepo(t)
		a, b := r.worktree("a", "feature-a"), r.worktree("b", "feature-b")
		own := r.commit(a, "a.txt", "Own", "10:30:00")
		manual := r.commit(b, "b.txt", "By hand", "10:30:01")
		source := `await tools.exec_command({cmd:"git commit -q -m Own", workdir:"` + a + `"});
await tools.exec_command({cmd:"ls", workdir:"` + b + `"});`
		s := reflogSession("session-codex", "codex", r.main, codeModeCall(source, "10:29:58", "10:30:03"))
		_, links := resolveReflog(t, r, []sessions.Session{s}, append(r.commits(own), r.commits(manual)...))
		assertLinks(t, links, own, "session-codex")
		assertLinks(t, links, manual)
	})
	t.Run("a cd in one command does not move the next", func(t *testing.T) {
		r := newReflogRepo(t)
		a, b := r.worktree("a", "feature-a"), r.worktree("b", "feature-b")
		own := r.commit(a, "a.txt", "Own", "10:30:00")
		source := `await tools.exec_command({cmd:"cd ` + b + ` && ls", workdir:"` + a + `"});
await tools.exec_command({cmd:"git commit -q -m Own", workdir:"` + a + `"});`
		s := reflogSession("session-codex", "codex", r.main, codeModeCall(source, "10:29:58", "10:30:03"))
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(own))
		assertLinks(t, links, own, "session-codex")
	})
}

func TestRootLocatorPrefersTheDeepestLiveWorktree(t *testing.T) {
	main := filepath.FromSlash("/repo")
	outer := filepath.Join(main, ".claude", "worktrees", "outer")
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")
	locate := rootLocator(main, []string{main, outer, inner})
	if got := locate(filepath.Join(inner, "pkg")); got != inner {
		t.Errorf("inner path located in %s", got)
	}
	if got := locate(filepath.Join(outer, "pkg")); got != outer {
		t.Errorf("outer path located in %s", got)
	}
	// A removed worktree still maps through the conventional folder.
	removed := filepath.Join(main, ".agents", "worktrees", "gone")
	if got := locate(filepath.Join(removed, "pkg")); got != removed {
		t.Errorf("removed path located in %s", got)
	}
	if got := locate(filepath.Join(main, "pkg")); got != main {
		t.Errorf("main path located in %s", got)
	}
	if got := locate(filepath.FromSlash("/elsewhere")); got != "" {
		t.Errorf("outside path located in %s", got)
	}
}

func TestReflogPlacesCommitInANestedLiveWorktree(t *testing.T) {
	r := newReflogRepo(t)
	outer := r.worktree("outer", "feature-outer")
	inner := r.worktree(filepath.Join(outer, ".claude", "worktrees", "inner"), "feature-inner")
	sha := r.commit(inner, "b.txt", "Nested", "10:30:00")
	in := reflogSession("session-inner", "claude-code", filepath.Join(inner, "pkg"), bash("git commit -q -m Nested", "10:29:58", "10:30:02"))
	out := reflogSession("session-outer", "claude-code", outer, bash("git commit -q -m Other", "10:29:58", "10:30:02"))
	_, links := resolveReflog(t, r, []sessions.Session{in, out}, r.commits(sha))
	assertLinks(t, links, sha, "session-inner")
}

func TestReflogBranchLogMatchesOnlyTheSameBranch(t *testing.T) {
	r := newReflogRepo(t)
	first, second := r.worktree("first", "branch-one"), r.worktree("second", "branch-two")
	earlier := r.commit(first, "1.txt", "One", "10:20:00")
	manual := r.commit(second, "2.txt", "By hand", "10:30:00")
	commits := append(r.commits(earlier), r.commits(manual)...)
	r.git(r.main, "10:40:00", "worktree", "remove", "--force", first)
	r.git(r.main, "10:40:00", "worktree", "remove", "--force", second)
	elsewhere := withBranch(reflogSession("session-first", "claude-code", first, bash("git commit -q -m One", "10:29:58", "10:30:02")), "branch-one")
	unknown := reflogSession("session-unknown", "claude-code", second, bash("git commit -q -m Two", "10:29:58", "10:30:02"))
	_, links := resolveReflog(t, r, []sessions.Session{elsewhere, unknown}, commits)
	assertLinks(t, links, manual)
	assertLinks(t, links, earlier)
}

func TestReflogNestedSessionWorkingDirOutranksTheParentAttribute(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("child", "feature")
	sha := r.commit(wt, "b.txt", "By a subagent", "10:30:00")
	s := withCwdAttribute(reflogSession("session-top", "claude-code", r.main), r.main)
	parent, spawned, cwd := "session-top", "toolu_spawn", wt
	s.Graph.Sessions = append(s.Graph.Sessions, session.Session{ID: "session-child", ParentSessionID: &parent, SpawnedByToolUseID: &spawned, WorkingDir: &cwd})
	start, end := at("10:29:58"), at("10:30:02")
	s.Graph.ToolUses = append(s.Graph.ToolUses, session.ToolUse{ID: "child-use", SessionID: "session-child", Name: "Bash", Verb: session.VerbExecute, Status: session.StatusOK, Input: json.RawMessage(`{"command":"git commit -q -m x"}`), Timestamp: &start})
	s.Graph.ToolResults = append(s.Graph.ToolResults, session.ToolResult{ID: "child-result", ToolUseID: "child-use", SessionID: "session-child", Status: session.StatusOK, Timestamp: &end})
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha, "session-top")
}
