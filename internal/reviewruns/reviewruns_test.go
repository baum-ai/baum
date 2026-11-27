package reviewruns

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

func makeRun(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "head"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "head", "a.go"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	// The unpacked checkout is read-only.
	if err := os.Chmod(filepath.Join(dir, "head"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "head"), 0o700) })
	return dir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestExpireRemovesOldRunsOnly(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	old := makeRun(t, root, agent.RunDirPrefix+"old")
	fresh := makeRun(t, root, agent.RunDirPrefix+"fresh")
	unprefixed := makeRun(t, root, "old-notes")
	outside := makeRun(t, elsewhere, agent.RunDirPrefix+"out")
	link := filepath.Join(root, agent.RunDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, agent.RunDirPrefix+"file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-15 * 24 * time.Hour)
	for _, path := range []string{old, unprefixed, outside, file} {
		if err := os.Chtimes(path, long, long); err != nil {
			t.Fatal(err)
		}
	}
	if err := Expire(root, time.Now(), MaxAge); err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Error("the old run stayed")
	}
	for _, path := range []string{fresh, unprefixed, outside, link, file} {
		if !exists(path) {
			t.Errorf("%s was removed", path)
		}
	}
	if err := Expire(filepath.Join(root, "missing"), time.Now(), MaxAge); err != nil {
		t.Errorf("a missing root: %v", err)
	}
}

// A root reached through a symlink expires its runs, and their sessions are
// found under the resolved path the agent CLI recorded.
func TestExpireUnderAliasedRoot(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	dir := makeRun(t, resolvedRoot(root), agent.RunDirPrefix+"alias")
	session := h.claudeSession(t, dir, "c-alias")
	long := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(dir, long, long); err != nil {
		t.Fatal(err)
	}
	if err := Expire(alias, time.Now(), MaxAge); err != nil {
		t.Fatal(err)
	}
	if exists(dir) || exists(session) {
		t.Errorf("left: run %v, session %v", exists(dir), exists(session))
	}
}
