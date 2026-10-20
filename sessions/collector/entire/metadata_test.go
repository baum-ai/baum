package entire

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSessionsReadsMetadataOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
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
		// Two sessions, named by ref and by position.
		"aa/0000000001/metadata.json":   `{"checkpoint_id":"aa0000000001","sessions":[{"metadata":"/aa/0000000001/0/metadata.json"},{}]}`,
		"aa/0000000001/0/metadata.json": `{"session_id":"s-1","created_at":"2026-07-01T10:00:00Z"}`,
		"aa/0000000001/1/metadata.json": `{"session_id":"s-2","created_at":"2026-07-01T11:00:00.5+01:00"}`,
		// A session whose metadata is not JSON: the checkpoint is unknown.
		"bb/0000000002/metadata.json":   `{"sessions":[{"metadata":"/bb/0000000002/0/metadata.json"}]}`,
		"bb/0000000002/0/metadata.json": `not json`,
		// A ref that climbs out of the checkpoint.
		"cc/0000000003/metadata.json": `{"sessions":[{"metadata":"/cc/../../etc/metadata.json"}]}`,
		// No sessions listed.
		"dd/0000000004/metadata.json": `{"sessions":[]}`,
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

	ctx := context.Background()
	store, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	objects := privateRepo(t, dir)
	got, err := Sessions(ctx, objects, store, []string{"aa0000000001", "bb0000000002", "cc0000000003", "dd0000000004", "ee0000000005", "not-an-id"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Snapshot{
		{SessionID: "s-1", CreatedAt: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)},
		{SessionID: "s-2", CreatedAt: time.Date(2026, 7, 1, 10, 0, 0, 5e8, time.UTC)},
	}
	if len(got) != 1 || !slices.Equal(got["aa0000000001"], want) {
		t.Errorf("Sessions = %+v, want only aa0000000001 = %+v", got, want)
	}

	if _, err := Sessions(ctx, objects, Store{Branch: "HEAD"}, []string{"aa0000000001"}); err == nil {
		t.Error("a commit that is not an object id was accepted")
	}
}

func TestSessionDir(t *testing.T) {
	for _, tc := range []struct {
		ref   string
		index int
		want  string
		ok    bool
	}{
		{"/aa/0000000001/3/metadata.json", 0, "3", true},
		{"", 2, "2", true},
		{"metadata.json", 1, "1", true},
		{"/aa/../metadata.json", 0, "", false},
		{"/aa/0000000001/.hidden/metadata.json", 0, "", false},
	} {
		got, ok := sessionDir(tc.ref, tc.index)
		if got != tc.want || ok != tc.ok {
			t.Errorf("sessionDir(%q, %d) = %q, %v; want %q, %v", tc.ref, tc.index, got, ok, tc.want, tc.ok)
		}
	}
}
