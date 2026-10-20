package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	if !strings.Contains(stderr, "Local commands:") {
		t.Errorf("stderr should carry usage, got %q", stderr)
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	_, stderr, err := run(t, "frobnicate")
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if !strings.Contains(stderr, "Local commands:") {
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

// The hosted service commands sit in a section of their own, and the hook
// dispatchers agents and Git call stay out of the listing.
func TestHelpGroupsHostedCommands(t *testing.T) {
	stdout, _, err := run(t, "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	local := strings.Index(stdout, "Local commands:")
	hosted := strings.Index(stdout, "Hosted service")
	if local < 0 || hosted < local {
		t.Fatalf("help has no local section followed by a hosted one:\n%s", stdout)
	}
	section := stdout[hosted:]
	if end := strings.Index(section, "\n\n"); end >= 0 {
		section = section[:end]
	}
	for _, name := range []string{"login", "logout", "whoami", "setup", "upload"} {
		if !strings.Contains(section, "\n  "+name+" ") {
			t.Errorf("the hosted section does not list %s:\n%s", name, section)
		}
		if strings.Contains(stdout[local:hosted], "\n  "+name+" ") {
			t.Errorf("%s is listed as a local command", name)
		}
	}
	if strings.Contains(stdout, "\n  hooks ") {
		t.Errorf("help lists the hidden hooks command:\n%s", stdout)
	}
}

// A delegated agent hook's exit status is the process's, and nothing about it
// is printed: the agent reads the hook's output.
func TestAgentHookExitStatusPassesThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for Entire is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "entire"), []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, err := run(t, "hooks", "agent", "claude-code", "stop")
	if ExitCode(err) != 7 || !Quiet(err) {
		t.Fatalf("err = %v: exit code %d, quiet %v", err, ExitCode(err), Quiet(err))
	}
	if Quiet(errors.New("other")) {
		t.Error("an ordinary failure is quiet")
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
