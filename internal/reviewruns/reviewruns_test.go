package reviewruns

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/store"
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

func session(dir string) store.Metadata {
	return store.Metadata{Session: &store.ReviewSession{Backend: "claude", ID: "x", Directory: dir}}
}

func TestPruneVersionsKeepsTheLatestTwo(t *testing.T) {
	root := t.TempDir()
	dirs := []string{
		makeRun(t, root, agent.RunDirPrefix+"3"), makeRun(t, root, agent.RunDirPrefix+"2"),
		makeRun(t, root, agent.RunDirPrefix+"1"), makeRun(t, root, agent.RunDirPrefix+"0"),
	}
	versions := []store.Metadata{session(dirs[0]), session(dirs[1]), session(dirs[2]), {}, session(dirs[3])}
	if err := PruneVersions(root, versions); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, true, false, false} {
		if exists(dirs[i]) != want {
			t.Errorf("%s exists = %v, want %v", dirs[i], !want, want)
		}
	}
}

func TestPruneVersionsNeverTouchesOtherPaths(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	outside := makeRun(t, elsewhere, agent.RunDirPrefix+"out")
	unprefixed := makeRun(t, root, "notes")
	nested := makeRun(t, filepath.Join(root, "sub"), agent.RunDirPrefix+"nested")
	link := filepath.Join(root, agent.RunDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	keep := makeRun(t, root, agent.RunDirPrefix+"keep")
	versions := []store.Metadata{session(keep), session(keep), session(outside), session(unprefixed), session(nested), session(link), session(root)}
	if err := PruneVersions(root, versions); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, unprefixed, nested, link, keep, root} {
		if !exists(path) {
			t.Errorf("%s was removed", path)
		}
	}
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

func TestCreateMakesAPrivateRunDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "review-runs")
	dir, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 || !candidate(root, dir) {
		t.Fatalf("%v %v candidate=%v", info, err, candidate(root, dir))
	}
	if info, err = os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("root: %v %v", info, err)
	}
}
