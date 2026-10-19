package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = Run(args, strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestNoCommandPrintsUsageAndFails(t *testing.T) {
	_, stderr, err := run(t)
	if err == nil {
		t.Fatal("expected an error without a command")
	}
	if !strings.Contains(stderr, "Available Commands") {
		t.Errorf("stderr should carry usage, got %q", stderr)
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	_, stderr, err := run(t, "frobnicate")
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if !strings.Contains(stderr, "Available Commands") {
		t.Errorf("stderr should carry usage, got %q", stderr)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	stdout, _, err := run(t, "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, name := range []string{"generate", "bundle", "view", "validate", "refresh", "sessions"} {
		if !strings.Contains(stdout, "\n  "+name) {
			t.Errorf("help does not list %s:\n%s", name, stdout)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	stdout, _, err := run(t, "--version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(stdout, "baum version ") {
		t.Errorf("stdout = %q", stdout)
	}
}
