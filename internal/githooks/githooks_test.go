//go:build !windows

package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolated keeps the developer's own Git configuration out of the tests.
var isolated = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), isolated...),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	return dir
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func outcomes(r Report) map[string]Outcome {
	m := map[string]Outcome{}
	for _, h := range r.Hooks {
		m[h.Hook] = h.Outcome
	}
	return m
}

func ensure(t *testing.T, root string) Report {
	t.Helper()
	report, err := EnsureWith(t.Context(), root, Options{Env: isolated})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestEnsureInstallsAllHooksAndIsIdempotent(t *testing.T) {
	repo := initRepo(t)
	report := ensure(t, repo)
	for _, hook := range Hooks {
		if report.Hooks == nil || outcomes(report)[hook] != Installed {
			t.Fatalf("%s outcome = %v", hook, outcomes(report))
		}
		info, err := os.Stat(filepath.Join(repo, ".git", "hooks", hook))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode: %v %v", hook, info, err)
		}
		if !strings.Contains(read(t, filepath.Join(repo, ".git", "hooks", hook)), versionLine) {
			t.Errorf("%s lacks the version line", hook)
		}
	}
	again := ensure(t, repo)
	if again.Changed() {
		t.Fatalf("second run changed hooks: %v", again)
	}
	for _, hook := range Hooks {
		if outcomes(again)[hook] != Current {
			t.Errorf("%s = %v", hook, outcomes(again)[hook])
		}
	}
}

func TestEnsureChainsForeignHookWithArgsAndStdin(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	log := filepath.Join(t.TempDir(), "foreign.log")
	writeExecutable(t, filepath.Join(hooks, "post-rewrite"), "#!/bin/sh\nprintf '%s|' \"$*\" > '"+log+"'\ncat >> '"+log+"'\n")
	report := ensure(t, repo)
	if outcomes(report)["post-rewrite"] != Chained {
		t.Fatalf("outcomes = %v", outcomes(report))
	}
	if got := read(t, filepath.Join(hooks, "post-rewrite.pre-baum")); !strings.Contains(got, "foreign.log") {
		t.Fatalf("backup = %q", got)
	}
	bin := t.TempDir()
	baumLog := filepath.Join(bin, "baum.log")
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\nprintf '%s;' \"$*\" >> '"+baumLog+"'\ncat >> '"+baumLog+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command(filepath.Join(hooks, "post-rewrite"), "amend")
	cmd.Stdin = strings.NewReader("old new\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, out)
	}
	if got := read(t, log); got != "amend|old new\n" {
		t.Errorf("chained hook got %q", got)
	}
	if got := read(t, baumLog); got != "hooks git --check-dispatcher post-rewrite;hooks git post-rewrite amend;old new\n" {
		t.Errorf("baum got %q", got)
	}
}

func TestEnsureChainsExistingEntireBackup(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	log := filepath.Join(t.TempDir(), "entire.log")
	writeExecutable(t, filepath.Join(hooks, "post-commit.pre-entire"), "#!/bin/sh\necho ran > '"+log+"'\n")
	ensure(t, repo)
	t.Setenv("PATH", "/usr/bin:/bin")
	if out, err := exec.Command(filepath.Join(hooks, "post-commit")).CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, out)
	}
	if read(t, log) != "ran\n" {
		t.Error("the pre-entire backup did not run")
	}
}

func TestEnsureLeavesHookWhenBackupSlotTaken(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "pre-push"), "#!/bin/sh\necho mine\n")
	writeExecutable(t, filepath.Join(hooks, "pre-push.pre-baum"), "#!/bin/sh\necho older\n")
	report := ensure(t, repo)
	if outcomes(report)["pre-push"] != Unclaimed {
		t.Fatalf("outcomes = %v", outcomes(report))
	}
	if read(t, filepath.Join(hooks, "pre-push")) != "#!/bin/sh\necho mine\n" || read(t, filepath.Join(hooks, "pre-push.pre-baum")) != "#!/bin/sh\necho older\n" {
		t.Error("files were touched")
	}
	if outcomes(report)["post-commit"] != Installed {
		t.Errorf("other hooks must still install: %v", outcomes(report))
	}
}

func TestEnsureNeverTouchesSymlinkedHook(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	target := filepath.Join(t.TempDir(), "shared-hook")
	writeExecutable(t, target, "#!/bin/sh\necho shared\n")
	if err := os.Symlink(target, filepath.Join(hooks, "post-commit")); err != nil {
		t.Fatal(err)
	}
	report := ensure(t, repo)
	if outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("outcomes = %v", outcomes(report))
	}
	if read(t, target) != "#!/bin/sh\necho shared\n" {
		t.Error("the symlink target was written")
	}
	if info, _ := os.Lstat(filepath.Join(hooks, "post-commit")); info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}
	if _, err := os.Lstat(filepath.Join(hooks, "post-commit.pre-baum")); err == nil {
		t.Error("a backup was made of a symlink")
	}
}

func TestEnsureSkipsCustomHooksPath(t *testing.T) {
	repo := initRepo(t)
	git(t, repo, "config", "core.hooksPath", ".githooks")
	report := ensure(t, repo)
	if report.Skipped == "" || len(report.Hooks) != 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, dir := range []string{filepath.Join(repo, ".git", "hooks", "post-commit"), filepath.Join(repo, ".githooks")} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s was written", dir)
		}
	}
}

func TestEnsureSkipsBareRepository(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "--bare")
	if report := ensure(t, dir); report.Skipped == "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestEnsureRepairsLegacyDispatchers(t *testing.T) {
	repo := initRepo(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "post-commit"), "#!/bin/sh\n"+legacyMarker+"\n# Entire CLI hooks\nold\n")
	writeExecutable(t, filepath.Join(hooks, "pre-push"), "#!/bin/sh\n"+legacyMarker+"\n# Entire CLI hooks\nentire hooks git pre-push \"$1\"\n")
	writeExecutable(t, filepath.Join(hooks, "post-rewrite"), Dispatcher("post-rewrite", "")+"# stale\n")
	report := ensure(t, repo)
	for _, hook := range Hooks {
		if outcomes(report)[hook] != Repaired {
			t.Errorf("%s = %v", hook, outcomes(report)[hook])
		}
		if read(t, filepath.Join(hooks, hook)) != Dispatcher(hook, "") {
			t.Errorf("%s not repaired", hook)
		}
		if _, err := os.Lstat(filepath.Join(hooks, hook+".pre-baum")); err == nil {
			t.Errorf("%s was backed up as foreign", hook)
		}
	}
}

func TestEnsureSharesHooksWithLinkedWorktrees(t *testing.T) {
	repo := initRepo(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	tree := filepath.Join(t.TempDir(), "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", tree)
	ensure(t, tree)
	if read(t, filepath.Join(repo, ".git", "hooks", "post-commit")) != Dispatcher("post-commit", "") {
		t.Fatal("the common hooks directory has no dispatcher")
	}
	if again := ensure(t, repo); again.Changed() {
		t.Errorf("main checkout changed the shared hooks: %+v", again)
	}
}

func TestPrePushBackupFailureBlocksButBaumFailureDoesNot(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	ensure(t, repo)
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\nexit 29\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command(filepath.Join(hooks, "pre-push"), "origin", "url")
	cmd.Stdin = strings.NewReader("refs\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("baum failure blocked the push: %v: %s", err, out)
	}
	writeExecutable(t, filepath.Join(hooks, "pre-push.pre-baum"), "#!/bin/sh\ncat > /dev/null\nexit 17\n")
	cmd = exec.Command(filepath.Join(hooks, "pre-push"), "origin", "url")
	cmd.Stdin = strings.NewReader("refs\n")
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 17 {
		t.Fatalf("chained failure = %v", err)
	}
}

func TestPostCommitNeverFailsBecauseOfBaum(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	ensure(t, repo)
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\nexit 29\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, hook := range []string{"post-commit", "post-rewrite"} {
		if out, err := exec.Command(filepath.Join(hooks, hook), "amend").CombinedOutput(); err != nil {
			t.Errorf("%s with failing baum: %v: %s", hook, err, out)
		}
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, hook := range []string{"post-commit", "post-rewrite"} {
		if out, err := exec.Command(filepath.Join(hooks, hook), "amend").CombinedOutput(); err != nil || len(out) != 0 {
			t.Errorf("%s without baum: %v: %q", hook, err, out)
		}
	}
}

func TestDispatcherSkipsBaumWhenCheckDeclines(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	ensure(t, repo)
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\necho \"$*\" >> '"+log+"'\nif [ \"$3\" = --check-dispatcher ]; then exit 1; fi\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := exec.Command(filepath.Join(hooks, "post-commit")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := read(t, log); got != "hooks git --check-dispatcher post-commit\n" {
		t.Errorf("baum calls = %q", got)
	}
}
