//go:build !windows

package baumcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const foreignPrePushHook = "#!/bin/sh\necho project pre-push\n"

func TestBootstrapInstallsEveryGitHookInAFreshClone(t *testing.T) {
	repo := initHookTestRepo(t)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	var log bytes.Buffer

	if err := runHooksBootstrap(t.Context(), repo, &log); err != nil {
		t.Fatalf("bootstrap: %v: %s", err, log.String())
	}
	for _, name := range entireGitHookNames() {
		path := filepath.Join(repo, ".git", "hooks", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s hook: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s hook is not executable: %v", name, info.Mode())
		}
		if got := readHookTestFile(t, path); got != gitHookDispatcher(name) {
			t.Errorf("%s hook = %q", name, got)
		}
	}
	marker := readHookTestFile(t, filepath.Join(repo, ".git", "baum", "bootstrap-ok"))
	if strings.TrimSpace(marker) != hooksBootstrapFingerprint() {
		t.Errorf("bootstrap marker = %q", marker)
	}
}

func TestBootstrapChainsAForeignGitHook(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	writeHookTestFile(t, hook, foreignPrePushHook)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")

	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); got != gitHookDispatcher("pre-push") {
		t.Errorf("pre-push hook = %q", got)
	}
	if got := readHookTestFile(t, hook+".pre-entire"); got != foreignPrePushHook {
		t.Errorf("chained hook = %q", got)
	}
}

// TestBootstrapKeepsAForeignHookItCannotChain covers the clone where something
// replaced the dispatcher after a first bootstrap: the backup slot the
// dispatcher chains is taken, so overwriting the new hook would destroy it.
func TestBootstrapKeepsAForeignHookItCannotChain(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	writeHookTestFile(t, hook, foreignPrePushHook)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	replacement := "#!/bin/sh\necho another tool\n"
	writeHookTestFile(t, hook, replacement)
	if err := os.Remove(filepath.Join(repo, ".git", "baum", "bootstrap-ok")); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	if err := runHooksBootstrap(t.Context(), repo, &log); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); got != replacement {
		t.Errorf("pre-push hook = %q", got)
	}
	if got := readHookTestFile(t, hook+".pre-entire"); got != foreignPrePushHook {
		t.Errorf("chained hook = %q", got)
	}
	if !strings.Contains(log.String(), "pre-push") {
		t.Errorf("bootstrap log = %q", log.String())
	}
	// No marker, so the next session start reports it again rather than
	// treating the clone as bootstrapped.
	if _, err := os.Stat(filepath.Join(repo, ".git", "baum", "bootstrap-ok")); !os.IsNotExist(err) {
		t.Errorf("bootstrap marker was recorded: %v", err)
	}
	// The hooks it can own are still installed.
	if got := readHookTestFile(t, filepath.Join(repo, ".git", "hooks", "commit-msg")); got != gitHookDispatcher("commit-msg") {
		t.Errorf("commit-msg hook = %q", got)
	}
}

func TestBootstrapTakesOverEntireHooksWithoutBackingThemUp(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "commit-msg")
	writeHookTestFile(t, hook, entireCommitMsgHook)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")

	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); got != gitHookDispatcher("commit-msg") {
		t.Errorf("commit-msg hook = %q", got)
	}
	if _, err := os.Stat(hook + ".pre-entire"); !os.IsNotExist(err) {
		t.Errorf("Entire's own hook was backed up: %v", err)
	}
}

func TestBootstrapLeavesASymlinkedHookAlone(t *testing.T) {
	repo := initHookTestRepo(t)
	target := filepath.Join(repo, "shared-hook")
	writeHookTestFile(t, target, foreignPrePushHook)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	if err := os.Symlink(target, hook); err != nil {
		t.Fatal(err)
	}
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")

	var log bytes.Buffer
	if err := runHooksBootstrap(t.Context(), repo, &log); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, target); got != foreignPrePushHook {
		t.Errorf("symlink target was changed: %q", got)
	}
	if !strings.Contains(log.String(), "pre-push") {
		t.Errorf("log does not name the symlinked hook: %q", log.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "baum", "bootstrap-ok")); !os.IsNotExist(err) {
		t.Errorf("bootstrap marker written although pre-push was not taken over: %v", err)
	}
}

func TestBootstrapSkipsTheWorkWhenTheCloneIsAlreadyHealthy(t *testing.T) {
	repo := initHookTestRepo(t)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, ".git", "hooks", "post-commit")
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	// Every session start runs this, so a healthy clone must not pay for the
	// hook inspection at all.
	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatalf("healthy bootstrap: %v", err)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Errorf("healthy bootstrap inspected the hooks: %v", err)
	}
}

func TestBootstrapRepairsHooksWhenTheMarkerIsStale(t *testing.T) {
	repo := initHookTestRepo(t)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, ".git", "baum", "bootstrap-ok")
	writeHookTestFile(t, marker, "installed-by-an-older-baum\n")
	hook := filepath.Join(repo, ".git", "hooks", "post-commit")
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); got != gitHookDispatcher("post-commit") {
		t.Errorf("post-commit hook = %q", got)
	}
	if got := readHookTestFile(t, marker); strings.TrimSpace(got) != hooksBootstrapFingerprint() {
		t.Errorf("bootstrap marker = %q", got)
	}
}

func TestBootstrapReinstallsWhenEntireDisappears(t *testing.T) {
	repo := initHookTestRepo(t)
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	if err := runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	withoutEntire(t)
	// The reinstall attempt stops before the download, which keeps the test off
	// the network while still proving the fast path gave way to it.
	original := entireRuntimeGOOS
	t.Cleanup(func() { entireRuntimeGOOS = original })
	entireRuntimeGOOS = "plan9"
	var log bytes.Buffer

	bootstrapHooks(t.Context(), repo, &log)
	if !strings.Contains(log.String(), "hook bootstrap skipped: automatic Entire installation is unsupported") {
		t.Errorf("log = %q", log.String())
	}
}

func TestBootstrapLeavesACustomHooksPathAlone(t *testing.T) {
	repo := initHookTestRepo(t)
	runGitTest(t, repo, "config", "core.hooksPath", ".githooks")
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")
	var log bytes.Buffer

	if err := runHooksBootstrap(t.Context(), repo, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "core.hooksPath is set") {
		t.Errorf("log = %q", log.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".githooks")); !os.IsNotExist(err) {
		t.Errorf("custom hooks directory was created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "baum", "bootstrap-ok")); !os.IsNotExist(err) {
		t.Errorf("skipped bootstrap was recorded as complete: %v", err)
	}
}

func TestBootstrapCommandReportsProblemsWithoutFailing(t *testing.T) {
	withoutEntire(t)
	t.Chdir(t.TempDir())

	stdout, _, err := runCLI(t, "hooks", "bootstrap")
	if err != nil {
		t.Fatalf("bootstrap command: %v", err)
	}
	if !strings.Contains(stdout, "hook bootstrap skipped") {
		t.Errorf("stdout = %q", stdout)
	}
}
