package reviewruns

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/baum-ai/baum/agent"
)

// compressedCodexSession writes a rollout as Codex stores it once it has
// compressed it.
func (h homes) compressedCodexSession(t *testing.T, cwd, id string) string {
	t.Helper()
	line := fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}`+"\n", id, cwd)
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.codex, "sessions", "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+id+".jsonl.zst")
	return write(t, path, buf.String())
}

// Codex compresses old rollouts; a run's compressed ones are removed as its
// plain ones are, and only when they are the run's.
func TestRemoveSessionsRemovesCompressedRollouts(t *testing.T) {
	h := fakeHomes(t)
	root := t.TempDir()
	run := makeRun(t, root, agent.RunDirPrefix+"1")
	byID, keep := h.compressedCodexSession(t, run, "drop-1"), h.compressedCodexSession(t, run, "keep-1")
	foreign := h.compressedCodexSession(t, "/work/elsewhere/repo", "drop-2")
	unreadable := write(t, filepath.Join(h.codex, "sessions", "2026", "07", "01", "rollout-2026-07-01T10-00-00-drop-3.jsonl.zst"), "not zstd")

	if err := RemoveSessions(root, run, "drop-1", "drop-2", "drop-3"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{byID: false, keep: true, foreign: true, unreadable: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
	if err := RemoveSessions(root, run); err != nil {
		t.Fatal(err)
	}
	if exists(keep) {
		t.Error("a compressed rollout of the run stayed")
	}
	if !exists(foreign) {
		t.Error("another directory's compressed rollout was removed")
	}
	_ = os.Remove(unreadable)
}
