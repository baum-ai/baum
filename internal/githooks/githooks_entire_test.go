//go:build !windows

package githooks

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyScript is what pre-release baum put in place of Entire's hook.
func legacyScript(hook string) string {
	return "#!/bin/sh\n" + legacyMarker + "\n" + entireMarker + "\nif command -v entire >/dev/null 2>&1; then\n  entire hooks git " + hook + " \"$1\"\nfi\n"
}

func enableEntire(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".entire"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".entire", "settings.json"), []byte(`{"enabled":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func legacyClone(t *testing.T) (repo, hooks string) {
	t.Helper()
	repo = initRepo(t)
	hooks = filepath.Join(repo, ".git", "hooks")
	for _, hook := range []string{"commit-msg", "prepare-commit-msg", "post-commit", "post-rewrite", "pre-push"} {
		writeExecutable(t, filepath.Join(hooks, hook), legacyScript(hook))
	}
	return repo, hooks
}

// Where Entire is enabled, the legacy dispatchers run Entire's hooks: all five
// stay, so Entire keeps working as a whole.
func TestEntireEnabledCloneKeepsItsLegacyDispatchers(t *testing.T) {
	repo, hooks := legacyClone(t)
	enableEntire(t, repo)
	report := ensure(t, repo)
	for _, hook := range []string{"commit-msg", "prepare-commit-msg", "post-commit", "post-rewrite", "pre-push"} {
		if read(t, filepath.Join(hooks, hook)) != legacyScript(hook) {
			t.Errorf("%s was changed", hook)
		}
		found := false
		for _, h := range report.Hooks {
			if h.Hook == hook {
				found = true
				if h.Outcome != Unclaimed || h.Reason != entireEnabledReason {
					t.Errorf("%s = %+v", hook, h)
				}
			}
		}
		if !found {
			t.Errorf("%s is not in the report", hook)
		}
	}
}

// A linked worktree counts as Entire-enabled by the main worktree's settings.
func TestEntireSettingsOfTheMainWorktreeCoverLinkedOnes(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	enableEntire(t, repo)
	tree := filepath.Join(t.TempDir(), "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", tree)
	ensure(t, tree)
	if read(t, filepath.Join(hooks, "post-commit")) != legacyScript("post-commit") {
		t.Fatal("the legacy dispatcher was repaired in an Entire-enabled repository")
	}
}

// Without Entire the three hooks are repaired and the others retired: the hook
// they replaced comes back, or they go.
func TestLegacyDispatchersAreRepairedAndRetiredWithoutEntire(t *testing.T) {
	repo, hooks := legacyClone(t)
	original := "#!/bin/sh\necho mine\n"
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	writeExecutable(t, filepath.Join(hooks, "commit-msg.pre-entire"), original)
	report := ensure(t, repo)
	got := map[string]Outcome{}
	for _, h := range report.Hooks {
		got[h.Hook] = h.Outcome
	}
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
	if read(t, filepath.Join(hooks, "prepare-commit-msg")) != foreign || read(t, filepath.Join(hooks, "commit-msg")) != legacyScript("commit-msg") {
		t.Error("files were changed")
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
