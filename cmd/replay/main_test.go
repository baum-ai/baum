package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagsAreChecked(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"cases", "checkouts.git"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BAUM_REPLAY_DATASET", "")
	empty := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(empty, []byte("# nothing\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no dataset":        {nil, "--dataset"},
		"not a dataset":     {[]string{"--dataset", t.TempDir()}, "not a replay dataset"},
		"resume no name":    {[]string{"--dataset", dir, "--resume"}, "--resume needs --name"},
		"override syntax":   {[]string{"--dataset", dir, "-prompt-override", "review-staged-outline"}, "<id>=<file>"},
		"seed":              {[]string{"--dataset", dir, "--shuffle-seed", "x"}, "--shuffle-seed"},
		"empty selection":   {[]string{"--dataset", dir, "--dry-run", "--case", "a", "--split", "calibration"}, "cannot be combined"},
		"positional value":  {[]string{"--dataset", dir, "--with-images", "false", "--limit", "3", "--dry-run"}, `unexpected argument "false"`},
		"unknown generator": {[]string{"--dataset", dir, "--generator", "layouts"}, "unknown generator"},
		"generator option":  {[]string{"--dataset", dir, "--generator-option", "x"}, "KEY=VALUE"},
		"review option":     {[]string{"--dataset", dir, "--generator-option", "x=y"}, "takes no --generator-option"},
		"tri-state":         {[]string{"--dataset", dir, "--with-images=maybe"}, "use true or false"},
		"case file":         {[]string{"--dataset", dir, "--case-file", filepath.Join(dir, "missing.txt")}, "missing.txt"},
		"empty case file":   {[]string{"--dataset", dir, "--case-file", empty}, "names no case"},
		"detector":          {[]string{"--dataset", dir, "--dry-run", "--detector", "tables"}, "no index"},
		"compose twice":     {[]string{"--dataset", dir, "--compose-from-store", "--compose-only"}, "not both"},
		"from-run alone":    {[]string{"--dataset", dir, "--from-run", dir}, "goes with --compose-only"},
		"compose-only run":  {[]string{"--dataset", dir, "--compose-only"}, "--from-run"},
		"compose chain":     {[]string{"--dataset", dir, "--compose-from-store", "--chain"}, "does not take --chain"},
		"compose override":  {[]string{"--dataset", dir, "--compose-only", "--from-run", dir, "--prompt-override", "review-staged-outline=" + empty}, "not the composition prompt"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), tc.args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPositionalArgumentsAreRefused(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"cases", "checkouts.git"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	err := run(context.Background(), []string{"--dataset", dir, "--with-images", "false", "--limit", "3", "--dry-run"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--with-images=false") || !strings.Contains(err.Error(), "ignored") {
		t.Errorf("error %v", err)
	}
	if err := run(context.Background(), []string{"--dataset", dir, "stray"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), `"stray"`) {
		t.Errorf("error %v", err)
	}
	if err := run(context.Background(), []string{"metrics", dir, "--compare", dir, "extra"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), `"extra"`) {
		t.Errorf("error %v", err)
	}
}

func TestHelpIsNotAFailure(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"--help"}, io.Discard, &out); !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "-prompt-override") {
		t.Errorf("%v\n%s", err, out.String())
	}
}

func TestCaseFileLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(path, []byte("# the first set\nc1\n  c2  # trailing comment\n\nc3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids, err := readCaseFile(path)
	if err != nil || strings.Join(ids, ",") != "c1,c2,c3" {
		t.Errorf("%v %v", ids, err)
	}
}

func TestTriStateFlag(t *testing.T) {
	for args, want := range map[string]string{"": "", "-x": "true", "-x=true": "true", "-x=false": "false"} {
		var f triState
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.Var(&f, "x", "")
		var in []string
		if args != "" {
			in = []string{args}
		}
		if err := fs.Parse(in); err != nil || f.String() != want {
			t.Errorf("%q: %q %v", args, f.String(), err)
		}
	}
}

func TestMetricsCommand(t *testing.T) {
	runDir := t.TempDir()
	manifest := `{"run":"r","generator":"review","case_facts":{"c1":{"size":"s"}}}`
	outcome := `{"case_id":"c1","status":"ok","wall_seconds":3,"calls":2,"tokens":10,"cost_usd":0.5,"repairs":0,"document_valid":true}`
	other := t.TempDir()
	for _, dir := range []string{runDir, other} {
		if err := os.MkdirAll(filepath.Join(dir, "cases", "c1"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"manifest.json": manifest, "cases/c1/outcome.json": outcome} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The run directory may come after the flags.
	var out strings.Builder
	if err := run(context.Background(), []string{"metrics", "--compare", other, runDir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run r, generator review: 1 case(s)", "cost_usd", "1 case(s) in both runs", "wrote " + runDir + "/metrics.json", "wrote " + runDir + "/compare.json"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, name := range []string{"metrics.json", "compare.json"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Error(err)
		}
	}
	for name, args := range map[string][]string{"no directory": {"metrics"}, "two directories": {"metrics", runDir, other}, "not a run": {"metrics", t.TempDir()}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestScreenSubcommandsCheckTheirArguments(t *testing.T) {
	notRun := t.TempDir()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"sheet without a run":    {[]string{"sheet"}, "usage: replay sheet"},
		"sheet of no run":        {[]string{"sheet", notRun}, "is not a replay run"},
		"sheet with two runs":    {[]string{"sheet", notRun, notRun}, "unexpected argument"},
		"judge of no run":        {[]string{"judge", notRun, "--backend", "codex"}, "is not a replay run"},
		"judge agree one report": {[]string{"judge", "agree", "a.json"}, "usage: replay judge agree"},
		"labels without a verb":  {[]string{"labels"}, "usage: replay labels"},
		"labels make no out":     {[]string{"labels", "make", notRun}, "--out"},
		"labels agree no report": {[]string{"labels", "agree", notRun}, "usage: replay labels agree"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), tc.args, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
}
