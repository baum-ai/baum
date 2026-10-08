package linktest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	// CheckpointID is the Entire checkpoint AddCheckpoint commits.
	CheckpointID = "0004a7a444a0"
	// CheckpointTrailer names CheckpointID the way Entire does in a commit.
	CheckpointTrailer = "Entire-Checkpoint: " + CheckpointID
)

// CheckpointSessions are the ids of the two Claude Code sessions the
// checkpoint records; neither is SessionID.
var CheckpointSessions = []string{"7f85b0e9-4f7a-4a81-a27e-1393aa1e80f6", "d9f72f5a-32ee-4a09-83dc-6c2d52918c0a"}

// AddCheckpoint commits the two-session checkpoint fixture to the clone's
// Entire checkpoint branch at CheckpointID, and leaves the clone on main.
func (f Fixture) AddCheckpoint(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the checkpoint fixture")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "..", "sessions", "entirecheckpoint", "testdata", "two_claude")
	git(t, f.Repo, "checkout", "-q", "--orphan", "entire/checkpoints/v1")
	git(t, f.Repo, "rm", "-rqf", ".")
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(f.Repo, CheckpointID[:2], CheckpointID[2:], rel), string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, f.Repo, "add", "-A")
	git(t, f.Repo, "commit", "-q", "-m", "Checkpoint: "+CheckpointID)
	git(t, f.Repo, "checkout", "-q", "main")
}
