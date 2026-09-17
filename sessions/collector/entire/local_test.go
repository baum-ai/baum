package entire

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// checkpointRepo builds a clone whose checkpoint branch holds two checkpoints
// and a stray file that must be ignored.
func checkpointRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
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
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "main")
	run("checkout", "-q", "--orphan", CheckpointBranch)
	files := map[string]string{
		"ab/cdef012345/metadata.json":   `{"checkpoint_id":"abcdef012345"}`,
		"ab/cdef012345/0/metadata.json": `{"session_id":"s-1"}`,
		"ff/0123456789/metadata.json":   `{"checkpoint_id":"ff0123456789"}`,
		"README.md":                     "not a checkpoint",
		"zz/short/metadata.json":        "wrong depth",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "checkpoints")
	run("checkout", "-q", "main")
	return dir
}

func TestResolveListRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := checkpointRepo(t)

	commit, ok, err := Resolve(ctx, dir)
	if err != nil || !ok || len(commit) < 40 {
		t.Fatalf("Resolve = %q, %v, %v", commit, ok, err)
	}

	checkpoints, err := List(ctx, dir, commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 2 || checkpoints[0].ID != "abcdef012345" || checkpoints[1].ID != "ff0123456789" {
		t.Fatalf("List = %+v", checkpoints)
	}
	if checkpoints[0].TreeOID == "" {
		t.Error("tree oid missing")
	}

	fsys, err := Read(ctx, dir, commit, "abcdef012345")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(fsys, "0/metadata.json")
	if err != nil || string(data) != `{"session_id":"s-1"}` {
		t.Fatalf("data=%s err=%v", data, err)
	}
	if _, err := fs.ReadFile(fsys, "README.md"); err == nil {
		t.Error("archive must be rooted at the checkpoint, not the branch")
	}
}

func TestResolveWithoutBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_, ok, err := Resolve(context.Background(), dir)
	if err != nil || ok {
		t.Fatalf("Resolve on a clone without checkpoints = ok %v, err %v", ok, err)
	}
}

func TestReadRejectsBadID(t *testing.T) {
	if _, err := Read(context.Background(), t.TempDir(), "HEAD", "../etc"); err == nil {
		t.Fatal("expected an error for a malformed id")
	}
}
