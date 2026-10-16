package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/doctor"
)

// doctorMachine isolates the environment and makes dir the whole of PATH.
func doctorMachine(t *testing.T) (bin string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake executables are shell scripts")
	}
	home := t.TempDir()
	bin = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("PATH", bin)
	for _, name := range []string{"GH_PATH", "GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "BAUM_BACKEND", "BAUM_MODEL", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "")
	}
	return bin
}

func fakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorExitsNonZeroOnAProblemAndPrintsTheFix(t *testing.T) {
	doctorMachine(t) // no git, claude or codex
	stdout, _, err := run(t, "doctor", "--offline")
	if err == nil || ExitCode(err) != ExitDiagnostics {
		t.Fatalf("err = %v, exit %d, want exit %d", err, ExitCode(err), ExitDiagnostics)
	}
	for _, want := range []string{"[problem]", "[warning]", "fix: ", "backend_paths.claude", "git", "price list"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorExitsZeroWithWarningsOnly(t *testing.T) {
	bin := doctorMachine(t)
	fakeTool(t, bin, "git", `echo "git version 2.43.0"`)
	fakeTool(t, bin, "claude", `echo "2.1.7 (Claude Code)"`)
	t.Setenv("GITHUB_TOKEN", "ghp_PLANTEDclitoken0000000000000000000000")
	stdout, _, err := run(t, "doctor", "--offline")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "PLANTED") || !strings.Contains(stdout, "environment variable GITHUB_TOKEN") {
		t.Errorf("output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "warning: ") {
		t.Errorf("no summary line:\n%s", stdout)
	}
}

func TestDoctorJSONShape(t *testing.T) {
	bin := doctorMachine(t)
	fakeTool(t, bin, "git", `echo "git version 2.43.0"`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-PLANTEDclikey000000000000000000000")
	stdout, _, err := run(t, "doctor", "--json", "--offline")
	if ExitCode(err) != ExitDiagnostics {
		t.Fatalf("err = %v (no backend is installed, so there is a problem)", err)
	}
	if strings.Contains(stdout, "PLANTED") {
		t.Errorf("the key leaked:\n%s", stdout)
	}
	var report doctor.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not a report: %v\n%s", err, stdout)
	}
	if report.Status != doctor.StatusProblem || len(report.Checks) == 0 {
		t.Fatalf("report = %+v", report)
	}
	var raw struct {
		Status string
		Checks []map[string]any
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatal(err)
	}
	for _, c := range raw.Checks {
		for _, key := range []string{"id", "label", "status", "detail"} {
			if s, _ := c[key].(string); s == "" {
				t.Errorf("check %v lacks %q", c, key)
			}
		}
	}
	byID := map[string]doctor.Check{}
	for _, c := range report.Checks {
		byID[c.ID] = c
	}
	if git := byID["git"]; git.Status != doctor.StatusOK || git.Version != "2.43.0" || git.Path != filepath.Join(bin, "git") {
		t.Errorf("git = %+v", git)
	}
	if auto := byID["backend-auto"]; auto.Status != doctor.StatusProblem || auto.Fix == "" {
		t.Errorf("backend-auto = %+v", auto)
	}
}

func TestDoctorFetchesPricesUnlessOffline(t *testing.T) {
	doctorMachine(t)
	old := doctorOptions
	t.Cleanup(func() { doctorOptions = old })
	var fetched []bool
	doctorOptions = func(fetch bool) doctor.Options {
		opts := old(fetch)
		opts.RefreshPrices = func(context.Context) error { fetched = append(fetched, true); return nil }
		return opts
	}
	_, _, _ = run(t, "doctor")
	_, _, _ = run(t, "doctor", "--offline")
	if len(fetched) != 1 {
		t.Errorf("price refresh ran %d times over a default and an --offline run, want 1", len(fetched))
	}
}

func TestDoctorRejectsArguments(t *testing.T) {
	doctorMachine(t)
	if _, _, err := run(t, "doctor", "extra"); err == nil {
		t.Error("doctor accepted an argument")
	}
}
