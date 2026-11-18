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
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no dataset":      {nil, "--dataset"},
		"not a dataset":   {[]string{"--dataset", t.TempDir()}, "not a replay dataset"},
		"resume no name":  {[]string{"--dataset", dir, "--resume"}, "--resume needs --name"},
		"override syntax": {[]string{"--dataset", dir, "-prompt-override", "review-staged-outline"}, "<id>=<file>"},
		"seed":            {[]string{"--dataset", dir, "--shuffle-seed", "x"}, "--shuffle-seed"},
		"empty selection": {[]string{"--dataset", dir, "--dry-run", "--case", "a", "--split", "calibration"}, "cannot be combined"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), tc.args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHelpIsNotAFailure(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"--help"}, io.Discard, &out); !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "-prompt-override") {
		t.Errorf("%v\n%s", err, out.String())
	}
}
