package gitlocal

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHubCloneURL(t *testing.T) {
	got, err := GitHubCloneURL("Org/my.repo-1")
	if err != nil || got != "https://github.com/Org/my.repo-1.git" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "org", "org/", "/repo", "org/repo/extra", "https://github.com/org/repo", "org/..", "org/.", "-org/repo", "org/re po", "org/re?po", "org/repo@x", "o rg/repo", "org/" + strings.Repeat("a", 101)} {
		if _, err := GitHubCloneURL(bad); !errors.Is(err, ErrInvalidCloneURL) {
			t.Errorf("GitHubCloneURL(%q) = %v, want ErrInvalidCloneURL", bad, err)
		}
	}
}

func TestCloneAcceptsOnlyURLsFromGitHubCloneURL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clone")
	for _, bad := range []string{
		"file:///tmp/repo", "ssh://git@github.com/org/repo.git", "git@github.com:org/repo.git", "http://github.com/org/repo.git",
		"https://user:pw@github.com/org/repo.git", "https://github.com:8443/org/repo.git", "https://evil.example/org/repo.git",
		"https://github.com/org/repo", "https://github.com/org/repo.git?x=1", "https://github.com/org/repo/extra.git", "https://github.com/-org/repo.git", "",
	} {
		if err := Clone(context.Background(), CloneOptions{URL: bad, Dir: dir, Token: "t"}); !errors.Is(err, ErrInvalidCloneURL) {
			t.Errorf("Clone(%q) = %v, want ErrInvalidCloneURL", bad, err)
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Error("a refused URL created the target")
	}
}

func TestCloneKeepsTheTokenInTheEnvironmentAndHardensGit(t *testing.T) {
	var spec runSpec
	var args []string
	original := cloneRun
	t.Cleanup(func() { cloneRun = original })
	cloneRun = func(_ context.Context, s runSpec, a ...string) ([]byte, error) {
		spec, args = s, a
		return nil, nil
	}
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_CURL_VERBOSE", "1")
	dir := filepath.Join(t.TempDir(), "a", "b", "repo")
	const token = "PRIVATE_CLONE_CREDENTIAL"
	if err := Clone(context.Background(), CloneOptions{URL: "https://github.com/org/repo.git", Dir: dir, Token: token}); err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(args, "\x00") + "\x00" + strings.Join(spec.config, "\x00")
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if strings.Contains(argv, token) || strings.Contains(argv, encoded) {
		t.Fatalf("token in arguments: %q", argv)
	}
	env := strings.Join(spec.env, "\n")
	for _, want := range []string{
		"Authorization: Basic " + encoded, "http.https://github.com/org/repo.git.extraHeader", "GIT_CONFIG_KEY_", "credential.helper", "http.followRedirects", "GIT_TERMINAL_PROMPT=0",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %q", want)
		}
	}
	for _, unwanted := range []string{"GIT_TRACE=", "GIT_CURL_VERBOSE="} {
		if strings.Contains(env, unwanted) {
			t.Errorf("environment keeps %q", unwanted)
		}
	}
	for _, want := range []string{"protocol.ext.allow=never", "core.fsmonitor=false", "http.followRedirects=false", "protocol.allow=never"} {
		found := false
		for _, c := range spec.config {
			found = found || c == want
		}
		if !found {
			t.Errorf("config lacks %s: %v", want, spec.config)
		}
	}
	hooks := false
	for _, c := range spec.config {
		hooks = hooks || (strings.HasPrefix(c, "core.hooksPath=") && len(c) > len("core.hooksPath="))
	}
	if !hooks {
		t.Errorf("no hooks path in %v", spec.config)
	}
	if args[0] != "clone" || args[len(args)-1] != dir || args[len(args)-2] != "https://github.com/org/repo.git" || !contains(args, "--no-recurse-submodules") || !contains(args, "--") {
		t.Errorf("args = %v", args)
	}

	// Without a token the environment carries no credential.
	if err := Clone(context.Background(), CloneOptions{URL: "https://github.com/org/repo.git", Dir: filepath.Join(t.TempDir(), "anon")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.env, "\n"), "Authorization") {
		t.Error("an anonymous clone carries a credential")
	}
	if !contains(spec.config, "protocol.ext.allow=never") {
		t.Error("an anonymous clone is not hardened")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestCloneFromALocalRepositoryRunsNoHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts")
	}
	f := newFixture(t)
	allowFileCloneURL = true
	t.Cleanup(func() { allowFileCloneURL = false })

	// The user's own configuration points hooks at a directory whose
	// post-checkout hook leaves a mark.
	hooks := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	write(t, hooks, "post-checkout", "#!/bin/sh\ntouch '"+marker+"'\n", 0o755)
	global := filepath.Join(t.TempDir(), "gitconfig")
	write(t, filepath.Dir(global), filepath.Base(global), "[core]\n\thooksPath = "+hooks+"\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	dir := filepath.Join(t.TempDir(), "code", "owner", "repo")
	if err := Clone(context.Background(), CloneOptions{URL: "file://" + f.dir, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatalf("working tree not checked out: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a post-checkout hook ran")
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
			if info, err := os.Stat(p); err != nil || info.Mode().Perm()&0o055 != 0o055 {
				t.Errorf("%s is not a normal readable directory: %v %v", p, info, err)
			}
		}
	}
	if out, err := os.ReadFile(filepath.Join(dir, ".git", "config")); err != nil || strings.Contains(string(out), "hooksPath") {
		t.Errorf("clone config persisted the temporary hooks path: %v\n%s", err, out)
	}
}

func TestCloneRefusesANonEmptyTargetAndCleansUpAFailure(t *testing.T) {
	requireGit(t)
	allowFileCloneURL = true
	t.Cleanup(func() { allowFileCloneURL = false })
	missing := "file://" + filepath.Join(t.TempDir(), "no-such-repository")

	root := t.TempDir()
	full := filepath.Join(root, "full")
	write(t, full, "mine.txt", "keep", 0o644)
	if err := Clone(context.Background(), CloneOptions{URL: missing, Dir: full}); !errors.Is(err, ErrCloneTargetExists) {
		t.Fatalf("err = %v, want ErrCloneTargetExists", err)
	}
	if b, err := os.ReadFile(filepath.Join(full, "mine.txt")); err != nil || string(b) != "keep" {
		t.Fatal("the refused target was touched")
	}

	nested := filepath.Join(root, "new", "owner", "repo")
	if err := Clone(context.Background(), CloneOptions{URL: missing, Dir: nested}); err == nil {
		t.Fatal("cloning a missing repository succeeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); err == nil {
		t.Error("a failed clone left the directories it created")
	}

	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), CloneOptions{URL: missing, Dir: empty}); err == nil {
		t.Fatal("cloning a missing repository succeeded")
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("the pre-existing empty directory = %v, %v; want kept and empty", entries, err)
	}
}

func TestCloneFailureWithATokenIsSanitized(t *testing.T) {
	original := cloneRun
	t.Cleanup(func() { cloneRun = original })
	cloneRun = func(context.Context, runSpec, ...string) ([]byte, error) {
		return nil, &Error{Command: "clone", Stderr: "fatal: could not read https://x-access-token:SECRET@github.com/org/repo.git"}
	}
	dir := filepath.Join(t.TempDir(), "x", "repo")
	err := Clone(context.Background(), CloneOptions{URL: "https://github.com/org/repo.git", Dir: dir, Token: "SECRET"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Dir(dir)); statErr == nil {
		t.Error("failed clone left its folder")
	}
}
