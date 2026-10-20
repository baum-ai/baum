package livesessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
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
