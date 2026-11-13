//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/repos"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// repository is a remote with a main branch, a feature branch that pull
// request 7 comes from and a fork's head as refs/pull/9/head, and a clone of
// it on main.
type repository struct {
	remote, clone, head, fork string
}

func newRepository(t *testing.T) repository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(name, "Test")
	}
	for _, name := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "test@example.com")
	}
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	remote := filepath.Join(base, "remote.git")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(base, "seed")
	git(t, base, "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "initial")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, seed, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "commit", "-q", "-am", "feature")
	head := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "-q", "origin", "feature")
	git(t, seed, "checkout", "-q", "-b", "elsewhere", "main")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "commit", "-q", "-am", "fork")
	fork := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "-q", "origin", "elsewhere:refs/pull/9/head")
	git(t, remote, "update-ref", "refs/pull/7/head", head)
	git(t, remote, "update-ref", "refs/pull/10/head", head)
	clone := filepath.Join(base, "clone")
	git(t, base, "clone", "-q", remote, clone)
	return repository{remote: remote, clone: clone, head: head, fork: fork}
}

// pullFixture is a service whose registry has the repository's clone and
// whose discovery knows pull requests 7 (feature) and 9 (a fork's main).
func pullFixture(t *testing.T, r repository, extra ...repos.Clone) *fixture {
	t.Helper()
	return newFixture(t, func(o *Options) {
		o.Clones = func(context.Context) ([]repos.Clone, error) {
			return append([]repos.Clone{{ID: "c1", Path: r.clone, Repository: "owner/repo", Available: true}}, extra...), nil
		}
		o.Pull = func(_ context.Context, repository string, number int) (Pull, error) {
			switch {
			case !strings.EqualFold(repository, "owner/repo"):
			case number == 7:
				return Pull{HeadRef: "feature", HeadSHA: r.head}, nil
			case number == 9:
				return Pull{HeadRef: "main", HeadSHA: r.fork, Fork: true}, nil
			case number == 10:
				// Its head branch was deleted; refs/pull/10/head remains.
				return Pull{HeadRef: "deleted", HeadSHA: r.head}, nil
			case number == 11:
				// Neither its head branch nor its pull request ref exists.
				return Pull{HeadRef: "nope", HeadSHA: r.head}, nil
			}
			return Pull{}, errors.New("unknown pull request")
		}
	})
}

func pullPage(number int) *agentcontext.PageContext {
	return &agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "Owner/Repo", PullRequest: number}
}

// A worktree that already has the head branch checked out is used as it is.
func TestDirectoryIsAnExistingWorktree(t *testing.T) {
	r := newRepository(t)
	existing := filepath.Join(filepath.Dir(r.clone), "feature-worktree")
	git(t, r.clone, "worktree", "add", "-q", existing, "feature")
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Directory != existing || c.CreatedWorktree || c.CreatedBranch || c.Branch != "feature" || c.Repository != "Owner/Repo" || c.PullRequest != 7 {
		t.Fatalf("created %+v", c)
	}
}

// The main checkout of another clone of the repository counts too.
func TestDirectoryIsAnotherClonesMainCheckout(t *testing.T) {
	r := newRepository(t)
	other := filepath.Join(filepath.Dir(r.clone), "other")
	git(t, filepath.Dir(r.clone), "clone", "-q", "-b", "feature", r.remote, other)
	f := pullFixture(t, r, repos.Clone{ID: "c2", Path: other, Repository: "owner/repo", Available: true},
		repos.Clone{ID: "c3", Path: "/nonexistent", Repository: "owner/repo", Available: false})
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Directory != other || c.CreatedWorktree || c.Branch != "feature" {
		t.Fatalf("created %+v", c)
	}
}

// Without one, baum adds a worktree under its own directory on the head
// branch, and the clone's checkout stays where it was.
func TestDirectoryIsANewWorktree(t *testing.T) {
	r := newRepository(t)
	before := git(t, r.clone, "rev-parse", "HEAD")
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Directory != filepath.Join(f.opts.Root, "worktrees", c.ID, "Repo") || !c.CreatedWorktree || !c.CreatedBranch || c.Branch != "feature" || c.DirectoryNote != "" {
		t.Fatalf("created %+v", c)
	}
	if got := git(t, c.Directory, "rev-parse", "HEAD"); got != r.head {
		t.Fatalf("the worktree is at %s, want %s", got, r.head)
	}
	if got := git(t, r.clone, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the clone's HEAD moved to %s", got)
	}
	if got := git(t, r.clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the clone's branch is %s", got)
	}
	// The next conversation on the pull request finds that worktree.
	again, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentCodex, Page: pullPage(7)})
	if err != nil {
		t.Fatal(err)
	}
	if again.Directory != c.Directory || again.CreatedWorktree {
		t.Fatalf("again %+v", again)
	}
}

// A fork's head is checked out on baum's branch name, not the fork's branch
// name, which here is the base's.
func TestDirectoryForAForkPullRequest(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(9)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Directory == r.clone || !c.CreatedWorktree || c.Branch != "baum/pr-9" {
		t.Fatalf("created %+v", c)
	}
	if got := git(t, c.Directory, "rev-parse", "HEAD"); got != r.fork {
		t.Fatalf("the worktree is at %s, want %s", got, r.fork)
	}
	if got := git(t, r.clone, "config", "branch.baum/pr-9.merge"); got != "refs/pull/9/head" {
		t.Fatalf("upstream %s", got)
	}
}

// Any other page, a pull request discovery does not know and a repository
// with no clone get a new empty directory.
func TestDirectoryIsNeutralElsewhere(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	for name, page := range map[string]*agentcontext.PageContext{
		"no page":       nil,
		"another page":  {Page: agentcontext.PageSessions},
		"unknown pull":  pullPage(8),
		"another repo":  {Page: agentcontext.PagePullRequest, Repository: "owner/other", PullRequest: 7},
		"a repo's page": {Page: agentcontext.PageRepositories, Repository: "owner/repo"},
	} {
		c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: page})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Directory != filepath.Join(f.opts.Root, "sessions", c.ID) || c.Repository != "" || c.CreatedWorktree {
			t.Fatalf("%s: %+v", name, c)
		}
		info, err := os.Stat(c.Directory)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: directory %v %v", name, info, err)
		}
	}
}
