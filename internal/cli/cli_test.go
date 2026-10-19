package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
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

// listedCommands returns the command names a help screen lists, one per
// "  name  description" line.
func listedCommands(help string) []string {
	var names []string
	for _, line := range strings.Split(help, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 1 && fields[0] != "baum" && !strings.HasPrefix(fields[0], "-") {
			names = append(names, fields[0])
		}
	}
	return names
}

// The top-level help lists what users run, and nothing of the engine.
func TestHelpListsOnlyUserCommands(t *testing.T) {
	stdout, _, err := run(t, "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	want := []string{"completion", "daemon", "help", "hooks", "login", "logout", "setup", "upload", "whoami"}
	got := listedCommands(stdout)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("top-level help lists %v, want %v:\n%s", got, want, stdout)
	}
	if strings.Contains(stdout, "internal") {
		t.Errorf("top-level help mentions the hidden internal group:\n%s", stdout)
	}
}

func TestInternalHelpListsEngineCommands(t *testing.T) {
	stdout, _, err := run(t, "internal", "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	want := []string{"bundle", "doctor", "generate", "refresh", "repos", "sessions", "validate", "view"}
	got := listedCommands(stdout)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("internal help lists %v, want %v:\n%s", got, want, stdout)
	}
}

// Every engine command answers under "baum internal" with its flags, and the
// old top-level spellings are gone.
func TestInternalCommandsDispatch(t *testing.T) {
	for name, flag := range map[string]string{
		"generate": "--backend", "bundle": "--pr", "view": "--sample", "validate": "--bundle",
		"refresh": "--pr", "sessions": "--json", "repos": "--help", "doctor": "--offline",
	} {
		stdout, _, err := run(t, "internal", name, "--help")
		if err != nil || !strings.Contains(stdout, flag) {
			t.Errorf("internal %s --help: %v\n%s", name, err, stdout)
		}
		if _, _, err := run(t, name, "--help"); err == nil {
			t.Errorf("top-level %s still answers", name)
		}
	}
	if _, _, err := run(t, "ui"); err == nil {
		t.Error("top-level ui still answers")
	}
}

// The browser launcher command was removed with no alias; nothing may send
// users to it.
func TestNoReferenceToRemovedUICommand(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []byte("baum" + " ui")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == "dist" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".ts", ".tsx", ".sh", ".yml", ".json":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, banned) {
			t.Errorf("%s refers to the removed %s command", path, banned)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	for _, name := range []string{"login", "logout", "whoami", "setup", "upload", "hooks"} {
		if !strings.Contains(section, "\n  "+name+" ") {
			t.Errorf("the hosted section does not list %s:\n%s", name, section)
		}
		if strings.Contains(stdout[local:hosted], "\n  "+name+" ") {
			t.Errorf("%s is listed as a local command", name)
		}
	}
	// Users install hooks; agents and Git run them.
	stdout, _, err = run(t, "hooks", "--help")
	if err != nil {
		t.Fatalf("hooks help: %v", err)
	}
	for _, name := range []string{"install", "uninstall", "bootstrap"} {
		if !strings.Contains(stdout, "\n  "+name+" ") {
			t.Errorf("hooks help does not list %s:\n%s", name, stdout)
		}
	}
	for _, name := range []string{"agent", "git"} {
		if strings.Contains(stdout, "\n  "+name+" ") {
			t.Errorf("hooks help lists the internal %s command:\n%s", name, stdout)
		}
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
