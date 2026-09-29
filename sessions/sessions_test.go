package sessions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
	"github.com/lastpersonlabs/baum/sessions/collector/entire"
	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// fixture is a fake machine: a git clone whose checkpoint branch holds one
// Entire checkpoint, a Claude Code store with one session in the clone and
// one elsewhere, and a Codex store with one rollout in the clone.
type fixture struct {
	repo, claudeDir, codexDir string
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	fx := fixture{
		repo:      filepath.Join(base, "work", "demo"),
		claudeDir: filepath.Join(base, "claude", "projects"),
		codexDir:  filepath.Join(base, "codex", "sessions"),
	}
	if err := os.MkdirAll(fx.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, fx.repo, "init", "-q", "-b", "main")
	git(t, fx.repo, "commit", "-q", "--allow-empty", "-m", "main")

	// The Claude Code fixture transcript records cwd /repo; point it at the
	// clone so discovery accepts it.
	src := filepath.Join("claudetranscript", "testdata")
	const claudeID = "11111111-2222-3333-4444-555555555555"
	transcript, err := os.ReadFile(filepath.Join(src, claudeID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo))
	write(t, filepath.Join(project, claudeID+".jsonl"), strings.ReplaceAll(string(transcript), `"/repo"`, fmt.Sprintf("%q", fx.repo)))
	if err := filepath.WalkDir(filepath.Join(src, claudeID), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(project, rel), strings.ReplaceAll(string(data), `"/repo"`, fmt.Sprintf("%q", fx.repo)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere")
	write(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(elsewhere), "eeeeeeee-0000-4000-8000-000000000005.jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":"eeeeeeee-0000-4000-8000-000000000005","timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, elsewhere))

	write(t, filepath.Join(fx.codexDir, "2026", "07", "01", "rollout-2026-07-01T10-00-00-019f0000-0000-7000-8000-000000000001.jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000001","cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`, fx.repo))

	// Entire: the two_claude checkpoint fixture committed at its id's path.
	git(t, fx.repo, "checkout", "-q", "--orphan", entire.CheckpointBranch)
	checkpoint := filepath.Join("entirecheckpoint", "testdata", "two_claude")
	if err := filepath.WalkDir(checkpoint, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(checkpoint, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(fx.repo, "00", "04a7a444a0", rel), string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, fx.repo, "add", ".")
	git(t, fx.repo, "commit", "-q", "-m", "checkpoint")
	git(t, fx.repo, "checkout", "-q", "main")
	return fx
}

func (fx fixture) options() Options {
	return Options{
		ClaudeProjectsDir: fx.claudeDir,
		CodexSessionsDir:  fx.codexDir,
		CursorStorePaths:  []string{filepath.Join(fx.claudeDir, "absent.vscdb")},
	}
}

func TestLoadMapsEverySource(t *testing.T) {
	fx := newFixture(t)
	sessions, report, err := Load(context.Background(), fx.repo, fx.options())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 0 {
		t.Errorf("failures: %+v", report.Failures)
	}
	if report.EntireCommit == "" {
		t.Error("checkpoint branch was not read")
	}
	bySource := map[vendors.Slug][]Session{}
	for _, s := range sessions {
		bySource[s.Source] = append(bySource[s.Source], s)
		if s.Graph == nil || s.Root() == nil {
			t.Errorf("%s %s has no root session", s.Source, s.ID)
		}
		if s.RepoRoot != fx.repo {
			t.Errorf("%s %s repo root = %q", s.Source, s.ID, s.RepoRoot)
		}
	}
	if len(bySource[vendors.ClaudeCode]) != 1 || bySource[vendors.ClaudeCode][0].ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("claude sessions = %+v", bySource[vendors.ClaudeCode])
	}
	if len(bySource[vendors.Codex]) != 1 {
		t.Errorf("codex sessions = %+v", bySource[vendors.Codex])
	}
	if len(bySource[Entire]) != 2 {
		t.Errorf("entire sessions = %d, want 2", len(bySource[Entire]))
	}
	for _, s := range bySource[Entire] {
		if s.Vendor != "claude_code" || s.Path != "0004a7a444a0" {
			t.Errorf("entire session = %+v", s)
		}
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i].ModifiedAt.Before(sessions[i-1].ModifiedAt) {
			t.Errorf("sessions are not ordered by modification time")
		}
	}
}

func TestLoadFilters(t *testing.T) {
	fx := newFixture(t)
	opts := fx.options()
	opts.Sources = []vendors.Slug{vendors.Codex}
	sessions, report, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Source != vendors.Codex || report.EntireCommit != "" {
		t.Errorf("vendor filter: %+v report %+v", sessions, report)
	}

	opts = fx.options()
	opts.Since = time.Now().Add(24 * time.Hour)
	sessions, _, err = Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("since filter kept %d sessions", len(sessions))
	}
}

func TestLoadReportsUnmappableSessions(t *testing.T) {
	fx := newFixture(t)
	// A session whose subagent file is not a sidechain transcript fails to
	// decode; its sibling must still be returned.
	const id = "99999999-0000-4000-8000-000000000009"
	project := filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo))
	write(t, filepath.Join(project, id+".jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, fx.repo, id))
	write(t, filepath.Join(project, id, "subagents", "agent-bad.jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"type":"user","uuid":"a-1","sessionId":%q,"timestamp":"2026-07-01T10:00:01.000Z","message":{"role":"user","content":"not a sidechain"}}
`, fx.repo, id))
	opts := fx.options()
	opts.Sources = []vendors.Slug{vendors.ClaudeCode}
	sessions, report, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID == id {
		t.Errorf("the good session must survive its sibling: %+v", sessions)
	}
	if len(report.Failures) != 1 || report.Failures[0].Source != vendors.ClaudeCode {
		t.Errorf("failures = %+v", report.Failures)
	}
}

func TestLoadSurvivesUnreadableClone(t *testing.T) {
	// A .git directory that is not a repository: home-directory sessions are
	// still listed and the checkpoint branch failure is reported.
	base := t.TempDir()
	repo := filepath.Join(base, "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(base, "claude")
	const id = "aaaaaaaa-0000-4000-8000-000000000001"
	write(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(repo), id+".jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, repo, id))
	sessions, report, err := Load(context.Background(), repo, Options{
		ClaudeProjectsDir: claudeDir, CodexSessionsDir: filepath.Join(base, "codex"),
		CursorStorePaths: []string{filepath.Join(base, "absent.vscdb")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != id {
		t.Errorf("sessions = %+v", sessions)
	}
	if len(report.Failures) != 1 || report.Failures[0].Source != Entire {
		t.Errorf("failures = %+v", report.Failures)
	}
}

func TestParseSource(t *testing.T) {
	for _, name := range []string{"claude_code", "codex", "cursor", "entire"} {
		if _, err := ParseSource(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseSource("copilot"); err == nil {
		t.Error("unknown vendor accepted")
	}
}
