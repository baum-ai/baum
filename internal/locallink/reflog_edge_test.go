package locallink

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestReflogBranchLogsDoNotLeakAcrossWorktrees(t *testing.T) {
	t.Run("manual commit in another worktree", func(t *testing.T) {
		r := newReflogRepo(t)
		one, two := r.worktree("one", "feature-one"), r.worktree("two", "feature-two")
		manual := r.commit(two, "two.txt", "By hand", "10:30:00")
		s := reflogSession("session-one", "claude-code", one, bash("git commit -q -m Nothing", "10:29:00", "10:31:00"))
		found, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(manual))
		assertLinks(t, links, manual)
		if got := created(found, "session-one"); len(got) != 0 {
			t.Fatalf("created %v", got)
		}
	})
	t.Run("manual commit in the main checkout", func(t *testing.T) {
		r := newReflogRepo(t)
		one := r.worktree("one", "feature-one")
		manual := r.commit(r.main, "main.txt", "By hand", "10:30:00")
		s := reflogSession("session-one", "claude-code", one, bash("git commit -q -m Nothing", "10:29:00", "10:31:00"))
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(manual))
		assertLinks(t, links, manual)
	})
	t.Run("removed worktree beside a live one", func(t *testing.T) {
		r := newReflogRepo(t)
		gone, live := r.worktree("gone", "feature-gone"), r.worktree("live", "feature-live")
		orphan := r.commit(gone, "gone.txt", "Orphan", "10:30:00")
		own := r.commit(live, "live.txt", "Own", "10:30:00")
		commits := append(r.commits(orphan), r.commits(own)...)
		r.git(r.main, "10:40:00", "worktree", "remove", "--force", gone)
		s := reflogSession("session-live", "claude-code", live, bash("git commit -q -m Own", "10:29:58", "10:30:02"))
		_, links := resolveReflog(t, r, []sessions.Session{s}, commits)
		assertLinks(t, links, orphan)
		assertLinks(t, links, own, "session-live")
	})
}

func TestCreatorsOfIgnoresHeadLogsOfUnknownWorktree(t *testing.T) {
	sha := strings.Repeat("a", 40)
	log := reflog{creations: []creation{
		{sha: sha, at: at("10:30:00"), head: true, worktree: ""},
		{sha: sha, at: at("10:30:00"), head: false},
	}}
	windows := []callWindow{{session: "s", start: at("10:29:00"), end: at("10:31:00"), root: "/gone"}}
	if got := creatorsOf(log, windows); len(got) != 0 {
		t.Fatalf("creators = %v", got)
	}
}

func TestWriteDirs(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"git commit -q -m m", []string{"/w"}},
		{"git -C /x commit -q -m m", []string{"/x"}},
		{"git -C sub commit -q", []string{"/w/sub"}},
		{"git -c user.name=a --no-pager commit -m m", []string{"/w"}},
		{"cd /x && git commit -q", []string{"/x"}},
		{"cd sub && git commit -q", []string{"/w/sub"}},
		{"cd -P /x && git commit -q", []string{"/x"}},
		{"pushd /x && git commit", []string{"/x"}},
		{"(cd /x && git commit -q)", []string{"/x"}},
		{"(cd /x); git commit -q", []string{"/w"}},
		{"bash -lc 'git am p.patch'", []string{"/w"}},
		{"bash -lc 'cd /x && git commit -q'", []string{"/x"}},
		{"cd /x && sh -c 'git commit -q'", []string{"/x"}},
		{"FOO=1 git rebase main", []string{"/w"}},
		{"git pull --rebase", []string{"/w"}},
		{"git status; git cherry-pick abc; git revert def", []string{"/w", "/w"}},
		{"git commit -q; cd /x", []string{"/w"}},
		{"git commit -m \"$(cat <<'EOF'\nmessage\nEOF\n)\"", []string{"/w"}},
		{"git --git-dir=/x/.git -C /y commit -q", []string{"/y"}},
		{"git log -- rebase.go", nil},
		{"git log --grep=revert", nil},
		{"git show HEAD:commit.go", nil},
		{"git status && echo commit", nil},
		{"git commit --dry-run", nil},
		{"echo \"done; git commit later\"", nil},
		{"gh pr comment 1 --body \"Rebased; git pull --rebase to update\"", nil},
		{"cat > release.sh <<'EOF'\ngit commit -am release\nEOF", nil},
		{"cd - && git commit", nil},
		{"cd ~ && git commit", nil},
		{"cd $HOME && git commit", nil},
		{"cd \"$DIR\" && git commit", nil},
		{"git -C \"$X\" commit", nil},
		{"if true; then cd /x; fi; git commit -q", nil},
		{"GIT_DIR=/x/.git git commit -m m", nil},
		{"export GIT_DIR=/x/.git; git commit -m m", nil},
		{"git --git-dir=/x/.git commit -m m", nil},
		{"git --work-tree /x commit -m m", nil},
		{"git commit -m 'unterminated", nil},
		{"go test ./...", nil},
	} {
		if got := writeDirs(tc.command, "/w"); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestShellCallsPairCodeModeCommandsWithTheirWorkdirs(t *testing.T) {
	source := `await tools.exec_command({cmd:"git commit -q -m x", workdir:"/a"});
await tools.exec_command({cmd:"cd /b && ls", workdir:"/a"});
await tools.exec_command({cmd:"git commit -q -m y"});
await tools.exec_command({cmd:` + "`git commit -m ${message}`" + `, workdir:"/c"});
await tools.exec_command({cmd:"git commit -q -m z", workdir:dir});`
	input, _ := json.Marshal(source)
	calls := shellCalls("exec", input, "/base")
	want := []shellCall{{"git commit -q -m x", "/a"}, {"cd /b && ls", "/a"}, {"git commit -q -m y", "/base"}}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	var dirs []string
	for _, c := range calls {
		dirs = append(dirs, writeDirs(c.command, c.dir)...)
	}
	if !slices.Equal(dirs, []string{"/a", "/base"}) {
		t.Fatalf("write dirs = %v", dirs)
	}
	stored := traceprocessing.NormalizeCodexExecInput("exec", input)
	if !strings.Contains(string(stored), `"command"`) {
		t.Fatalf("the stored input has no command: %s", stored)
	}
	if got := shellCalls("exec", stored, "/base"); !slices.Equal(got, want) {
		t.Fatalf("object input: %v", got)
	}
	plain := shellCalls("exec_command", json.RawMessage(`{"cmd":"git commit -q","workdir":"sub"}`), "/base")
	if !slices.Equal(plain, []shellCall{{"git commit -q", "/base/sub"}}) {
		t.Fatalf("plain call: %v", plain)
	}
}

func TestReflogLinksCodexCodeModeRebaseByQuotedWorkdir(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("quoted", "feature")
	r.commit(wt, "b.txt", "Feature work", "10:30:00")
	r.commit(r.main, "c.txt", "Main moves on", "10:40:00")
	r.git(wt, "10:50:00", "rebase", "-q", "main")
	rebased := r.git(wt, "10:50:00", "rev-parse", "HEAD")
	source := `text(await tools.exec_command({cmd:"git rebase main; git status --short --branch","workdir":"` + wt + `","sandbox_permissions":"require_escalated"}))`
	s := reflogSession("session-codex", "codex", r.main, codeModeCall(source, "10:49:58", "10:50:02"))
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(rebased))
	assertLinks(t, links, rebased, "session-codex")
}

func TestReflogReadsRelativeWorktreeGitdir(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree(filepath.Join(t.TempDir(), "relative"), "feature")
	sha := r.commit(wt, "b.txt", "Relative", "10:30:00")
	admin := filepath.Join(r.main, ".git", "worktrees", filepath.Base(wt))
	rel, err := filepath.Rel(admin, filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(rel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := reflogSession("session-a", "claude-code", wt, bash("git commit -q -m Relative", "10:29:58", "10:30:02"))
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha, "session-a")
}

func TestReflogCountsSubagentCallsForTheTopLevelSession(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("sub", "feature")
	sha := r.commit(wt, "b.txt", "By a subagent", "10:30:00")
	s := reflogSession("session-top", "claude-code", r.main)
	parent, spawned, cwd := "session-top", "toolu_spawn", wt
	s.Graph.Sessions = append(s.Graph.Sessions, session.Session{ID: "session-child", ParentSessionID: &parent, SpawnedByToolUseID: &spawned, WorkingDir: &cwd})
	start, end := at("10:29:58"), at("10:30:02")
	s.Graph.ToolUses = append(s.Graph.ToolUses, session.ToolUse{ID: "child-use", SessionID: "session-child", Name: "Bash", Verb: session.VerbExecute, Status: session.StatusOK, Input: json.RawMessage(`{"command":"git commit -q -m x"}`), Timestamp: &start})
	s.Graph.ToolResults = append(s.Graph.ToolResults, session.ToolResult{ID: "child-result", ToolUseID: "child-use", SessionID: "session-child", Status: session.StatusOK, Timestamp: &end})
	_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha, "session-top")
}

func TestReflogIgnoresPollsOfRunningCommands(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("poll", "feature")
	sha := r.commit(wt, "b.txt", "Polled", "10:30:00")
	poll := call{tool: "write_stdin", input: `{"session_id":1,"chars":"git commit -q -m x\n"}`, start: at("10:29:58"), end: at("10:30:02")}
	wait := call{tool: "wait", input: `{"session_id":1}`, start: at("10:29:58"), end: at("10:30:02")}
	s := reflogSession("session-codex", "codex", wt, poll, wait)
	found, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
	assertLinks(t, links, sha)
	if got := created(found, "session-codex"); len(got) != 0 {
		t.Fatalf("created %v", got)
	}
}

func TestReflogCallWithoutGitWriteEstablishesNothing(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("script", "feature")
	sha := r.commit(wt, "b.txt", "By a script", "10:30:00")
	hooked := sessions.Session{ID: "session-hook", Vendor: "claude-code", Source: "claude_code", CreatedCommits: []string{sha}}
	runner := reflogSession("session-runner", "claude-code", wt, bash("make release", "10:29:58", "10:30:02"))
	found, links := resolveReflog(t, r, []sessions.Session{hooked, runner}, r.commits(sha))
	assertLinks(t, links, sha, "session-hook")
	if got := created(found, "session-runner"); len(got) != 0 {
		t.Fatalf("created %v", got)
	}
}

func TestReflogGenerationRunIsDroppedNotReassigned(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("generation", "feature")
	sha := r.commit(wt, "b.txt", "By a run", "10:30:00")
	run := reflogSession("session-run", "claude-code", wt, bash("git commit -q -m x", "10:29:58", "10:30:02"))
	run.Graph.UserInputs = append(run.Graph.UserInputs, session.UserInput{ID: "input", SessionID: "session-run", Text: sessions.GenerationRunMarker + " review"})
	bystander := reflogSession("session-bystander", "claude-code", wt, bash("go test ./...", "10:29:58", "10:30:02"))
	commits := r.commits(sha)
	found := addReflogCommits(context.Background(), r.main, []sessions.Session{run, bystander}, commits)
	if !slices.Contains(created(found, "session-run"), sha) {
		t.Fatalf("the run was not the reflog creator: %v", created(found, "session-run"))
	}
	found, _ = withoutGenerationRuns(found, nil)
	_, _, edges, err := linkSessions(context.Background(), found, commits)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.CommitSHA == sha {
			t.Fatalf("the commit was linked to %s by %s", e.RunID, e.Method)
		}
	}
}

func appendReflogLine(t *testing.T, repo, sha, message string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(repo, ".git", "logs", "HEAD"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := fmt.Fprintf(file, "%s %s Grace Hopper <grace@example.com> 1782900000 +0000\t%s\n", strings.Repeat("0", 40), sha, message); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceMemoNoticesNewCreationsOnly(t *testing.T) {
	useEvidenceMemo(t)
	f := linktest.New(t)
	req := evidenceRequest(t, f)
	buildEvidence(t, req)

	loads := counter("sessions.load.calls")
	appendReflogLine(t, f.Repo, f.Commits[0], "checkout: moving from feature to main")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 0 {
		t.Errorf("a HEAD move recomputed the evidence %d times", got)
	}
	appendReflogLine(t, f.Repo, "1111111111111111111111111111111111111111", "commit: elsewhere")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 0 {
		t.Errorf("a creation of another commit recomputed the evidence %d times", got)
	}
	appendReflogLine(t, f.Repo, f.Commits[0], "commit (amend): again")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 1 {
		t.Errorf("a new creation of a pull request commit: %d loads, want 1", got)
	}
}

func reflogLine(sha, seconds, message string) string {
	return strings.Repeat("0", 40) + " " + sha + " Grace Hopper <grace@example.com> " + seconds + " +0000\t" + message + "\n"
}

func TestReadCreationsDegradesGracefully(t *testing.T) {
	sha := strings.Repeat("a", 40)
	wanted := map[string]bool{sha: true}
	path := filepath.Join(t.TempDir(), "HEAD")
	content := "garbage\n\t\nx y z\tcommit: nope\n" +
		reflogLine(sha, "not-a-number", "commit: bad time") +
		strings.Repeat("a", 200<<10) + "\n" +
		"\x00\xff binary \x01\n" +
		reflogLine(sha, "1782900000", "commit: good")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCreations([]reflogFile{{path: path, head: true, worktree: "/w", size: int64(len(content))}}, wanted)
	if len(got) != 1 || got[0].sha != sha || got[0].worktree != "/w" || !got[0].head || got[0].at.Unix() != 1782900000 {
		t.Fatalf("creations = %+v", got)
	}
	if got := readCreations([]reflogFile{{path: filepath.Join(t.TempDir(), "absent")}}, wanted); len(got) != 0 {
		t.Fatalf("an absent reflog gave %+v", got)
	}
}

func TestReadCreationsReadsTheNewestEndOfAnOversizedFile(t *testing.T) {
	oldest, newest := strings.Repeat("a", 40), strings.Repeat("b", 40)
	path := filepath.Join(t.TempDir(), "HEAD")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(file)
	_, _ = w.WriteString(reflogLine(oldest, "1782900000", "commit: old"))
	filler := strings.Repeat("x", 99) + "\n"
	for range (maxReflogBytes + maxReflogBytes/4) / len(filler) {
		_, _ = w.WriteString(filler)
	}
	_, _ = w.WriteString(reflogLine(newest, "1782900100", "commit: new"))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil || info.Size() <= maxReflogBytes {
		t.Fatalf("file size %v, %v", info, err)
	}
	got := readCreations([]reflogFile{{path: path, head: true, worktree: "/w", size: info.Size()}}, map[string]bool{oldest: true, newest: true})
	if len(got) != 1 || got[0].sha != newest {
		t.Fatalf("creations = %+v", got)
	}
}

func writeQuietTranscript(t *testing.T, f linktest.Fixture, id string) {
	t.Helper()
	const usage = `{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"service_tier":"standard"}`
	common := func(kind, uuid, parent, at string) string {
		return fmt.Sprintf(`"cwd":%q,"gitBranch":"feature","isSidechain":false,"sessionId":%q,"type":%q,"uuid":%q,"parentUuid":%s,"timestamp":%q`,
			f.Repo, id, kind, uuid, parent, at)
	}
	lines := []string{
		`{` + common("user", "u-1", "null", "2026-07-01T10:00:00.000Z") + `,"promptId":"p-1","message":{"role":"user","content":"add the feature quietly"}}`,
		`{` + common("assistant", "a-1", `"u-1"`, "2026-07-01T10:29:58.000Z") + `,"requestId":"req_1","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","stop_reason":"tool_use","usage":` + usage +
			`,"content":[{"type":"tool_use","id":"toolu_commit","name":"Bash","input":{"command":"git commit -q -m 'Add the feature'"}}]}}`,
		`{` + common("user", "u-2", `"a-1"`, "2026-07-01T10:30:02.000Z") + `,"promptId":"p-1","message":{"role":"user","content":[{"tool_use_id":"toolu_commit","type":"tool_result","content":"(no output)"}]},"toolUseResult":{"stdout":"","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":true}}`,
	}
	path := filepath.Join(f.Store.ClaudeProjectsDir, claudecode.EncodeProjectDir(f.Repo), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAndLinkCommitsUseTheReflogForAQuietCommit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := linktest.New(t).NoSession(t)
	writeQuietTranscript(t, f, "quiet-session")

	result := resolve(t, request(f))
	first := result.Commits[0]
	if len(result.Sessions) != 1 || result.Sessions[0].ID != "quiet-session" ||
		len(first.Links) != 1 || first.Links[0].Session != "quiet-session" || first.Links[0].Method != "commit_ref" ||
		len(result.Commits[1].Links) != 0 {
		t.Fatalf("Resolve: %+v", result)
	}

	links, err := LinkCommits(context.Background(), f.Repo, commitsOf(t, f), LinkOptions{Sessions: f.Store})
	if err != nil || len(links) != 1 || links[0].Commit != f.Commits[0] {
		t.Fatalf("LinkCommits: %+v %v", links, err)
	}
}
