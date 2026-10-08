package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// TestLoadNeverFetchesFromAPartialClone reads the Entire checkpoint branch of a
// --filter=blob:none clone whose promisor remote is reachable. Loading must
// leave the checkpoint's blob where it was, absent, and report the checkpoint
// as one it could not read.
func TestLoadNeverFetchesFromAPartialClone(t *testing.T) {
	src := t.TempDir()
	git(t, src, "init", "-q", "-b", "main")
	git(t, src, "config", "uploadpack.allowFilter", "true")
	git(t, src, "config", "uploadpack.allowAnySHA1InWant", "true")
	write(t, filepath.Join(src, "a.txt"), "a\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "base")
	git(t, src, "checkout", "-q", "--orphan", entireBranch)
	git(t, src, "rm", "-rqf", ".")
	write(t, filepath.Join(src, "ab", "cdef012345", "metadata.json"), `{"checkpoint_id":"abcdef012345"}`+"\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "checkpoint")
	blob := gitOutput(t, src, "rev-parse", entireBranch+":ab/cdef012345/metadata.json")
	git(t, src, "checkout", "-q", "main")

	base := t.TempDir()
	clone := filepath.Join(base, "clone")
	if out, err := exec.Command("git", "clone", "-q", "--filter=blob:none", "file://"+filepath.ToSlash(src), clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	git(t, clone, "fetch", "-q", "origin", "refs/heads/*:refs/remotes/origin/*")
	if present(t, clone, blob) {
		t.Fatal("the checkpoint blob is already in the partial clone")
	}

	_, report, err := Load(context.Background(), clone, Options{
		Sources:           []vendors.Slug{Entire},
		ClaudeProjectsDir: filepath.Join(base, "claude"),
		CodexSessionsDir:  filepath.Join(base, "codex"),
		CursorStorePaths:  []string{filepath.Join(base, "absent.vscdb")},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if present(t, clone, blob) {
		t.Fatal("Load fetched the checkpoint blob from the promisor remote")
	}
	if len(report.Failures) != 1 || report.Failures[0].Source != Entire {
		t.Errorf("failures = %+v, want the one unreadable checkpoint", report.Failures)
	}
}

const entireBranch = "entire/checkpoints/v1"

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// present reports whether the object is in the repository, without letting git
// fetch it.
func present(t *testing.T, dir, oid string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", oid)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return cmd.Run() == nil
}
