package locallink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
)

// codexRepo is a clone whose Codex sessions are rollout files ingested the way
// production ingests them, so tool inputs are stored as production stores them.
type codexRepo struct {
	*reflogRepo
	opts sessions.Options
}

func newCodexRepo(t *testing.T) *codexRepo {
	t.Helper()
	r := newReflogRepo(t)
	r.git(r.main, "10:00:30", "remote", "add", "origin", linktest.UnreachableOrigin)
	store := t.TempDir()
	return &codexRepo{reflogRepo: r, opts: sessions.Options{
		ClaudeProjectsDir: filepath.Join(store, "claude"),
		CodexSessionsDir:  filepath.Join(store, "codex", "sessions"),
		CursorStorePaths:  []string{filepath.Join(store, "absent.vscdb")},
	}}
}

// rolloutCall is one code-mode `exec` call of a rollout.
type rolloutCall struct {
	program, start, end string
	output              string
}

// rollout writes a Codex rollout for a session started in the main checkout.
func (c *codexRepo) rollout(id string, calls ...rolloutCall) {
	c.t.Helper()
	stamp := func(clock string) string { return "2026-07-01T" + clock + ".000Z" }
	envelope := func(clock, class, payload string) string {
		return `{"timestamp":"` + stamp(clock) + `","type":"` + class + `","payload":` + payload + `}`
	}
	meta, _ := json.Marshal(map[string]string{"id": id, "timestamp": stamp("10:00:00"), "cwd": c.main, "cli_version": "0.129.0", "source": "cli"})
	turn, _ := json.Marshal(map[string]string{"turn_id": "turn", "cwd": c.main, "model": "gpt-5"})
	lines := []string{
		envelope("10:00:00", "session_meta", string(meta)),
		envelope("10:00:01", "event_msg", `{"type":"task_started","turn_id":"turn"}`),
		envelope("10:00:02", "turn_context", string(turn)),
		envelope("10:00:03", "event_msg", `{"type":"user_message","message":"Commit it"}`),
	}
	for i, call := range calls {
		id := fmt.Sprintf("call_%d", i)
		program, _ := json.Marshal(call.program)
		output, _ := json.Marshal([]map[string]string{
			{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": call.output},
		})
		lines = append(lines,
			envelope(call.start, "response_item", `{"type":"custom_tool_call","name":"exec","input":`+string(program)+`,"call_id":"`+id+`","status":"completed"}`),
			envelope(call.end, "response_item", `{"type":"custom_tool_call_output","call_id":"`+id+`","output":`+string(output)+`}`),
		)
	}
	name := "rollout-2026-07-01T10-00-00-" + id + ".jsonl"
	path := filepath.Join(c.opts.CodexSessionsDir, "2026", "07", "01", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

// resolve links the commits from c.base to head.
func (c *codexRepo) resolve(head string) Result {
	return resolve(c.t, Request{Dir: c.main, Repository: linktest.Repository, Head: head, Base: c.base, Sessions: c.opts})
}

func assertCodexLinked(t *testing.T, result Result, id string) {
	t.Helper()
	links := result.Commits[len(result.Commits)-1].Links
	if len(links) != 1 || links[0].Session != id || links[0].Method != "commit_ref" {
		t.Fatalf("links = %+v, want %s by commit_ref", links, id)
	}
}

func assertCodexUnlinked(t *testing.T, result Result) {
	t.Helper()
	for _, c := range result.Commits {
		if len(c.Links) != 0 {
			t.Fatalf("links = %+v, want none", c.Links)
		}
	}
}

const codexSession = "019f0000-0000-7000-8000-000000000001"

func TestCodexRolloutRealCommitShapeLinksThroughTheStoredInput(t *testing.T) {
	c := newCodexRepo(t)
	wt := c.worktree("real", "feature")
	sha := c.commit(wt, "b.txt", "Subject", "10:30:00")
	program := `text(await tools.exec_command({cmd:"git add a b\n git diff --cached --check\n git commit -m 'Subject' -m 'Body text.'","workdir":"` + wt + `","sandbox_permissions":"require_escalated","justification":"May I commit?","max_output_tokens":2500,"yield_time_ms":1000}));` + "\n" +
		`text(await tools.mcp__codex_apps__github_get_user_login({}));` + "\n"
	c.rollout(codexSession, rolloutCall{program: program, start: "10:29:58", end: "10:30:02", output: `{"chunk_id":"a","exit_code":0,"output":"[feature abc] Subject"}`})
	result := c.resolve(sha)
	assertCodexLinked(t, result, codexSession)

	// The stored input is the normalized object, with the program and its commands.
	graph := result.Graphs[codexSession]
	if graph == nil || len(graph.ToolUses) != 1 {
		t.Fatalf("graph = %+v", graph)
	}
	use := graph.ToolUses[0]
	var stored struct{ Input, Command string }
	if err := json.Unmarshal(use.Input, &stored); err != nil || stored.Input == "" || !strings.Contains(stored.Command, "git commit -m 'Subject'") || use.Status != session.StatusOK {
		t.Fatalf("stored input %s, status %q, %v", use.Input, use.Status, err)
	}
}

func TestCodexRolloutCommandsKeepTheirOwnWorkdir(t *testing.T) {
	t.Run("a command in another workdir does not place the commit", func(t *testing.T) {
		c := newCodexRepo(t)
		a, b := c.worktree("a", "feature-a"), c.worktree("b", "feature-b")
		own := c.commit(a, "a.txt", "Own", "10:30:00")
		manual := c.commit(b, "b.txt", "By hand", "10:30:01")
		program := `await tools.exec_command({cmd:"git commit -q -m Own",workdir:"` + a + `"});` + "\n" +
			`await tools.exec_command({cmd:"ls",workdir:"` + b + `"});` + "\n"
		c.rollout(codexSession, rolloutCall{program: program, start: "10:29:58", end: "10:30:03", output: "ok"})
		assertCodexLinked(t, c.resolve(own), codexSession)
		assertCodexUnlinked(t, c.resolve(manual))
	})
	t.Run("a cd in one command does not move the next", func(t *testing.T) {
		c := newCodexRepo(t)
		a, b := c.worktree("a", "feature-a"), c.worktree("b", "feature-b")
		own := c.commit(a, "a.txt", "Own", "10:30:00")
		program := `await tools.exec_command({cmd:"cd ` + b + ` && ls",workdir:"` + a + `"});` + "\n" +
			`await tools.exec_command({cmd:"git commit -q -m Own",workdir:"` + a + `"});` + "\n"
		c.rollout(codexSession, rolloutCall{program: program, start: "10:29:58", end: "10:30:03", output: "ok"})
		assertCodexLinked(t, c.resolve(own), codexSession)
	})
	t.Run("a quoted workdir key places a rebase pick", func(t *testing.T) {
		c := newCodexRepo(t)
		wt := c.worktree("quoted", "feature")
		c.commit(wt, "b.txt", "Feature work", "10:30:00")
		c.commit(c.main, "c.txt", "Main moves on", "10:40:00")
		c.git(wt, "10:50:00", "rebase", "-q", "main")
		rebased := c.git(wt, "10:50:00", "rev-parse", "HEAD")
		program := `text(await tools.exec_command({cmd:"git rebase main; git status --short --branch","workdir":"` + wt + `","sandbox_permissions":"require_escalated"}));` + "\n"
		c.rollout(codexSession, rolloutCall{program: program, start: "10:49:58", end: "10:50:02", output: "ok"})
		assertCodexLinked(t, c.resolve(rebased), codexSession)
	})
}

func TestCodexRolloutWorkdirsItCannotSeeGiveNoEvidence(t *testing.T) {
	for name, program := range map[string]string{
		"shorthand workdir": `await tools.exec_command({cmd:"git commit -q -m x", workdir});` + "\n",
		"spread":            `await tools.exec_command({...opts, cmd:"git commit -q -m x"});` + "\n",
		"held in a list":    `const steps = [{cmd:"git commit -q -m x"}]; for (const s of steps) await tools.exec_command(s);` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := newCodexRepo(t)
			sha := c.commit(c.main, "b.txt", "In the checkout the session started in", "10:30:00")
			c.rollout(codexSession, rolloutCall{program: program, start: "10:29:58", end: "10:30:02", output: "ok"})
			assertCodexUnlinked(t, c.resolve(sha))
		})
	}
}

func TestReflogExtendsAYieldedCommandToItsLastPoll(t *testing.T) {
	r := newReflogRepo(t)
	wt := r.worktree("slow", "feature")
	sha := r.commit(wt, "b.txt", "Slow hooks", "10:29:30")
	start := call{tool: "exec_command", input: `{"cmd":"git commit -q -m x","workdir":"` + wt + `"}`, start: at("10:29:00"), end: at("10:29:02"),
		output: "Chunk ID: a\nWall time: 1.0 seconds\nProcess running with session ID 4242\nOriginal token count: 0\nOutput:\n"}
	running := call{tool: "write_stdin", input: `{"session_id":4242,"chars":""}`, start: at("10:29:10"), end: at("10:29:12"),
		output: "Chunk ID: b\nProcess running with session ID 4242\n"}
	finished := call{tool: "write_stdin", input: `{"session_id":4242,"chars":""}`, start: at("10:29:40"), end: at("10:29:42"),
		output: "Chunk ID: c\nProcess exited with code 0\n"}

	t.Run("the commit lands between polls", func(t *testing.T) {
		s := reflogSession("session-slow", "codex", wt, start, running, finished)
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
		assertLinks(t, links, sha, "session-slow")
	})
	t.Run("without polls the window ends with the call", func(t *testing.T) {
		s := reflogSession("session-slow", "codex", wt, start)
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
		assertLinks(t, links, sha)
	})
	t.Run("a poll of another process does not extend it", func(t *testing.T) {
		other := finished
		other.input = `{"session_id":4343,"chars":""}`
		s := reflogSession("session-slow", "codex", wt, start, other)
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
		assertLinks(t, links, sha)
	})
	t.Run("a failed poll does not extend it", func(t *testing.T) {
		failed := running
		failed.status = session.StatusError
		s := reflogSession("session-slow", "codex", wt, start, failed)
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(sha))
		assertLinks(t, links, sha)
	})
	t.Run("a poll after the process ended is another process", func(t *testing.T) {
		reused := finished
		reused.start, reused.end = at("10:35:00"), at("10:35:02")
		late := r.commit(wt, "c.txt", "Another", "10:35:01")
		s := reflogSession("session-slow", "codex", wt, start, finished, reused)
		_, links := resolveReflog(t, r, []sessions.Session{s}, r.commits(late))
		assertLinks(t, links, late)
	})
}

func TestCodexRolloutYieldedCommandIsExtendedByCodeModePolls(t *testing.T) {
	c := newCodexRepo(t)
	wt := c.worktree("yield", "feature")
	sha := c.commit(wt, "b.txt", "Slow hooks", "10:29:30")
	first := `text(await tools.exec_command({cmd:"git commit -q -m x","workdir":"` + wt + `","yield_time_ms":1000}));` + "\n"
	poll := `text(await tools.write_stdin({session_id:76741,chars:"",yield_time_ms:1000,max_output_tokens:1000}));` + "\n"
	c.rollout(codexSession,
		rolloutCall{program: first, start: "10:29:00", end: "10:29:02", output: `{"chunk_id":"a","wall_time_seconds":1.0,"session_id":76741,"original_token_count":0,"output":""}`},
		rolloutCall{program: poll, start: "10:29:40", end: "10:29:42", output: `{"chunk_id":"b","wall_time_seconds":0.5,"exit_code":0,"output":"[feature abc] x"}`},
	)
	assertCodexLinked(t, c.resolve(sha), codexSession)
}

func TestNestedRepositoryDirectoriesGiveNoEvidence(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"vendor/sub/.git", "pkg"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if !nestedRepository(filepath.Join(root, "vendor", "sub"), root) || !nestedRepository(filepath.Join(root, "vendor", "sub", "deep"), root) {
		t.Error("a submodule was not recognised")
	}
	if nestedRepository(filepath.Join(root, "pkg"), root) || nestedRepository(root, root) || nestedRepository(root, "") {
		t.Error("a plain directory was taken for a nested repository")
	}
}
