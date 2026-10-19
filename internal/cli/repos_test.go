package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestReposCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	if _, e := run("internal", "repos", "add", p, "--backend=invalid"); e == nil {
		t.Fatal("invalid backend accepted")
	}
	raw, e := run("internal", "repos", "list", "--json")
	if e != nil || raw != "[]\n" {
		t.Fatalf("invalid flags registered clone %s %v", raw, e)
	}
	if _, e = run("internal", "repos", "add", p, "--automatic", "--daily-cap=2.5", "--model=custom"); e != nil {
		t.Fatal(e)
	}
	if _, e = run("internal", "repos", "add", p, "--backend=codex"); e != nil {
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
	if len(list) != 1 || (list[0].Settings.Automatic == nil || !*list[0].Settings.Automatic) || list[0].Settings.DailyCap == nil || *list[0].Settings.DailyCap != 2.5 || list[0].Settings.Model == nil {
		t.Fatalf("settings lost %s", raw)
	}
	if _, e = run("internal", "repos", "add", p, "--daily-cap=1", "--clear-daily-cap"); e == nil {
		t.Fatal("conflicting cap flags accepted")
	}
	if _, e = run("internal", "repos", "add", p, "--clear-daily-cap"); e != nil {
		t.Fatal(e)
	}
	raw, e = run("internal", "repos", "list", "--json")
	if e != nil {
		t.Fatal(e)
	}
	list = nil
	if e = json.Unmarshal([]byte(raw), &list); e != nil {
		t.Fatal(e)
	}
	if len(list) != 1 || list[0].Settings.DailyCap != nil || (list[0].Settings.Automatic == nil || !*list[0].Settings.Automatic) {
		t.Fatalf("cap not cleared or other settings lost %s", raw)
	}
	if _, e = run("internal", "repos", "add", p, "--backend=", "--model="); e != nil {
		t.Fatal(e)
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
