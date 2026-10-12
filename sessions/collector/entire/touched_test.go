package entire

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTouchedDatesEachCheckpointByItsNewestCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := checkpointRepo(t) // one commit, made now, holding both checkpoints
	run := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date,
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	const when = "2030-01-01T12:00:00Z" // after the first commit
	run(when, "checkout", "-q", CheckpointBranch)
	changed := filepath.Join(dir, "ff", "0123456789", "metadata.json")
	if err := os.WriteFile(changed, []byte(`{"checkpoint_id":"ff0123456789","again":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run(when, "add", ".")
	run(when, "commit", "-q", "-m", "later")
	run(when, "checkout", "-q", "main")

	ctx := context.Background()
	commit, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	touched, err := Touched(ctx, privateRepo(t, dir), commit)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	if len(touched) != 2 || !touched["ff0123456789"].Equal(later) {
		t.Fatalf("touched = %v", touched)
	}
	if first := touched["abcdef012345"]; first.IsZero() || !first.Before(later) {
		t.Errorf("the untouched checkpoint is dated by its own, earlier commit, got %v", first)
	}
}
