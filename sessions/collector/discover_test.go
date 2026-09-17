package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lastpersonlabs/reviewpal/sessions/collector/claudecode"
	"github.com/lastpersonlabs/reviewpal/sessions/vendors"
)

// claudeTranscript builds a minimal parseable Claude Code transcript with a
// sidecar first line (no cwd) so probing has to scan past it.
func claudeTranscript(sessionID, cwd string) string {
	return fmt.Sprintf(`{"type":"mode","mode":"normal","sessionId":%[1]q}
{"parentUuid":null,"isSidechain":false,"cwd":%[2]q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%[1]q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, sessionID, cwd)
}

// codexRollout builds a minimal parseable Codex rollout file.
func codexRollout(sessionID, cwd string) string {
	return fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`, sessionID, cwd)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a fake home layout: a git repository plus Claude Code and Codex
// session stores with sessions in and out of the repository, including
// sessions recorded at a prior repository location (oldRepo) that no longer
// exists on disk.
type fixture struct {
	repo, sub, oldRepo, claudeDir, codexDir, cursorStore string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	fx := fixture{
		repo:        filepath.Join(base, "work", "demo"),
		oldRepo:     filepath.Join(base, "attic", "demo"),
		claudeDir:   filepath.Join(base, "claude", "projects"),
		codexDir:    filepath.Join(base, "codex", "sessions"),
		cursorStore: filepath.Join(base, "cursor", "absent.vscdb"),
	}
	fx.sub = filepath.Join(fx.repo, "platform")
	if err := os.MkdirAll(filepath.Join(fx.repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fx.sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Sibling path whose encoded project name shares the repo's prefix:
	// a candidate directory that cwd verification must reject.
	sibling := fx.repo + "-extra"

	claudeSessions := []struct{ launchDir, sessionID string }{
		{fx.repo, "aaaaaaaa-0000-4000-8000-000000000001"},
		{fx.sub, "bbbbbbbb-0000-4000-8000-000000000002"},
		{sibling, "cccccccc-0000-4000-8000-000000000003"},
		{filepath.Join(base, "elsewhere"), "dddddddd-0000-4000-8000-000000000004"},
		{fx.oldRepo, "ffffffff-0000-4000-8000-000000000006"},
	}
	for _, s := range claudeSessions {
		writeFile(t,
			filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(s.launchDir), s.sessionID+".jsonl"),
			claudeTranscript(s.sessionID, s.launchDir))
	}
	// Sidecar entries inside a matching project directory: ignored.
	writeFile(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo), "subagents", "agent-1.jsonl"),
		claudeTranscript("eeeeeeee-0000-4000-8000-000000000005", fx.repo))
	writeFile(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo), "notes.txt"), "not a transcript")

	writeFile(t, filepath.Join(fx.codexDir, "2026", "07", "01", "rollout-2026-07-01T10-00-00-codex-in-repo.jsonl"),
		codexRollout("codex-in-repo", fx.repo))
	writeFile(t, filepath.Join(fx.codexDir, "2026", "07", "02", "rollout-2026-07-02T10-00-00-codex-elsewhere.jsonl"),
		codexRollout("codex-elsewhere", filepath.Join(base, "elsewhere")))
	writeFile(t, filepath.Join(fx.codexDir, "2026", "07", "03", "rollout-2026-07-03T10-00-00-no-meta.jsonl"),
		`{"timestamp":"2026-07-03T10:00:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`+"\n")
	writeFile(t, filepath.Join(fx.codexDir, "2026", "07", "03", "history.jsonl"),
		codexRollout("not-a-rollout-name", fx.repo))
	writeFile(t, filepath.Join(fx.codexDir, "2026", "06", "01", "rollout-2026-06-01T10-00-00-codex-old-path.jsonl"),
		codexRollout("codex-old-path", fx.oldRepo))
	return fx
}

func (fx fixture) options() FindOptions {
	return FindOptions{
		ClaudeProjectsDir: fx.claudeDir, CodexSessionsDir: fx.codexDir,
		CursorStorePaths: []string{fx.cursorStore},
	}
}

func TestFindSessions(t *testing.T) {
	fx := newFixture(t)

	// Search from a subdirectory: the repo root should be resolved first.
	sessions, _, err := FindSessions(fx.sub, fx.options())
	if err != nil {
		t.Fatalf("FindSessions: %v", err)
	}

	want := map[string]struct {
		source vendors.Slug
		cwd    string
	}{
		"aaaaaaaa-0000-4000-8000-000000000001": {vendors.ClaudeCode, fx.repo},
		"bbbbbbbb-0000-4000-8000-000000000002": {vendors.ClaudeCode, fx.sub},
		"codex-in-repo":                        {vendors.Codex, fx.repo},
	}
	if len(sessions) != len(want) {
		t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(want), sessions)
	}
	for _, s := range sessions {
		w, ok := want[s.SessionID]
		if !ok {
			t.Errorf("unexpected session %q (%s)", s.SessionID, s.Path)
			continue
		}
		if s.Source != w.source {
			t.Errorf("session %q source = %q, want %q", s.SessionID, s.Source, w.source)
		}
		if s.WorkingDir != w.cwd {
			t.Errorf("session %q working dir = %q, want %q", s.SessionID, s.WorkingDir, w.cwd)
		}
		if s.ModifiedAt.IsZero() {
			t.Errorf("session %q has zero ModifiedAt", s.SessionID)
		}
	}
}

func TestFindSessionsPreviousPaths(t *testing.T) {
	fx := newFixture(t)
	opts := fx.options()
	// The repository used to live at oldRepo (which no longer exists on
	// disk); a duplicate of the current root must not double-scan.
	opts.PreviousPaths = []string{fx.oldRepo, fx.repo}

	sessions, _, err := FindSessions(fx.repo, opts)
	if err != nil {
		t.Fatalf("FindSessions: %v", err)
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		if _, dup := byID[s.SessionID]; dup {
			t.Errorf("session %q returned twice", s.SessionID)
		}
		byID[s.SessionID] = s
	}
	if len(sessions) != 5 {
		t.Fatalf("got %d sessions, want 5: %+v", len(sessions), sessions)
	}
	if s, ok := byID["ffffffff-0000-4000-8000-000000000006"]; !ok {
		t.Error("claude session at previous path not found")
	} else if s.WorkingDir != fx.oldRepo {
		t.Errorf("previous-path claude session working dir = %q, want %q", s.WorkingDir, fx.oldRepo)
	}
	if _, ok := byID["codex-old-path"]; !ok {
		t.Error("codex session at previous path not found")
	}
}

func TestFindSessionsIncludesCursorTreesAtPreviousPath(t *testing.T) {
	fx := newFixture(t)
	opts := FindOptions{
		ClaudeProjectsDir: filepath.Join(t.TempDir(), "absent-claude"),
		CodexSessionsDir:  filepath.Join(t.TempDir(), "absent-codex"),
		CursorStorePaths: []string{
			filepath.Join("..", "cursortranscript", "testdata", "store.vscdb"),
		},
		PreviousPaths: []string{"/repo"},
	}
	sessions, report, err := FindSessions(fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 0 || len(sessions) != 3 {
		t.Fatalf("Cursor sessions/report = %+v / %+v", sessions, report)
	}
	for _, found := range sessions {
		if found.Source != vendors.Cursor || len(found.Files) != 1 || found.Files[0].Data == nil {
			t.Fatalf("Cursor session = %+v", found)
		}
	}
}

func TestFindSessionsOrderedByModTime(t *testing.T) {
	fx := newFixture(t)
	newest := filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo), "aaaaaaaa-0000-4000-8000-000000000001.jsonl")
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(newest, future, future); err != nil {
		t.Fatal(err)
	}

	sessions, _, err := FindSessions(fx.repo, fx.options())
	if err != nil {
		t.Fatalf("FindSessions: %v", err)
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i].ModifiedAt.Before(sessions[i-1].ModifiedAt) {
			t.Fatalf("sessions not ordered by ModifiedAt: %s (%v) after %s (%v)",
				sessions[i].SessionID, sessions[i].ModifiedAt, sessions[i-1].SessionID, sessions[i-1].ModifiedAt)
		}
	}
	if last := sessions[len(sessions)-1].Path; last != newest {
		t.Errorf("last session = %s, want most recently modified %s", last, newest)
	}
}

func TestFindSessionsMissingStores(t *testing.T) {
	fx := newFixture(t)
	sessions, _, err := FindSessions(fx.repo, FindOptions{
		ClaudeProjectsDir: filepath.Join(t.TempDir(), "absent"),
		CodexSessionsDir:  filepath.Join(t.TempDir(), "absent"),
		CursorStorePaths:  []string{filepath.Join(t.TempDir(), "absent")},
	})
	if err != nil {
		t.Fatalf("FindSessions with missing stores: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions from missing stores, want 0", len(sessions))
	}
}

func TestFindSessionsNotARepo(t *testing.T) {
	if _, _, err := FindSessions(t.TempDir(), FindOptions{}); err == nil {
		t.Fatal("FindSessions outside a git repository succeeded, want error")
	}
}

func TestRepoRoot(t *testing.T) {
	fx := newFixture(t)
	root, err := RepoRoot(fx.sub)
	if err != nil {
		t.Fatalf("RepoRoot(%s): %v", fx.sub, err)
	}
	if root != fx.repo {
		t.Errorf("RepoRoot = %q, want %q", root, fx.repo)
	}

	// A .git file (linked worktree) counts too.
	worktree := filepath.Join(t.TempDir(), "wt")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: /somewhere/.git/worktrees/wt\n")
	root, err = RepoRoot(worktree)
	if err != nil {
		t.Fatalf("RepoRoot on worktree: %v", err)
	}
	if root != worktree {
		t.Errorf("RepoRoot on worktree = %q, want %q", root, worktree)
	}

	if _, err := RepoRoot(t.TempDir()); err == nil {
		t.Error("RepoRoot outside a repository succeeded, want error")
	}
}

// A session that ran in a git worktree nested inside the checkout has to record
// the worktree too, because that is the directory it belongs to. A live worktree
// holds a .git file, so the same walk finds it; a deleted one is found through
// git's bookkeeping entry, which outlives it.
func TestFindSessionsRecordsRepoAndWorktreeRoots(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "demo")
	sub := filepath.Join(repo, "platform")
	worktree := filepath.Join(repo, ".worktrees", "feature")
	deleted := filepath.Join(repo, ".worktrees", "shipped")
	claudeDir := filepath.Join(base, "claude", "projects")
	for _, dir := range []string{filepath.Join(repo, ".git"), sub, worktree} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "feature")+"\n")
	// The shipped worktree's directory is gone; only its entry remains. The
	// relocated one is gone too, and its entry names the .git file relative to
	// itself, as git does with worktree.useRelativePaths set.
	writeFile(t, filepath.Join(repo, ".git", "worktrees", "shipped", "gitdir"), filepath.Join(deleted, ".git")+"\n")
	relocated := filepath.Join(repo, ".worktrees", "relocated")
	writeFile(t, filepath.Join(repo, ".git", "worktrees", "relocated", "gitdir"),
		filepath.Join("..", "..", "..", ".worktrees", "relocated", ".git")+"\n")
	// The reused directory was once the worktree its stale entry still names,
	// but it exists and is a plain subdirectory now, and presence wins.
	reused := filepath.Join(repo, "reused")
	if err := os.MkdirAll(reused, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".git", "worktrees", "reused", "gitdir"), filepath.Join(reused, ".git")+"\n")
	cwds := map[string]string{
		"aaaaaaaa-0000-4000-8000-00000000000a": sub,
		"bbbbbbbb-0000-4000-8000-00000000000b": worktree,
		"cccccccc-0000-4000-8000-00000000000c": deleted,
		"ffffffff-0000-4000-8000-00000000000f": relocated,
		"99999999-0000-4000-8000-000000000009": reused,
	}
	for id, cwd := range cwds {
		writeFile(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(cwd), id+".jsonl"), claudeTranscript(id, cwd))
	}

	// A worktree git no longer remembers cannot be told from a subdirectory since
	// deleted. The checkout is recorded either way; what must not happen is a
	// worktree named on a guess.
	forgotten := filepath.Join(repo, ".worktrees", "forgotten")
	cwds["dddddddd-0000-4000-8000-00000000000d"] = forgotten
	writeFile(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(forgotten), "dddddddd-0000-4000-8000-00000000000d.jsonl"),
		claudeTranscript("dddddddd-0000-4000-8000-00000000000d", forgotten))

	// The walk climbs to the nearest directory that still exists, so for a gone
	// working directory it describes somewhere above the session and can name a
	// root shallower than the frame it ran in. Here it would reach an inner
	// clone git never listed as a worktree; only git's memory speaks for a
	// directory that is gone, and it says nothing about this one.
	inner := filepath.Join(repo, "vendor", "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	vanished := filepath.Join(inner, "pkg")
	cwds["eeeeeeee-0000-4000-8000-00000000000e"] = vanished
	writeFile(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(vanished), "eeeeeeee-0000-4000-8000-00000000000e.jsonl"),
		claudeTranscript("eeeeeeee-0000-4000-8000-00000000000e", vanished))

	sessions, _, err := FindSessions(repo, FindOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  filepath.Join(base, "codex"),
		CursorStorePaths:  []string{filepath.Join(base, "absent.vscdb")},
	})
	if err != nil {
		t.Fatalf("FindSessions: %v", err)
	}
	if len(sessions) != len(cwds) {
		t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(cwds), sessions)
	}
	for _, s := range sessions {
		wantWorktree := ""
		if s.WorkingDir == worktree || s.WorkingDir == deleted || s.WorkingDir == relocated {
			wantWorktree = s.WorkingDir
		}
		if s.RepoRoot != repo || s.WorktreeRoot != wantWorktree {
			t.Errorf("session in %q recorded roots %q/%q, want %q/%q",
				s.WorkingDir, s.RepoRoot, s.WorktreeRoot, repo, wantWorktree)
		}
	}
}
