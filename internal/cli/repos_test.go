package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestReposCLIPickAndUnpick(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		e := Run(append([]string{"internal", "repos"}, args...), nil, &out, &errOut)
		return out.String(), e
	}
	if out, e := run("pick", "Owner/Repo"); e != nil || out != "owner/repo\n" {
		t.Fatalf("%q %v", out, e)
	}
	if _, e := run("pick", "a/b/c"); e == nil {
		t.Fatal("invalid name accepted")
	}
	raw, e := run("repositories", "--json")
	if e != nil || !strings.HasPrefix(raw, `[{"repository":"owner/repo"`) || !strings.Contains(raw, `"clones":[]`) {
		t.Fatalf("%s %v", raw, e)
	}
	var list []repos.Repository
	if e = json.Unmarshal([]byte(raw), &list); e != nil || len(list) != 1 {
		t.Fatalf("%s %v", raw, e)
	}
	if out, e := run("repositories"); e != nil || !strings.HasPrefix(out, "owner/repo\t0\t") {
		t.Fatalf("%q %v", out, e)
	}
	// Clones are unchanged by picking.
	if out, e := run("list", "--json"); e != nil || out != "[]\n" {
		t.Fatalf("%q %v", out, e)
	}
	if _, e = run("unpick", "owner/repo"); e != nil {
		t.Fatal(e)
	}
	if _, e = run("unpick", "owner/repo"); e == nil {
		t.Fatal("unpicked twice")
	}
	if out, e := run("repositories", "--json"); e != nil || out != "[]\n" {
		t.Fatalf("%q %v", out, e)
	}
}

func TestReposCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = p
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s %v", out, e)
		}
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		e := Run(args, nil, &out, &errOut)
		return out.String(), e
	}
	// The harnesses and models are global: there is no repository override.
	if _, e := run("internal", "repos", "add", p, "--model=custom"); e == nil {
		t.Fatal("a model flag was accepted")
	}
	raw, e := run("internal", "repos", "list", "--json")
	if e != nil || raw != "[]\n" {
		t.Fatalf("invalid flags registered clone %s %v", raw, e)
	}
	if _, e = run("internal", "repos", "add", p, "--automatic"); e != nil {
		t.Fatal(e)
	}
	if _, e = run("internal", "repos", "add", p); e != nil {
		t.Fatal(e)
	}
	raw, e = run("internal", "repos", "list", "--json")
	if e != nil {
		t.Fatal(e)
	}
	var list []repos.Clone
	if e = json.Unmarshal([]byte(raw), &list); e != nil {
		t.Fatal(e)
	}
	if len(list) != 1 || (list[0].Settings.Automatic == nil || !*list[0].Settings.Automatic) {
		t.Fatalf("settings lost %s", raw)
	}
	// There is no daily cap to set.
	if _, e = run("internal", "repos", "add", p, "--daily-cap=1"); e == nil {
		t.Fatal("a daily cap flag was accepted")
	}
	automatic := func() *bool {
		t.Helper()
		raw, e := run("internal", "repos", "list", "--json")
		var list []repos.Clone
		if e != nil || json.Unmarshal([]byte(raw), &list) != nil || len(list) != 1 {
			t.Fatalf("%s %v", raw, e)
		}
		return list[0].Settings.Automatic
	}
	if _, e = run("internal", "repos", "add", p, "--automatic=false"); e != nil {
		t.Fatal(e)
	}
	if v := automatic(); v == nil || *v {
		t.Fatalf("explicit off not stored: %v", v)
	}
	if _, e = run("internal", "repos", "add", p, "--automatic", "--clear-automatic"); e == nil {
		t.Fatal("conflicting automatic flags accepted")
	}
	if _, e = run("internal", "repos", "add", p, "--clear-automatic"); e != nil {
		t.Fatal(e)
	}
	if v := automatic(); v != nil {
		t.Fatalf("automatic not cleared: %v", *v)
	}
	if _, e = run("internal", "repos", "remove", filepath.Clean(p)); e != nil {
		t.Fatal(e)
	}
	raw, e = run("internal", "repos", "list", "--json")
	if e != nil || raw != "[]\n" {
		t.Fatalf("%s %v", raw, e)
	}
}
