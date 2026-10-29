package reviewruns

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

type homes struct{ claude, codex string }

func fakeHomes(t *testing.T) homes {
	t.Helper()
	h := homes{claude: filepath.Join(t.TempDir(), "claude"), codex: filepath.Join(t.TempDir(), "codex")}
	t.Setenv("CLAUDE_CONFIG_DIR", h.claude)
	t.Setenv("CODEX_HOME", h.codex)
	return h
}

func write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeSession writes a Claude Code transcript as the CLI stores it.
func (h homes) claudeSession(t *testing.T, cwd, id string) string {
	line := fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":%q}`+"\n", cwd, id)
	path := write(t, filepath.Join(h.claude, "projects", claudecode.EncodeProjectDir(cwd), id+".jsonl"), line)
	write(t, filepath.Join(h.claude, "projects", claudecode.EncodeProjectDir(cwd), id, "subagents", "a.jsonl"), "{}\n")
	return path
}

// codexSession writes a rollout as Codex stores it, the cwd in session_meta.
func (h homes) codexSession(t *testing.T, cwd, id string) string {
	line := fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}`+"\n", id, cwd)
	return write(t, filepath.Join(h.codex, "sessions", "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+id+".jsonl"), line)
}

func TestEncodingMatchesClaudeCode(t *testing.T) {
	if got := claudecode.EncodeProjectDir("/tmp/x/baum-agent-run-smoke"); got != "-tmp-x-baum-agent-run-smoke" {
		t.Errorf("encoded = %q", got)
	}
}

func TestRemoveSessionsByIDAndAll(t *testing.T) {
	h := fakeHomes(t)
	root := t.TempDir()
	run := makeRun(t, root, agent.RunDirPrefix+"1")
	other := makeRun(t, root, agent.RunDirPrefix+"2")
	keepClaude, dropClaude := h.claudeSession(t, run, "keep-1"), h.claudeSession(t, run, "drop-1")
	keepCodex, dropCodex := h.codexSession(t, run, "keep-2"), h.codexSession(t, run, "drop-2")
	otherClaude, otherCodex := h.claudeSession(t, other, "o-1"), h.codexSession(t, other, "o-2")
	// A session with an id of the run but another cwd is not the run's.
	foreign := h.codexSession(t, "/work/elsewhere/repo", "drop-3")

	if err := RemoveSessions(root, run, "drop-1", "drop-2", "drop-3"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{keepClaude: true, dropClaude: false, keepCodex: true, dropCodex: false, otherClaude: true, otherCodex: true, foreign: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
	if exists(filepath.Join(filepath.Dir(dropClaude), "drop-1")) {
		t.Error("the session's sidecar directory stayed")
	}

	if err := RemoveSessions(root, run); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{keepClaude: false, keepCodex: false, otherClaude: true, otherCodex: true, foreign: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
	if exists(filepath.Dir(keepClaude)) {
		t.Error("the run's Claude project directory stayed")
	}
}

func TestRemoveSessionsNeverTouchesDirectoriesOutsideTheRoot(t *testing.T) {
	h := fakeHomes(t)
	root, elsewhere := t.TempDir(), t.TempDir()
	for _, dir := range []string{
		filepath.Join(elsewhere, agent.RunDirPrefix+"x"), // not under the root
		filepath.Join(root, "notes"),                     // no prefix
		filepath.Join(root, "sub", agent.RunDirPrefix+"n"),
		root, "/",
	} {
		c, x := h.claudeSession(t, dir, "s-1"), h.codexSession(t, dir, "s-2")
		if err := RemoveSessions(root, dir); err != nil {
			t.Fatal(err)
		}
		if !exists(c) || !exists(x) {
			t.Errorf("sessions of %s were removed", dir)
		}
	}
}

func TestRemoveSessionsDoesNotFollowSymlinks(t *testing.T) {
	h := fakeHomes(t)
	root := t.TempDir()
	run := filepath.Join(root, agent.RunDirPrefix+"1")
	precious := t.TempDir()
	write(t, filepath.Join(precious, "keep.txt"), "x")
	// The project directory and a rollout are links to things outside.
	projects := filepath.Join(h.claude, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(precious, filepath.Join(projects, claudecode.EncodeProjectDir(run))); err != nil {
		t.Fatal(err)
	}
	rollout := h.codexSession(t, run, "s-2")
	moved := filepath.Join(precious, "rollout.jsonl")
	if err := os.Rename(rollout, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, rollout); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSessions(root, run); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(precious, "keep.txt")) || !exists(moved) {
		t.Error("something a link pointed at was removed")
	}
}

func TestRemoveSessionsIgnoresMissingStores(t *testing.T) {
	fakeHomes(t)
	root := t.TempDir()
	if err := RemoveSessions(root, filepath.Join(root, agent.RunDirPrefix+"1")); err != nil {
		t.Error(err)
	}
	if err := RemoveSessions(root, filepath.Join(root, agent.RunDirPrefix+"1"), "not a/valid id"); err == nil {
		t.Error("an invalid session id was accepted")
	}
}

func TestPruningAlsoRemovesTheRunsSessions(t *testing.T) {
	h := fakeHomes(t)
	root := t.TempDir()
	resolved := resolvedRoot(root)
	oldest := makeRun(t, resolved, agent.RunDirPrefix+"0")
	newer := makeRun(t, resolved, agent.RunDirPrefix+"1")
	expired := makeRun(t, resolved, agent.RunDirPrefix+"2")
	fresh := makeRun(t, resolved, agent.RunDirPrefix+"3")
	files := map[string][]string{}
	for _, dir := range []string{oldest, newer, expired, fresh} {
		files[dir] = []string{h.claudeSession(t, dir, "c-"+filepath.Base(dir)), h.codexSession(t, dir, "x-"+filepath.Base(dir))}
	}
	long := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(expired, long, long); err != nil {
		t.Fatal(err)
	}
	if err := PruneVersions(root, []store.Metadata{session(newer), session(newer), session(oldest)}); err != nil {
		t.Fatal(err)
	}
	if err := Expire(root, time.Now(), MaxAge); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{oldest: false, newer: true, expired: false, fresh: true} {
		for _, path := range files[dir] {
			if exists(path) != want {
				t.Errorf("%s exists = %v, want %v", path, !want, want)
			}
		}
		if exists(dir) != want {
			t.Errorf("%s exists = %v, want %v", dir, !want, want)
		}
	}
}
