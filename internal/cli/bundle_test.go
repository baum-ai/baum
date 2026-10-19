package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
)

// fakeGitHub stands in for GitHub: it answers ResolvePR and Fetch from the
// fixture, and records what it was asked.
type fakeGitHub struct {
	assembletest.GitHub
	resolveErr error
	resolved   []github.ResolveOptions
	// onFetch runs when the evidence is asked for.
	onFetch func()
}

func (f *fakeGitHub) Fetch(ctx context.Context, repo github.Repository, number int, opts github.FetchOptions) (github.Evidence, error) {
	if f.onFetch != nil {
		f.onFetch()
	}
	return f.GitHub.Fetch(ctx, repo, number, opts)
}

func (f *fakeGitHub) ResolvePR(_ context.Context, _ github.Clone, opts github.ResolveOptions) (github.Target, error) {
	f.resolved = append(f.resolved, opts)
	if f.resolveErr != nil {
		return github.Target{}, f.resolveErr
	}
	number := opts.PR
	if number == 0 {
		number = assembletest.Number
	}
	repo := assembletest.Repository
	if opts.Repo != "" {
		var err error
		if repo, err = github.ParseRepository(opts.Repo); err != nil {
			return github.Target{}, err
		}
	}
	return github.Target{Repository: repo, Number: number}, nil
}

// bundleEnv gives the command a clone to work in, a home without sessions
// or configuration, and a fake GitHub.
func bundleEnv(t *testing.T) (assembletest.Repo, *fakeGitHub, []string) {
	t.Helper()
	repo := assembletest.NewRepo(t, assembletest.Options{})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Chdir(repo.Dir)
	fake := &fakeGitHub{GitHub: assembletest.GitHub{Evidence: repo.Evidence()}}
	oldGitHub, oldImages := newGitHub, newImageFetcher
	newGitHub = func(config.Config) pullRequestSource { return fake }
	newImageFetcher = func() assemble.ImageFetcher {
		return func(context.Context, string, int64) ([]byte, error) { return assembletest.PNG, nil }
	}
	t.Cleanup(func() { newGitHub, newImageFetcher = oldGitHub, oldImages })
	stores := []string{
		"--claude-dir", filepath.Join(home, "claude"),
		"--codex-dir", filepath.Join(home, "codex"),
		"--cursor-store", filepath.Join(home, "absent.vscdb"),
	}
	return repo, fake, stores
}

func TestBundleWritesOnlyTheBundleToStdout(t *testing.T) {
	repo, fake, stores := bundleEnv(t)
	before := repo.State(t)
	stdout, stderr, err := run(t, append([]string{"internal", "bundle", "--pr", "7"}, stores...)...)
	if err != nil {
		t.Fatalf("bundle: %v\n%s", err, stderr)
	}
	var decoded bundle.Bundle
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not the bundle JSON: %v\n%.300s", err, stdout)
	}
	if !strings.HasSuffix(stdout, "}\n") || strings.Count(stdout, "\n") != 1 {
		t.Error("stdout is not exactly one JSON document and a newline")
	}
	if decoded.Repository != "acme/demo" || len(decoded.Diff) == 0 || decoded.PullRequest.MergeBaseSHA != repo.MergeBase {
		t.Errorf("bundle = %q, %d hunks, merge base %s", decoded.Repository, len(decoded.Diff), decoded.PullRequest.MergeBaseSHA)
	}
	if strings.Contains(stdout, assembletest.PlantedSecret) {
		t.Error("the planted secret is in the bundle")
	}
	for _, want := range []string{"assembling the bundle for acme/demo#7", "bundle digest sha256:", "masked span"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, `"repository"`) {
		t.Error("the bundle was written to stderr")
	}
	if len(fake.resolved) != 1 || fake.resolved[0].PR != 7 || fake.resolved[0].Repo != "" {
		t.Errorf("resolve options = %+v", fake.resolved)
	}
	if after := repo.State(t); after != before {
		t.Errorf("the clone changed:\n%s\n--- after ---\n%s", before, after)
	}
}

func TestBundleOutputFile(t *testing.T) {
	_, _, stores := bundleEnv(t)
	stdout, _, err := run(t, append([]string{"internal", "bundle", "--pr", "7"}, stores...)...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	for _, flag := range []string{"-o", "--output"} {
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, stderr, err := run(t, append([]string{"internal", "bundle", "--pr", "7", flag, path}, stores...)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", flag, err, stderr)
		}
		if out != "" {
			t.Errorf("%s: stdout = %.100q, want nothing", flag, out)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != stdout {
			t.Errorf("%s: file differs from the stdout bundle: %v", flag, err)
		}
		if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode = %v, want 0600", flag, info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

func TestBundleFlagsReachTheResolver(t *testing.T) {
	_, fake, stores := bundleEnv(t)
	if _, stderr, err := run(t, append([]string{"internal", "bundle", "--repo", "acme/demo"}, stores...)...); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if len(fake.resolved) != 1 || fake.resolved[0].PR != 0 || fake.resolved[0].Repo != "acme/demo" {
		t.Errorf("resolve options = %+v", fake.resolved)
	}
	if len(fake.Fetched) != 1 || fake.Fetched[0].Number != assembletest.Number {
		t.Errorf("fetched = %+v", fake.Fetched)
	}
}

func TestBundleErrors(t *testing.T) {
	missing := strings.Repeat("cd", 20)
	for name, tc := range map[string]struct {
		args    []string
		setup   func(*testing.T, *fakeGitHub)
		want    string
		notRepo bool
	}{
		"negative pr":    {args: []string{"--pr", "-1"}, want: "--pr must be a pull request number"},
		"extra argument": {args: []string{"7"}, want: "unknown command"},
		"no token": {args: []string{"--pr", "7"}, setup: func(_ *testing.T, f *fakeGitHub) {
			f.resolveErr = fmt.Errorf("%w for github.com: set GITHUB_TOKEN", config.ErrNoGitHubToken)
		}, want: "no GitHub token"},
		"no remote":       {args: nil, setup: func(_ *testing.T, f *fakeGitHub) { f.resolveErr = github.ErrNoRemote }, want: "pass --repo owner/name"},
		"no pull request": {args: nil, setup: func(_ *testing.T, f *fakeGitHub) { f.resolveErr = github.ErrNoPullRequest }, want: "no pull request found for the current branch"},
		"bad repo":        {args: []string{"--pr", "7", "--repo", "not a repo"}, want: "--repo must be owner/name"},
		"not found": {args: []string{"--pr", "7"}, setup: func(_ *testing.T, f *fakeGitHub) {
			f.Err = fmt.Errorf("%w: pull request acme/demo#7", github.ErrNotFound)
		},
			want: "pull request acme/demo#7 was not found"},
		"missing head": {args: []string{"--pr", "7"}, setup: func(_ *testing.T, f *fakeGitHub) { f.Evidence.PullRequest.HeadSHA = missing },
			want: "git fetch <remote for acme/demo> pull/7/head"},
		"missing base": {args: []string{"--pr", "7"}, setup: func(_ *testing.T, f *fakeGitHub) { f.Evidence.PullRequest.BaseSHA = missing },
			want: "git fetch <remote for acme/demo> main"},
		"not a clone": {args: []string{"--pr", "7"}, notRepo: true, want: "inside a git clone"},
	} {
		t.Run(name, func(t *testing.T) {
			repo, fake, stores := bundleEnv(t)
			if tc.notRepo {
				t.Chdir(t.TempDir())
			}
			if tc.setup != nil {
				tc.setup(t, fake)
			}
			before := repo.State(t)
			stdout, stderr, err := run(t, append(append([]string{"internal", "bundle"}, tc.args...), stores...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q\nstderr: %s", err, tc.want, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %.100q on failure", stdout)
			}
			if after := repo.State(t); after != before {
				t.Errorf("the clone changed on failure:\n%s\n--- after ---\n%s", before, after)
			}
		})
	}
}

func TestBundleFailureWritesNoOutputFile(t *testing.T) {
	_, fake, stores := bundleEnv(t)
	fake.Err = github.ErrNotFound
	path := filepath.Join(t.TempDir(), "bundle.json")
	if _, _, err := run(t, append([]string{"internal", "bundle", "--pr", "7", "-o", path}, stores...)...); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an output file exists after a failed run: %v", err)
	}
	// A directory that does not exist is an error, and nothing reaches stdout.
	fake.Err = nil
	stdout, _, err := run(t, append([]string{"internal", "bundle", "--pr", "7", "-o", filepath.Join(t.TempDir(), "absent", "b.json")}, stores...)...)
	if err == nil || !strings.Contains(err.Error(), "writing the bundle") || stdout != "" {
		t.Errorf("err = %v, stdout = %.50q", err, stdout)
	}
}

// Ctrl-C (here, a cancelled context) stops the run with the cancellation, and
// leaves neither an output file nor a temporary one.
func TestBundleCancellationCleansUp(t *testing.T) {
	repo, fake, stores := bundleEnv(t)
	cancelled := false
	old := bundleContext
	bundleContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		fake.onFetch = func() { cancelled = true; cancel() }
		return ctx, cancel
	}
	t.Cleanup(func() { bundleContext = old })
	before := repo.State(t)
	dir := t.TempDir()
	stdout, _, err := run(t, append([]string{"internal", "bundle", "--pr", "7", "-o", filepath.Join(dir, "bundle.json")}, stores...)...)
	if !cancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v, err = %v", cancelled, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 || stdout != "" {
		t.Errorf("a failed run left %v, stdout %.50q", entries, stdout)
	}
	if after := repo.State(t); after != before {
		t.Errorf("the clone changed:\n%s\n--- after ---\n%s", before, after)
	}
}

func TestBundleRejectsAnUnknownCap(t *testing.T) {
	_, _, stores := bundleEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "baum")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[caps]\nmax_sessoins = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, append([]string{"internal", "bundle", "--pr", "7"}, stores...)...)
	if err == nil || !strings.Contains(err.Error(), `unknown cap "max_sessoins"`) {
		t.Errorf("err = %v", err)
	}
}

func TestBundleHonoursConfiguredCaps(t *testing.T) {
	_, _, stores := bundleEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "baum")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[caps]\nmax_comments = 1\nmax_hunk_patch_lines = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := run(t, append([]string{"internal", "bundle", "--pr", "7"}, stores...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var decoded bundle.Bundle
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Truncated) == 0 || !strings.Contains(strings.Join(decoded.Truncated, "\n"), "1-comment limit") {
		t.Errorf("truncated = %v", decoded.Truncated)
	}
}

func TestBundleHelp(t *testing.T) {
	stdout, _, err := run(t, "internal", "bundle", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--pr", "--repo", "--output", "does not fetch"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help lacks %q:\n%s", want, stdout)
		}
	}
	for _, hidden := range []string{"--claude-dir", "--codex-dir", "--cursor-store"} {
		if strings.Contains(stdout, hidden) {
			t.Errorf("help shows %s", hidden)
		}
	}
}
