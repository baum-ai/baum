package livesessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
)

type registry struct{ clones []repos.Clone }

func (r registry) List(context.Context) ([]repos.Clone, error) { return r.clones, nil }

type cache struct {
	prs   []discovery.PullRequest
	calls int
}

func (c *cache) Cached(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	c.calls++
	return discovery.Snapshot{Repository: repo.String(), PullRequests: c.prs}, nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestResolverLinksDirectoriesOfRegisteredClones(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(clone, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q", "-b", "main")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, clone, "checkout", "-q", "-b", "feature")
	worktree := filepath.Join(root, "demo-wt")
	git(t, clone, "worktree", "add", "-q", "-b", "other", worktree)
	stranger := filepath.Join(root, "stranger")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, stranger, "init", "-q")

	prs := &cache{prs: []discovery.PullRequest{
		{Number: 7, HeadRef: "feature"},
		{Number: 9, HeadRef: "other", Fork: true},
		{Number: 3, HeadRef: "main", Fork: true},
	}}
	now := time.Now()
	r := &Resolver{
		Registry:  registry{[]repos.Clone{{ID: "c", Path: clone, CommonDir: filepath.Join(clone, ".git"), Repository: "Acme/Demo", Available: true}}},
		Discovery: prs,
		Now:       func() time.Time { return now },
	}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		cwd  string
		want Link
	}{
		"top":          {clone, Link{Repository: "acme/demo", Branch: "feature", PR: 7}},
		"subdirectory": {filepath.Join(clone, "pkg"), Link{Repository: "acme/demo", Branch: "feature", PR: 7}},
		// A linked worktree is the clone; only a head in the repository
		// itself is a pull request, not a fork's branch of the same name.
		"worktree": {worktree, Link{Repository: "acme/demo", Branch: "other"}},
	} {
		got, ok := r.Link(ctx, tc.cwd)
		if !ok || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, tc.want)
		}
	}
	git(t, clone, "checkout", "-q", "--detach")
	now = now.Add(time.Minute)
	if got, ok := r.Link(ctx, clone); !ok || got.Branch != "" || got.PR != 0 {
		t.Errorf("detached: %+v %v", got, ok)
	}
	for name, cwd := range map[string]string{"unregistered clone": stranger, "relative": "demo", "missing": filepath.Join(root, "missing")} {
		if got, ok := r.Link(ctx, cwd); ok {
			t.Errorf("%s linked: %+v", name, got)
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestResolverFindsPullRequestsByUpstreamAndByCommit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "demo")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q", "-b", "main")
	git(t, clone, "remote", "add", "origin", "https://github.com/acme/demo")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, clone, "checkout", "-q", "-b", "local")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "second")
	head := gitOut(t, clone, "rev-parse", "HEAD")
	prs := &cache{prs: []discovery.PullRequest{
		{Number: 5, HeadRef: "pushed-as", HeadSHA: "0000"},
		{Number: 6, HeadRef: "elsewhere", HeadSHA: head},
		{Number: 8, HeadRef: "forked", HeadSHA: head, Fork: true},
	}}
	r := &Resolver{
		Registry:  registry{[]repos.Clone{{ID: "c", Path: clone, CommonDir: filepath.Join(clone, ".git"), Repository: "acme/demo", Available: true}}},
		Discovery: prs,
	}
	ctx := context.Background()
	// Pushed as another branch, with no upstream: the commit says.
	if got, ok := r.Link(ctx, clone); !ok || got.Branch != "local" || got.PR != 6 {
		t.Fatalf("by commit: %+v %v", got, ok)
	}
	// An upstream in the repository names the head branch, before the commit.
	git(t, clone, "config", "branch.local.remote", "origin")
	git(t, clone, "config", "branch.local.merge", "refs/heads/pushed-as")
	r.unlinked = nil
	if got, _ := r.Link(ctx, clone); got.PR != 5 {
		t.Fatalf("by upstream: %+v", got)
	}
	// An upstream in another repository is not this repository's branch.
	git(t, clone, "remote", "add", "fork", "https://github.com/someone/demo")
	git(t, clone, "config", "branch.local.remote", "fork")
	if got, _ := r.Link(ctx, clone); got.PR != 6 {
		t.Fatalf("by a fork's upstream: %+v", got)
	}
	// A detached HEAD has no branch; the commit still links.
	git(t, clone, "checkout", "-q", "--detach")
	if got, ok := r.Link(ctx, clone); !ok || got.Branch != "" || got.PR != 6 {
		t.Fatalf("detached: %+v %v", got, ok)
	}
}

type failingRegistry struct{ calls int }

func (f *failingRegistry) List(context.Context) ([]repos.Clone, error) {
	f.calls++
	return nil, errors.New("registry unavailable")
}

func TestResolverBacksOffAFailingRegistry(t *testing.T) {
	now := time.Now()
	reg := &failingRegistry{}
	r := &Resolver{Registry: reg, Now: func() time.Time { return now }}
	for i := range 5 {
		now = now.Add(time.Second + time.Duration(i))
		r.Link(context.Background(), fmt.Sprintf("/work/%d", i))
	}
	if reg.calls != 1 {
		t.Fatalf("listed %d times in 10 seconds", reg.calls)
	}
	now = now.Add(clonesFor)
	r.Link(context.Background(), "/work/later")
	if reg.calls != 2 {
		t.Fatalf("listed %d times after the back-off", reg.calls)
	}
}

func TestResolverSkipsUnavailableClonesAndRemembersMisses(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	now := time.Now()
	reg := registry{[]repos.Clone{{ID: "c", Path: root, CommonDir: filepath.Join(root, ".git"), Repository: "acme/demo"}}}
	r := &Resolver{Registry: reg, Now: func() time.Time { return now }}
	if _, ok := r.Link(context.Background(), root); ok {
		t.Fatal("an unavailable clone linked")
	}
	// The miss is remembered for unlinkedFor, even once the clone is available.
	reg.clones[0].Available = true
	r.clones = reg.clones
	if _, ok := r.Link(context.Background(), root); ok {
		t.Fatal("a remembered miss was looked up again")
	}
	now = now.Add(unlinkedFor)
	if got, ok := r.Link(context.Background(), root); !ok || got.Repository != "acme/demo" {
		t.Fatalf("after the miss expired: %+v %v", got, ok)
	}
}

func TestResolverUpstreamComparesTheHost(t *testing.T) {
	for _, tc := range []struct {
		name, repository, same, other string
	}{
		{"github.com", "acme/demo", "https://github.com/acme/demo", "https://ghe.acme.test/acme/demo"},
		{"enterprise", "octo.ghe.com/acme/demo", "git@octo.ghe.com:acme/demo.git", "https://github.com/acme/demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			clone := filepath.Join(root, "demo")
			if err := os.MkdirAll(clone, 0o755); err != nil {
				t.Fatal(err)
			}
			git(t, clone, "init", "-q", "-b", "main")
			git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
			git(t, clone, "checkout", "-q", "-b", "local")
			git(t, clone, "remote", "add", "origin", tc.same)
			git(t, clone, "remote", "add", "other", tc.other)
			git(t, clone, "config", "branch.local.merge", "refs/heads/pushed-as")
			parsed, err := github.ParseRepository(tc.repository)
			if err != nil {
				t.Fatal(err)
			}
			repo, err := gitlocal.Open(context.Background(), clone, gitlocal.Options{CommandTimeout: gitTimeout})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repo.Close() }()
			r := &Resolver{}
			git(t, clone, "config", "branch.local.remote", "origin")
			if got := r.upstreamBranch(context.Background(), repo, parsed, "local"); got != "pushed-as" {
				t.Fatalf("same host: %q", got)
			}
			git(t, clone, "config", "branch.local.remote", "other")
			if got := r.upstreamBranch(context.Background(), repo, parsed, "local"); got != "" {
				t.Fatalf("other host: %q", got)
			}
		})
	}
}
