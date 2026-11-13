//go:build !windows

package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// legacyScript is what pre-release baum put in place of Entire's hook.
func legacyScript(hook string) string {
	return "#!/bin/sh\n" + legacyMarker + "\n" + entireMarker + "\nif command -v entire >/dev/null 2>&1; then\n  entire hooks git " + hook + " \"$1\"\nfi\n"
}

// plainLegacyScript is a dispatcher of an earlier version that runs no hook of
// Entire's.
func plainLegacyScript(hook string) string {
	return "#!/bin/sh\n" + legacyMarker + "\nbaum hooks git " + hook + " \"$@\"\n"
}

var allLegacyHooks = []string{"commit-msg", "prepare-commit-msg", "post-commit", "post-rewrite", "pre-push"}

func legacyClone(t *testing.T) (repo, hooks string) {
	t.Helper()
	repo = initRepo(t)
	hooks = filepath.Join(repo, ".git", "hooks")
	for _, hook := range allLegacyHooks {
		writeExecutable(t, filepath.Join(hooks, hook), legacyScript(hook))
	}
	return repo, hooks
}

// stubEntire puts an entire on PATH that logs its arguments.
func stubEntire(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "entire.log")
	writeExecutable(t, filepath.Join(bin, "entire"), "#!/bin/sh\necho \"$*\" >> '"+log+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// Whether the repository uses Entire does not matter: a legacy dispatcher that
// runs Entire's hook is replaced, and Entire's hook is kept as the backup the
// new dispatcher runs. The legacy-only hooks, which baum no longer installs,
// are left, since they still run Entire's hook.
func TestLegacyDispatchersThatRunEntireKeepEntiresHook(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	report := ensure(t, repo)
	got := outcomes(report)
	for _, hook := range Hooks {
		if got[hook] != Chained {
			t.Errorf("%s = %v", hook, got[hook])
		}
		if read(t, filepath.Join(hooks, hook)) != Dispatcher(hook, "") {
			t.Errorf("%s is not the dispatcher", hook)
		}
		backup := read(t, filepath.Join(hooks, hook+backupSuffix))
		if !strings.Contains(backup, "entire hooks git "+hook+" \"$1\"") || ours(backup) {
			t.Errorf("%s backup = %q", hook, backup)
		}
	}
	for _, hook := range legacyOnly {
		for _, h := range report.Hooks {
			if h.Hook == hook && (h.Outcome != Unclaimed || h.Reason != runsEntireReason) {
				t.Errorf("%s = %+v", hook, h)
			}
		}
		if read(t, filepath.Join(hooks, hook)) != legacyScript(hook) {
			t.Errorf("%s was changed", hook)
		}
	}
	if again := ensure(t, repo); again.Changed() {
		t.Errorf("a second run changed %+v", again)
	}
}

// The kept call of a dispatcher that buffered its input reads the input the new
// dispatcher passes on.
func TestEntireCallDropsTheOldInputBuffer(t *testing.T) {
	legacy := "#!/bin/sh\n" + legacyMarker + "\n" + entireMarker + "\nif command -v baum >/dev/null 2>&1 && baum hooks git --check pre-push >/dev/null 2>&1; then\n  baum hooks git pre-push \"$@\" < \"$hook_stdin\" || exit $?\nelif command -v entire >/dev/null 2>&1; then\n  entire hooks git pre-push \"$1\" < \"$hook_stdin\" || exit $?\nfi\n"
	if got := entireCall(legacy, "pre-push"); got != `entire hooks git pre-push "$1" || exit $?` {
		t.Fatalf("entireCall = %q", got)
	}
}

// After the repair both baum and Entire see a commit.
func TestRepairedDispatcherRunsBaumAndEntire(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	baumLog, entireLog := stubBaum(t), stubEntire(t)
	ensure(t, repo)
	commitWithHooks(t, repo)
	if n := calls(t, baumLog); n != 1 {
		t.Errorf("baum ran %d times", n)
	}
	data, err := os.ReadFile(entireLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"prepare-commit-msg", "commit-msg", "post-commit"} {
		if !strings.Contains(string(data), "hooks git "+hook) {
			t.Errorf("Entire's %s hook did not run: %q", hook, data)
		}
	}
	if _, err := os.Lstat(filepath.Join(hooks, "post-commit.pre-entire")); err == nil {
		t.Error("an unexpected backup appeared")
	}
}

// A backup name already taken leaves the legacy dispatcher as it is.
func TestLegacyEntireDispatcherWithATakenBackupIsLeft(t *testing.T) {
	repo, hooks := legacyClone(t)
	taken := "#!/bin/sh\necho mine\n"
	writeExecutable(t, filepath.Join(hooks, "post-commit"+backupSuffix), taken)
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("report = %+v", report)
	}
	if read(t, filepath.Join(hooks, "post-commit")) != legacyScript("post-commit") || read(t, filepath.Join(hooks, "post-commit"+backupSuffix)) != taken {
		t.Fatal("files were changed")
	}
}

// Opting out never removes a legacy dispatcher that still runs Entire's hook.
func TestOptOutLeavesLegacyDispatchersThatRunEntire(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "config", "baum.hooks", "false")
	report := ensure(t, repo)
	for _, hook := range allLegacyHooks {
		if read(t, filepath.Join(hooks, hook)) != legacyScript(hook) {
			t.Errorf("%s was changed", hook)
		}
	}
	for _, h := range report.Hooks {
		if h.Outcome != Unclaimed || h.Reason != runsEntireReason {
			t.Errorf("%s = %+v", h.Hook, h)
		}
	}
}

// Opting out after the repair puts Entire's hook back in place.
func TestOptOutAfterTheRepairRestoresEntiresHook(t *testing.T) {
	repo, hooks := legacyClone(t)
	ensure(t, repo)
	git(t, repo, "config", "baum.hooks", "false")
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Restored {
		t.Fatalf("report = %+v", report)
	}
	if got := read(t, filepath.Join(hooks, "post-commit")); !strings.Contains(got, "entire hooks git post-commit") || ours(got) {
		t.Fatalf("post-commit = %q", got)
	}
}

// The hook a legacy dispatcher replaced runs once per commit after the repair,
// and still runs once Entire's hook is restored by an opt-out.
func TestTheHookALegacyDispatcherReplacedKeepsRunning(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	stubBaum(t)
	entireLog := stubEntire(t)
	mine := filepath.Join(t.TempDir(), "mine.log")
	writeExecutable(t, filepath.Join(hooks, "post-commit.pre-entire"), "#!/bin/sh\necho ran >> '"+mine+"'\n")
	countLines := func(path string) int {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
	ensure(t, repo)
	commitWithHooks(t, repo)
	if n := countLines(mine); n != 1 {
		t.Fatalf("with the dispatcher the replaced hook ran %d times", n)
	}
	git(t, repo, "config", "baum.hooks", "false")
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Restored {
		t.Fatalf("report = %+v", report)
	}
	commitWithHooks(t, repo)
	if n := countLines(mine); n != 2 {
		t.Fatalf("after the opt-out the replaced hook ran %d times in all", n)
	}
	if data, _ := os.ReadFile(entireLog); strings.Count(string(data), "hooks git post-commit") != 2 {
		t.Fatalf("Entire's post-commit ran: %q", data)
	}
}

// Both Entire and the replaced pre-push hook read all of the push's input.
func TestKeptPrePushHookFeedsItsInputToBoth(t *testing.T) {
	repo, hooks := legacyClone(t)
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "entire"), "#!/bin/sh\ncat > '"+filepath.Join(dir, "entire.in")+"'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeExecutable(t, filepath.Join(hooks, "pre-push.pre-entire"), "#!/bin/sh\ncat > '"+filepath.Join(dir, "mine.in")+"'\n")
	ensure(t, repo)
	git(t, repo, "config", "baum.hooks", "false")
	ensure(t, repo)
	cmd := exec.Command(filepath.Join(hooks, "pre-push"), "origin", "url")
	cmd.Stdin = strings.NewReader("refs/heads/a 1 refs/heads/a 2\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, name := range []string{"entire.in", "mine.in"} {
		if got := read(t, filepath.Join(dir, name)); got != "refs/heads/a 1 refs/heads/a 2\n" {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// Legacy dispatchers that run no hook of Entire's are repaired and the others
// retired: the hook they replaced comes back, or they go.
func TestPlainLegacyDispatchersAreRepairedAndRetired(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	for _, hook := range allLegacyHooks {
		writeExecutable(t, filepath.Join(hooks, hook), plainLegacyScript(hook))
	}
	original := "#!/bin/sh\necho mine\n"
	writeExecutable(t, filepath.Join(hooks, "commit-msg.pre-entire"), original)
	report := ensure(t, repo)
	got := outcomes(report)
	want := map[string]Outcome{"post-commit": Repaired, "post-rewrite": Repaired, "pre-push": Repaired, "commit-msg": Restored, "prepare-commit-msg": Removed}
	for hook, outcome := range want {
		if got[hook] != outcome {
			t.Errorf("%s = %v, want %v", hook, got[hook], outcome)
		}
	}
	if read(t, filepath.Join(hooks, "commit-msg")) != original {
		t.Error("the commit-msg hook did not come back")
	}
	if _, err := os.Lstat(filepath.Join(hooks, "prepare-commit-msg")); err == nil {
		t.Error("the prepare-commit-msg dispatcher is still there")
	}
	if again := ensure(t, repo); again.Changed() {
		t.Errorf("a second run changed %+v", again)
	}
}

func TestRetireLeavesAHookItCannotRestoreOrDoesNotOwn(t *testing.T) {
	repo, hooks := legacyClone(t)
	writeExecutable(t, filepath.Join(hooks, "commit-msg"), plainLegacyScript("commit-msg"))
	writeExecutable(t, filepath.Join(hooks, "commit-msg.pre-entire"), legacyScript("commit-msg"))
	foreign := "#!/bin/sh\necho theirs\n"
	writeExecutable(t, filepath.Join(hooks, "prepare-commit-msg"), foreign)
	report := ensure(t, repo)
	for _, h := range report.Hooks {
		if h.Hook == "commit-msg" && h.Outcome != Unclaimed {
			t.Errorf("commit-msg = %+v", h)
		}
		if h.Hook == "prepare-commit-msg" {
			t.Errorf("a hook without the marker was reported: %+v", h)
		}
	}
	if read(t, filepath.Join(hooks, "prepare-commit-msg")) != foreign || read(t, filepath.Join(hooks, "commit-msg")) != plainLegacyScript("commit-msg") {
		t.Error("files were changed")
	}
}

// Entire's own hook, with no dispatcher of baum's around, is chained like any
// other: both run.
func TestEntiresOwnHookIsChained(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	entire := "#!/bin/sh\n" + entireMarker + "\nentire hooks git post-commit 2>/dev/null || true\n"
	writeExecutable(t, filepath.Join(hooks, "post-commit"), entire)
	baumLog, entireLog := stubBaum(t), stubEntire(t)
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Chained {
		t.Fatalf("report = %+v", report)
	}
	if read(t, filepath.Join(hooks, "post-commit"+backupSuffix)) != entire {
		t.Fatal("Entire's hook was not kept as it is")
	}
	commitWithHooks(t, repo)
	if n := calls(t, baumLog); n != 1 {
		t.Errorf("baum ran %d times", n)
	}
	if data, err := os.ReadFile(entireLog); err != nil || !strings.Contains(string(data), "hooks git post-commit") {
		t.Errorf("Entire's hook did not run: %q %v", data, err)
	}
}

// A dispatcher another tool moved aside is reported, not taken for absent.
func TestOptOutReportsADispatcherInASiblingSlot(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "post-commit.pre-entire"), Dispatcher("post-commit", ""))
	writeExecutable(t, filepath.Join(hooks, "post-commit"), "#!/bin/sh\n# Entire CLI hooks\n# entire hooks git post-commit\n")
	git(t, repo, "config", "baum.hooks", "false")
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("report = %+v", report)
	}
}

// A dispatcher moved aside is reported when the hook itself is missing too.
func TestOptOutReportsADispatcherWhenTheHookIsMissing(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "post-commit.legacy"), Dispatcher("post-commit", ""))
	git(t, repo, "config", "baum.hooks", "false")
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("report = %+v", report)
	}
}

// Only the path of the fallback binary differs: a binary that is still there is
// not a reason to rewrite; one that is gone is.
func TestChangedFallbackBinaryRepairsOnlyWhenTheOldOneIsGone(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	old, moved := filepath.Join(dir, "old-baum"), filepath.Join(dir, "new-baum")
	writeExecutable(t, old, "#!/bin/sh\n")
	writeExecutable(t, moved, "#!/bin/sh\n")
	if _, err := EnsureWith(t.Context(), repo, Options{Env: isolated, Baum: old}); err != nil {
		t.Fatal(err)
	}
	report, err := EnsureWith(t.Context(), repo, Options{Env: isolated, Baum: moved})
	if err != nil || report.Changed() || outcomes(report)["post-commit"] != Current {
		t.Fatalf("a still-present binary caused a repair: %+v %v", report, err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	report, err = EnsureWith(t.Context(), repo, Options{Env: isolated, Baum: moved})
	if err != nil || outcomes(report)["post-commit"] != Repaired {
		t.Fatalf("a missing binary did not repair: %+v %v", report, err)
	}
	if read(t, filepath.Join(repo, ".git", "hooks", "post-commit")) != Dispatcher("post-commit", moved) {
		t.Error("the repaired dispatcher does not name the new binary")
	}
}
