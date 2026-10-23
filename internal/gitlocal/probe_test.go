package gitlocal

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestProbeFindsTheWorkingTreeFromASubdirectory(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "remote", "add", "origin", "git@github.com:someone/fork.git")
	git(t, f.dir, "remote", "add", "upstream", "https://github.com/acme/widgets.git")
	sub := filepath.Join(f.dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(context.Background(), sub, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	top, _ := filepath.EvalSymlinks(f.dir)
	gotTop, _ := filepath.EvalSymlinks(got.Top)
	gotCommon, _ := filepath.EvalSymlinks(got.CommonDir)
	if gotTop != top || gotCommon != filepath.Join(top, ".git") {
		t.Errorf("top = %s, common = %s", got.Top, got.CommonDir)
	}
	want := []Remote{{"origin", "git@github.com:someone/fork.git"}, {"upstream", "https://github.com/acme/widgets.git"}}
	if !slices.Equal(got.Remotes, want) {
		t.Errorf("remotes = %v, want %v", got.Remotes, want)
	}
	if _, err := Probe(context.Background(), t.TempDir(), 10*time.Second); err == nil {
		t.Error("a directory outside any repository probed")
	}
}
