//go:build !windows

package githooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubBaum puts a baum on PATH that logs each hook it is run for, and returns
// the log's path.
func stubBaum(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "baum.log")
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\ncase \"$*\" in *--check-dispatcher*) exit 0;; esac\necho \"$*\" >> '"+log+"'\ncat > /dev/null\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "hooks git ")
}

// commitWithHooks makes a real commit, which must finish: a dispatcher that
// loops would hang it.
func commitWithHooks(t *testing.T, repo string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "work")
	cmd.Env = append(append(os.Environ(), isolated...), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
}

// A hook manager moves the hook it finds aside and calls it from its own: baum
// must not chain that hook, which leads back to a dispatcher of baum's.
func TestEnsureRefusesToChainAHookThatLeadsBackToADispatcher(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	log := stubBaum(t)
	ensure(t, repo)
	if err := os.Rename(filepath.Join(hooks, "post-commit"), filepath.Join(hooks, "post-commit.legacy")); err != nil {
		t.Fatal(err)
	}
	manager := "#!/bin/sh\n\"$(dirname \"$0\")/post-commit.legacy\" \"$@\"\n"
	writeExecutable(t, filepath.Join(hooks, "post-commit"), manager)
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Unclaimed || read(t, filepath.Join(hooks, "post-commit")) != manager {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Lstat(filepath.Join(hooks, "post-commit.pre-baum")); err == nil {
		t.Fatal("the manager's hook was chained")
	}
	commitWithHooks(t, repo)
	if n := calls(t, log); n != 1 {
		t.Fatalf("baum ran %d times", n)
	}
}

// Whatever leads back to the dispatcher while it runs ends there: baum runs once.
func TestDispatcherEndsAReentrantLoop(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	log := stubBaum(t)
	writeExecutable(t, filepath.Join(hooks, "post-commit"), "#!/bin/sh\n\"$(dirname \"$0\")/post-commit\" \"$@\"\n")
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Chained {
		t.Fatalf("report = %+v", report)
	}
	commitWithHooks(t, repo)
	if n := calls(t, log); n != 1 {
		t.Fatalf("baum ran %d times", n)
	}
}

// Entire installed after baum and moved baum's dispatcher aside: its hook is left
// alone, and the dispatcher behind it, which holds a marker, is not run twice.
func TestEnsureLeavesEntireManagedHooksAlone(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	log := stubBaum(t)
	writeExecutable(t, filepath.Join(hooks, "post-commit.pre-entire"), Dispatcher("post-commit", ""))
	entire := "#!/bin/sh\n# Entire CLI hooks\n# entire hooks git post-commit\n\"$(dirname \"$0\")/post-commit.pre-entire\" \"$@\"\n"
	writeExecutable(t, filepath.Join(hooks, "post-commit"), entire)
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("report = %+v", report)
	}
	if read(t, filepath.Join(hooks, "post-commit")) != entire {
		t.Fatal("Entire's hook was rewritten")
	}
	commitWithHooks(t, repo)
	if n := calls(t, log); n != 1 {
		t.Fatalf("baum ran %d times", n)
	}
}

func TestDispatcherNeverRunsABackupHoldingADispatcher(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	ensure(t, repo)
	flag := filepath.Join(t.TempDir(), "ran")
	for _, marker := range []string{Marker, legacyMarker} {
		writeExecutable(t, filepath.Join(hooks, "post-commit.pre-baum"), "#!/bin/sh\n"+marker+"\ntouch '"+flag+"'\n")
		if out, err := exec.Command(filepath.Join(hooks, "post-commit")).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if _, err := os.Stat(flag); err == nil {
			t.Fatalf("a backup with %q ran", marker)
		}
	}
	// The hook itself, reached by another name, is not run either.
	if err := os.Remove(filepath.Join(hooks, "post-commit.pre-baum")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(hooks, "post-commit"), filepath.Join(hooks, "post-commit.pre-baum")); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(filepath.Join(hooks, "post-commit")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestEnsureLeavesHooksThatActOnTheirOwnName(t *testing.T) {
	for name, contents := range map[string]string{
		"husky v4":   "#!/bin/sh\nhookName=$(basename \"$0\")\nrun $hookName\n",
		"unquoted":   "#!/bin/sh\nn=$(basename $0)\n",
		"husky":      "#!/bin/sh\n. \"$(dirname \"$0\")/_/husky.sh\"\n",
		"overcommit": "#!/bin/sh\n# OVERCOMMIT\novercommit-hook\n",
		"ruby":       "#!/usr/bin/env ruby\nHOOK = File.basename($0)\n",
		"strip":      "#!/bin/sh\nname=${0##*/}\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo := initRepo(t)
			path := filepath.Join(repo, ".git", "hooks", "post-commit")
			writeExecutable(t, path, contents)
			report := ensure(t, repo)
			if outcomes(report)["post-commit"] != Unclaimed || read(t, path) != contents {
				t.Fatalf("report = %+v", report)
			}
			if _, err := os.Lstat(path + ".pre-baum"); err == nil {
				t.Fatal("hook was moved")
			}
		})
	}
}

// When the input cannot be buffered, baum is not run and the chained hooks read
// the original input.
func TestChainedHooksReadTheOriginalInputWhenBufferingFails(t *testing.T) {
	for _, hook := range []string{"pre-push", "post-rewrite"} {
		t.Run(hook, func(t *testing.T) {
			repo := initRepo(t)
			hooks := filepath.Join(repo, ".git", "hooks")
			log := stubBaum(t)
			got := filepath.Join(t.TempDir(), "stdin")
			writeExecutable(t, filepath.Join(hooks, hook), "#!/bin/sh\ncat > '"+got+"'\n")
			ensure(t, repo)
			cmd := exec.Command(filepath.Join(hooks, hook), "origin", "url")
			cmd.Env = append(os.Environ(), "TMPDIR=/nonexistent")
			cmd.Stdin = strings.NewReader("refs/heads/main abc\n")
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("%v: %q", err, out)
			}
			if read(t, got) != "refs/heads/main abc\n" {
				t.Errorf("chained hook read %q", read(t, got))
			}
			if n := calls(t, log); n != 0 {
				t.Errorf("baum ran %d times without its input", n)
			}
		})
	}
}

func TestOptOutRestoresTheHooksTheClonesHad(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	original := "#!/bin/sh\necho mine\n"
	writeExecutable(t, filepath.Join(hooks, "pre-push"), original)
	ensure(t, repo)
	if _, err := os.Lstat(filepath.Join(hooks, "pre-push.pre-baum")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "baum.hooks", "false")
	report := ensure(t, repo)
	want := map[string]Outcome{"pre-push": Restored, "post-commit": Removed, "post-rewrite": Removed}
	for hook, outcome := range want {
		if outcomes(report)[hook] != outcome {
			t.Errorf("%s = %v", hook, outcomes(report)[hook])
		}
	}
	if read(t, filepath.Join(hooks, "pre-push")) != original {
		t.Error("the clone's hook did not come back")
	}
	if info, _ := os.Stat(filepath.Join(hooks, "pre-push")); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode())
	}
	for _, name := range []string{"pre-push.pre-baum", "post-commit", "post-rewrite"} {
		if _, err := os.Lstat(filepath.Join(hooks, name)); err == nil {
			t.Errorf("%s is still there", name)
		}
	}
	if again := ensure(t, repo); again.Changed() {
		t.Errorf("a second run changed %+v", again)
	}
	// Opting back in installs them again.
	git(t, repo, "config", "baum.hooks", "true")
	if back := ensure(t, repo); outcomes(back)["pre-push"] != Chained {
		t.Errorf("opted back in: %+v", back)
	}
}

func TestRestoreLeavesWhatItCannotRestore(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "pre-push"), "#!/bin/sh\necho mine\n")
	ensure(t, repo)
	// The backup slot now holds a dispatcher, not the hook it replaced.
	writeExecutable(t, filepath.Join(hooks, "pre-push.pre-baum"), "#!/bin/sh\n"+Marker+"\n")
	// A backup of the old kind still runs through the other dispatcher.
	writeExecutable(t, filepath.Join(hooks, "post-commit.pre-entire"), "#!/bin/sh\necho older\n")
	report, err := Restore(t.Context(), repo, Options{Env: isolated})
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"pre-push", "post-commit"} {
		if outcomes(report)[hook] != Unclaimed {
			t.Errorf("%s = %v", hook, outcomes(report)[hook])
		}
	}
	if read(t, filepath.Join(hooks, "pre-push")) != Dispatcher("pre-push", "") {
		t.Error("the dispatcher was removed")
	}
}

func TestRepairKeepsAUserChmod(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	ensure(t, repo)
	path := filepath.Join(hooks, "post-commit")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Current {
		t.Fatalf("report = %+v", report)
	}
	writeExecutable(t, path, "#!/bin/sh\n"+legacyMarker+"\nold\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Repaired {
		t.Fatalf("report = %+v", report)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", info.Mode())
	}
}

// Without baum on PATH the dispatcher uses the binary it was installed with, and
// a different binary makes it outdated.
func TestDispatcherFallsBackToTheInstalledBinary(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	dir := filepath.Join(t.TempDir(), "it's here")
	log := filepath.Join(t.TempDir(), "log")
	writeExecutable(t, filepath.Join(dir, "baum"), "#!/bin/sh\ncase \"$*\" in *--check-dispatcher*) exit 0;; esac\necho \"$*\" >> '"+log+"'\n")
	opts := Options{Env: isolated, Baum: filepath.Join(dir, "baum")}
	if _, err := EnsureWith(t.Context(), repo, opts); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	if out, err := exec.Command(filepath.Join(hooks, "post-commit")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if calls(t, log) != 1 {
		t.Fatalf("the installed binary was not run: %q", read(t, log))
	}
	// The binary is gone: a dispatcher naming another one is repaired.
	if err := os.Remove(filepath.Join(dir, "baum")); err != nil {
		t.Fatal(err)
	}
	opts.Baum = "/elsewhere/baum"
	report, err := EnsureWith(t.Context(), repo, opts)
	if err != nil || outcomes(report)["post-commit"] != Repaired {
		t.Fatalf("a changed binary did not repair: %+v %v", report, err)
	}
}
