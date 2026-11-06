package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestReposAddPreservesDesktopSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	r, err := repos.Open("")
	if err != nil {
		t.Fatal(err)
	}
	clone, err := r.AddPatch(t.Context(), path, func(s repos.Settings) (repos.Settings, error) { off := false; s.Automatic = &off; return s, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, _, err := run(t, "internal", "repos", "add", "--", path)
		if err != nil || strings.TrimSpace(out) != clone.ID {
			t.Fatalf("%s %v", out, err)
		}
	}
	r, err = repos.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	clones, err := r.List(t.Context())
	if err != nil || len(clones) != 1 || clones[0].Settings.Automatic == nil || *clones[0].Settings.Automatic {
		t.Fatalf("%+v %v", clones, err)
	}
	for _, flag := range []string{"--automatic", "--clear-automatic", "--model=custom"} {
		if _, _, err := run(t, "internal", "repos", "add", path, flag); err == nil {
			t.Errorf("accepted unused %s", flag)
		}
	}
}
