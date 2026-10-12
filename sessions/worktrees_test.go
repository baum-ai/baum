package sessions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// worktreeFixture is a clone with linked worktrees and a session recorded in
// each place: the main checkout, a worktree inside it, a worktree beside it,
// a worktree since deleted, and an unrelated directory whose name the main
// checkout's is a prefix of.
type worktreeFixture struct {
	main, nested, beside, gone, other string
	claudeDir, codexDir               string
}

// session ids by where they were recorded.
const (
	inMain   = "aaaaaaaa-0000-4000-8000-00000000000a"
	inNested = "bbbbbbbb-0000-4000-8000-00000000000b"
	inBeside = "cccccccc-0000-4000-8000-00000000000c"
	inGone   = "dddddddd-0000-4000-8000-00000000000d"
	inOther  = "eeeeeeee-0000-4000-8000-00000000000e"
	codexIn  = "019f0000-0000-7000-8000-0000000000b1"
)

func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	fx := worktreeFixture{
		main:      filepath.Join(base, "work", "demo"),
		beside:    filepath.Join(base, "work", "demo-feature"),
		gone:      filepath.Join(base, "work", "demo-gone"),
		other:     filepath.Join(base, "work", "demo-other"),
		claudeDir: filepath.Join(base, "claude", "projects"),
		codexDir:  filepath.Join(base, "codex", "sessions"),
	}
	fx.nested = filepath.Join(fx.main, ".claude", "worktrees", "fix")
	if err := os.MkdirAll(fx.main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, fx.main, "init", "-q", "-b", "main")
	git(t, fx.main, "commit", "-q", "--allow-empty", "-m", "main")
	git(t, fx.main, "worktree", "add", "-q", "-b", "fix", fx.nested)
	git(t, fx.main, "worktree", "add", "-q", "-b", "feature", fx.beside)
	git(t, fx.main, "worktree", "add", "-q", "-b", "gone", fx.gone)
	if err := os.RemoveAll(fx.gone); err != nil {
		t.Fatal(err)
	}
	// An unrelated clone beside the main checkout, sharing its name's prefix.
	if err := os.MkdirAll(fx.other, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, fx.other, "init", "-q", "-b", "main")

	for id, cwd := range map[string]string{
		inMain: fx.main, inNested: fx.nested, inBeside: filepath.Join(fx.beside, "sub"),
		inGone: fx.gone, inOther: fx.other,
	} {
		// Claude Code files a session under the directory it started in.
		write(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(cwd), id+".jsonl"),
			fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, cwd, id))
	}
	write(t, filepath.Join(fx.codexDir, "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+codexIn+".jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`, codexIn, fx.nested))
	return fx
}

func (fx worktreeFixture) options() Options {
	return Options{
		Sources:           []vendors.Slug{vendors.ClaudeCode, vendors.Codex},
		ClaudeProjectsDir: fx.claudeDir,
		CodexSessionsDir:  fx.codexDir,
		CursorStorePaths:  []string{filepath.Join(fx.claudeDir, "absent.vscdb")},
	}
}

func ids(sessions []Session) []string {
	var out []string
	for _, s := range sessions {
		out = append(out, s.ID)
	}
	slices.Sort(out)
	return out
}

// A session recorded in any worktree of the clone is found from every one of
// them, a deleted worktree included, and the unrelated sibling's never is.
func TestLoadFindsSessionsInEveryWorktree(t *testing.T) {
	fx := newWorktreeFixture(t)
	want := []string{codexIn, inMain, inNested, inBeside, inGone}
	slices.Sort(want)
	for _, from := range []string{fx.main, fx.nested, fx.beside, filepath.Join(fx.main, ".claude")} {
		sessions, report, err := Load(context.Background(), from, fx.options())
		if err != nil {
			t.Fatalf("from %s: %v", from, err)
		}
		if len(report.Failures) != 0 {
			t.Errorf("from %s: failures %+v", from, report.Failures)
		}
		if got := ids(sessions); !slices.Equal(got, want) {
			t.Errorf("from %s:\n got %v\nwant %v", from, got, want)
		}
		for _, s := range sessions {
			// The session is placed the same way from everywhere: in the main
			// checkout, and in its worktree when that lies inside it.
			switch s.ID {
			case inNested, codexIn:
				if s.RepoRoot != fx.main || s.WorktreeRoot != fx.nested {
					t.Errorf("from %s: %s placed in %q / %q", from, s.ID, s.RepoRoot, s.WorktreeRoot)
				}
			case inBeside:
				if s.RepoRoot != fx.beside {
					t.Errorf("from %s: %s placed in %q", from, s.ID, s.RepoRoot)
				}
			}
		}
	}

	// The unrelated sibling is its own repository, and sees only its own.
	sessions, _, err := Load(context.Background(), fx.other, fx.options())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(sessions); !slices.Equal(got, []string{inOther}) {
		t.Errorf("from the sibling: %v", got)
	}
}

// A previous path still adds the sessions recorded where the repository used
// to live, beside the worktrees.
func TestLoadKeepsPreviousPathsWithWorktrees(t *testing.T) {
	fx := newWorktreeFixture(t)
	moved := filepath.Join(filepath.Dir(fx.claudeDir), "attic", "demo")
	const id = "ffffffff-0000-4000-8000-00000000000f"
	write(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(moved), id+".jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, moved, id))
	opts := fx.options()
	opts.PreviousPaths = []string{moved}
	sessions, _, err := Load(context.Background(), fx.nested, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(sessions); !slices.Contains(got, id) || !slices.Contains(got, inMain) || slices.Contains(got, inOther) {
		t.Errorf("sessions = %v", got)
	}
}
