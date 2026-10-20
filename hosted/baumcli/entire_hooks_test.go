//go:build !windows

package baumcli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const entireCommitMsgHook = `#!/bin/sh
# Entire CLI hooks
if command -v entire >/dev/null 2>&1; then entire hooks git commit-msg "$1" || true; fi
`

func TestOwnEntireGitHooksDispatchesThroughBaum(t *testing.T) {
	repo := initHookTestRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeHookTestFile(t, filepath.Join(hooks, "commit-msg"), entireCommitMsgHook)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	entireLog := filepath.Join(repo, "entire.log")
	baumLog := filepath.Join(repo, "baum.log")
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+entireLog+"\"\nexit 17\n")
	writeHookTestFile(t, filepath.Join(bin, "baum"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+baumLog+"\"\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	message := filepath.Join(repo, "message")
	cmd := exec.Command(filepath.Join(hooks, "commit-msg"), message)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dispatcher: %v: %s", err, output)
	}
	if got := readHookTestFile(t, baumLog); got != "hooks git commit-msg "+message {
		t.Errorf("baum invocation = %q", got)
	}
	if _, err := os.Stat(entireLog); !os.IsNotExist(err) {
		t.Errorf("shell hook invoked Entire directly: %v", err)
	}
}

func TestOwnEntireGitHooksRepairsManagedHook(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "commit-msg")
	writeHookTestFile(t, hook, entireCommitMsgHook)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	writeHookTestFile(t, hook, entireCommitMsgHook)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); !strings.Contains(got, baumHookMarker) || !strings.Contains(got, entireHookMarker) {
		t.Fatalf("repaired hook = %q", got)
	}
}

func TestGitHookFallsBackToEntireWhenBaumIsUnavailable(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	writeHookTestFile(t, hook, "#!/bin/sh\nentire hooks git pre-push \"$1\" || true\n")
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(repo, "entire-fallback.log")
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+log+"\"\n")
	t.Setenv("PATH", bin)
	if output, err := exec.Command(hook, "origin", "https://example.test/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("fallback hook: %v: %s", err, output)
	}
	if got := readHookTestFile(t, log); got != "hooks git pre-push origin" {
		t.Errorf("Entire fallback invocation = %q", got)
	}
}

func TestBaumGitHookCommandDelegatesToEntireAndIgnoresInformationalFailure(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "entire.log")
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+log+"\"\nexit 17\n")
	t.Setenv("PATH", bin)
	stdout, stderr, err := runCLI(t, "hooks", "git", "commit-msg", "message-file")
	if err != nil || stdout != "" || !strings.Contains(stderr, "Entire Git hook failed; continuing") {
		t.Fatalf("hooks command: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if got := readHookTestFile(t, log); got != "hooks git commit-msg message-file" {
		t.Errorf("Entire invocation = %q", got)
	}
}

// TestBaumGitHookFindsEntireOutsideThePath covers the machine the
// bootstrap installs Entire for: the install directory is not on the PATH, and
// a Git hook still has to reach the binary the session start put there.
func TestBaumGitHookFindsEntireOutsideThePath(t *testing.T) {
	withoutEntire(t)
	log := filepath.Join(t.TempDir(), "entire.log")
	writeExecutable(t, filepath.Join(entireInstallDir, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+log+"\"\n")

	stdout, stderr, err := runCLI(t, "hooks", "git", "commit-msg", "message-file")
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("hooks command: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if got := readHookTestFile(t, log); got != "hooks git commit-msg message-file" {
		t.Errorf("Entire invocation = %q", got)
	}
}

func TestBaumPrePushPropagatesEntireFailure(t *testing.T) {
	bin := t.TempDir()
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\nexit 17\n")
	t.Setenv("PATH", bin)
	_, _, err := runCLI(t, "hooks", "git", "pre-push", "origin", "https://example.test/repo.git")
	if err == nil || !strings.Contains(err.Error(), "Entire pre-push hook failed") {
		t.Fatalf("pre-push error = %v", err)
	}
}

func TestPrePushDispatcherPropagatesBaumFailure(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	writeHookTestFile(t, hook, "#!/bin/sh\n# Entire CLI hooks\nentire hooks git pre-push \"$1\"\n")
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeHookTestFile(t, filepath.Join(bin, "baum"), "#!/bin/sh\nif [ \"$3\" = '--check' ]; then exit 0; fi\nexit 29\n")
	t.Setenv("PATH", bin)
	cmd := exec.Command(hook, "origin", "https://example.test/repo.git")
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 29 {
		t.Fatalf("pre-push error = %v", err)
	}
}

func TestGitHookFallsBackToEntireWhenBaumDoesNotSupportHooks(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "commit-msg")
	writeHookTestFile(t, hook, entireCommitMsgHook)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(repo, "entire-fallback.log")
	writeHookTestFile(t, filepath.Join(bin, "baum"), "#!/bin/sh\nexit 1\n")
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s' \"$*\" > \""+log+"\"\n")
	t.Setenv("PATH", bin)
	if output, err := exec.Command(hook, "message").CombinedOutput(); err != nil {
		t.Fatalf("fallback hook: %v: %s", err, output)
	}
	if got := readHookTestFile(t, log); got != "hooks git commit-msg message" {
		t.Errorf("Entire fallback invocation = %q", got)
	}
}

func TestEntireGitHookArgsMatchEntireGeneratedHooks(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{[]string{"commit-msg", "message", "ignored"}, "hooks git commit-msg message"},
		{[]string{"post-commit", "ignored"}, "hooks git post-commit"},
		{[]string{"post-rewrite", "amend", "ignored"}, "hooks git post-rewrite amend"},
		{[]string{"pre-push", "origin", "https://example.test/repo.git"}, "hooks git pre-push origin"},
		{[]string{"prepare-commit-msg", "message", "commit", "ignored"}, "hooks git prepare-commit-msg message commit"},
	}
	for _, test := range tests {
		got, ok := entireGitHookArgs(test.input)
		if !ok || strings.Join(got, " ") != test.want {
			t.Errorf("entireGitHookArgs(%q) = %q, %v; want %q", test.input, got, ok, test.want)
		}
	}
}

func TestBaumGitHookForwardsStdinToEntire(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "stdin.log")
	writeHookTestFile(t, filepath.Join(bin, "entire"), "#!/bin/sh\ncat > \""+log+"\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := newRootCommand(bytes.NewBufferString("old new refs/heads/main\n"), &bytes.Buffer{}, &bytes.Buffer{})
	command.SetArgs([]string{"hooks", "git", "post-rewrite", "amend"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, log); got != "old new refs/heads/main\n" {
		t.Errorf("Entire stdin = %q", got)
	}
}

func TestOwnEntireGitHooksRefusesCustomHooksPath(t *testing.T) {
	repo := initHookTestRepo(t)
	runGitTest(t, repo, "config", "core.hooksPath", ".githooks")
	if err := os.Mkdir(filepath.Join(repo, ".githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, ".githooks", "commit-msg")
	writeHookTestFile(t, hook, entireCommitMsgHook)
	if err := ownEntireGitHooks(t.Context(), repo); !errors.Is(err, errCustomGitHooksPath) {
		t.Fatalf("own hooks error = %v", err)
	}
	if got := readHookTestFile(t, hook); got != entireCommitMsgHook {
		t.Errorf("custom hook was changed: %q", got)
	}
}

func TestOwnEntireHooksDuringSetupReportsCustomHooksPathAsSkip(t *testing.T) {
	repo := initHookTestRepo(t)
	runGitTest(t, repo, "config", "core.hooksPath", ".githooks")
	var stdout bytes.Buffer
	if err := ownEntireHooksDuringSetup(t.Context(), repo, &stdout); err != nil {
		t.Fatalf("own hooks during setup: %v", err)
	}
	if !strings.Contains(stdout.String(), "core.hooksPath is set") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestOwnEntireGitHooksSkipsSymlinksAndUnmarkedHooks(t *testing.T) {
	repo := initHookTestRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	target := filepath.Join(repo, "target-hook")
	writeHookTestFile(t, target, entireCommitMsgHook)
	if err := os.Symlink(target, filepath.Join(hooks, "commit-msg")); err != nil {
		t.Fatal(err)
	}
	unmarked := "#!/bin/sh\nentire hooks git pre-push \"$1\"\n"
	writeHookTestFile(t, filepath.Join(hooks, "pre-push"), unmarked)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, target); got != entireCommitMsgHook {
		t.Errorf("symlink target was changed: %q", got)
	}
	if got := readHookTestFile(t, filepath.Join(hooks, "pre-push")); got != unmarked {
		t.Errorf("unmarked hook was changed: %q", got)
	}
}

func TestOwnEntireGitHooksSkipsUnsupportedEntireHook(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "post-merge")
	contents := "#!/bin/sh\n# Entire CLI hooks\nentire hooks git post-merge \"$1\"\n"
	writeHookTestFile(t, hook, contents)
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if got := readHookTestFile(t, hook); got != contents {
		t.Errorf("unsupported hook was changed: %q", got)
	}
}

func TestGitHookDispatcherRunsPreEntireBackupAndPropagatesFailure(t *testing.T) {
	repo := initHookTestRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	hook := filepath.Join(hooks, "commit-msg")
	writeHookTestFile(t, hook, entireCommitMsgHook)
	backupLog := filepath.Join(repo, "backup.log")
	writeHookTestFile(t, hook+".pre-entire", "#!/bin/sh\nprintf '%s' \"$*\" > \""+backupLog+"\"\nexit 23\n")
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	cmd := exec.Command(hook, "message-file")
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 23 {
		t.Fatalf("hook error = %v", err)
	}
	if got := readHookTestFile(t, backupLog); got != "message-file" {
		t.Errorf("backup arguments = %q", got)
	}
}

func TestPrePushDispatcherReplaysStdinToBackupAfterBaumReadsIt(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	writeHookTestFile(t, hook, "#!/bin/sh\n# Entire CLI hooks\nentire hooks git pre-push \"$1\"\n")
	backupLog := filepath.Join(repo, "backup-stdin.log")
	writeHookTestFile(t, hook+".pre-entire", "#!/bin/sh\ncat > \""+backupLog+"\"\n")
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// Drains standard input, as Entire is free to do when it inspects the refs
	// being pushed; the chained hook must still see the full list.
	writeHookTestFile(t, filepath.Join(bin, "baum"), "#!/bin/sh\nif [ \"$3\" = '--check' ]; then exit 0; fi\ncat >/dev/null\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	cmd := exec.Command(hook, "origin", "https://example.test/repo.git")
	cmd.Stdin = strings.NewReader("refs/heads/main aaa refs/heads/main bbb\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, output)
	}
	if got := readHookTestFile(t, backupLog); got != "refs/heads/main aaa refs/heads/main bbb\n" {
		t.Errorf("backup stdin = %q", got)
	}
}

func TestPostRewriteDispatcherReplaysStdinToBackup(t *testing.T) {
	repo := initHookTestRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	hook := filepath.Join(hooks, "post-rewrite")
	writeHookTestFile(t, hook, "#!/bin/sh\n# Entire CLI hooks\nentire hooks git post-rewrite \"$1\"\n")
	backupLog := filepath.Join(repo, "backup-stdin.log")
	writeHookTestFile(t, hook+".pre-entire", "#!/bin/sh\ncat > \""+backupLog+"\"\n")
	if err := ownEntireGitHooks(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	cmd := exec.Command(hook, "amend")
	cmd.Stdin = strings.NewReader("old new\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, output)
	}
	if got := readHookTestFile(t, backupLog); got != "old new\n" {
		t.Errorf("backup stdin = %q", got)
	}
}

func initHookTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	return repo
}

func writeHookTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readHookTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
