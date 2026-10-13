// Package linktest builds the fixture the local linkage tests share: a real
// clone with a pull request's worth of commits, and a Claude Code store with
// one session that created the first of them. Only tests import it.
package linktest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/sessions"
	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
)

const (
	// Repository is the "owner/name" the clone's origin names.
	Repository = "acme/widgets"
	// UnreachableOrigin names Repository at an address nothing answers on
	// (RFC 5737 TEST-NET-1), so a test that tried to fetch would hang or
	// fail instead of passing quietly.
	UnreachableOrigin = "https://192.0.2.1/acme/widgets.git"
	// SessionID is the id of the one session in the store.
	SessionID = "aaaaaaaa-0000-4000-8000-000000000001"
	// Email is planted in the session's title, and Secret, an access key, too.
	// Neither may reach a response.
	Email  = "ada.lovelace@example.com"
	Secret = "AKIA" + "Z3MQ5TNV2XWB7KDL"
)

// Fixture is a clone, its pull request, and the stores holding the session.
type Fixture struct {
	// Repo is the clone's working directory.
	Repo string
	// Base is the main branch's commit the pull request starts from; Head is
	// the feature branch's tip, and Commits are the two commits between,
	// oldest first. Only Commits[0] was created by the session.
	Base, Head string
	Commits    []string
	// Store points session discovery at the fixture's stores.
	Store sessions.Options
	// Flags are Store as the hidden command-line flags of "baum sessions" and
	// "baum view".
	Flags []string
}

// New builds a Fixture. It skips the test when git is not installed.
func New(t *testing.T) Fixture {
	t.Helper()
	return build(t, "", nil)
}

// NewWithTrailer builds a Fixture whose first commit, the one the session
// created, ends its message with trailer, such as an Entire-Checkpoint line.
func NewWithTrailer(t *testing.T, trailer string) Fixture {
	t.Helper()
	return build(t, trailer, nil)
}

// NewWithFiles builds a Fixture whose first commit also adds files, paths
// relative to the clone, beside b.txt.
func NewWithFiles(t *testing.T, files ...string) Fixture {
	t.Helper()
	return build(t, "", files)
}

func build(t *testing.T, trailer string, files []string) Fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Grace Hopper")
	t.Setenv("GIT_AUTHOR_EMAIL", "grace@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Grace Hopper")
	t.Setenv("GIT_COMMITTER_EMAIL", "grace@example.com")

	base := t.TempDir()
	repo := filepath.Join(base, "widgets")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "remote", "add", "origin", UnreachableOrigin)
	write(t, filepath.Join(repo, "a.txt"), "a\n")
	baseOID := commit(t, repo, "Start", "2026-07-01T09:00:00Z")
	git(t, repo, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(repo, "b.txt"), "b\n")
	for _, file := range files {
		write(t, filepath.Join(repo, filepath.FromSlash(file)), file+"\n")
	}
	message := "Add the feature"
	if trailer != "" {
		message += "\n\n" + trailer
	}
	first := commit(t, repo, message, "2026-07-01T10:30:00Z")
	write(t, filepath.Join(repo, "c.txt"), "c\n")
	second := commit(t, repo, "Tidy up, reported by bob@example.com", "2026-07-01T10:40:00Z")
	git(t, repo, "checkout", "-q", "main")

	claudeDir := filepath.Join(base, "claude")
	write(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(repo), SessionID+".jsonl"), transcript(SessionID, repo, "add the feature", first))
	flags := []string{
		"--claude-dir", claudeDir,
		"--codex-dir", filepath.Join(base, "codex", "sessions"),
		"--cursor-store", filepath.Join(base, "absent.vscdb"),
	}
	return Fixture{
		Repo: repo, Base: baseOID, Head: second, Commits: []string{first, second}, Flags: flags,
		Store: sessions.Options{
			ClaudeProjectsDir: claudeDir,
			CodexSessionsDir:  filepath.Join(base, "codex", "sessions"),
			CursorStorePaths:  []string{filepath.Join(base, "absent.vscdb")},
		},
	}
}

// AddSession writes one more Claude Code session into f's Claude Code store:
// one recorded in cwd, asked prompt, that created the first of f.Commits as
// the fixture's own session did.
func (f Fixture) AddSession(t *testing.T, id, cwd, prompt string) {
	t.Helper()
	write(t, filepath.Join(f.Store.ClaudeProjectsDir, claudecode.EncodeProjectDir(cwd), id+".jsonl"), transcript(id, cwd, prompt, f.Commits[0]))
}

// NoSession returns f's stores with the session removed.
func (f Fixture) NoSession(t *testing.T) Fixture {
	t.Helper()
	empty := t.TempDir()
	f.Store = sessions.Options{
		ClaudeProjectsDir: filepath.Join(empty, "claude"),
		CodexSessionsDir:  filepath.Join(empty, "codex"),
		CursorStorePaths:  []string{filepath.Join(empty, "absent.vscdb")},
	}
	f.Flags = []string{"--claude-dir", f.Store.ClaudeProjectsDir, "--codex-dir", f.Store.CodexSessionsDir, "--cursor-store", f.Store.CursorStorePaths[0]}
	return f
}

// Git runs git in dir for test setup and returns its trimmed output.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return git(t, dir, args...)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, repo, message, date string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_DATE", date)
	t.Setenv("GIT_COMMITTER_DATE", date)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", message)
	return git(t, repo, "rev-parse", "HEAD")
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

// transcript is a Claude Code session in cwd that was asked prompt and ran
// git commit, whose output names the commit.
func transcript(id, cwd, prompt, commitOID string) string {
	const usage = `{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"service_tier":"standard"}`
	common := func(kind, uuid, parent, at string) string {
		return fmt.Sprintf(`"cwd":%q,"gitBranch":"feature","isSidechain":false,"sessionId":%q,"type":%q,"uuid":%q,"parentUuid":%s,"timestamp":%q`,
			cwd, id, kind, uuid, parent, at)
	}
	lines := []string{
		`{` + common("user", "u-1", "null", "2026-07-01T10:00:00.000Z") + fmt.Sprintf(`,"promptId":"p-1","message":{"role":"user","content":%q}}`, prompt),
		`{` + common("assistant", "a-1", `"u-1"`, "2026-07-01T10:20:00.000Z") + `,"requestId":"req_1","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","stop_reason":"tool_use","usage":` + usage +
			`,"content":[{"type":"tool_use","id":"toolu_commit","name":"Bash","input":{"command":"git commit -m 'Add the feature'"}}]}}`,
		`{` + common("user", "u-2", `"a-1"`, "2026-07-01T10:30:00.000Z") + fmt.Sprintf(`,"promptId":"p-1","message":{"role":"user","content":[{"tool_use_id":"toolu_commit","type":"tool_result","content":"[feature %s] Add the feature"}]},"toolUseResult":{"stdout":"[feature %s] Add the feature","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}`, commitOID[:7], commitOID[:7]),
		fmt.Sprintf(`{"type":"ai-title","aiTitle":"Ask %s about the feature (%s)","sessionId":%q}`, Email, Secret, id),
	}
	return strings.Join(lines, "\n") + "\n"
}
