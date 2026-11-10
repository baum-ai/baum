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
	"github.com/baum-ai/baum/internal/prlink"
	"github.com/baum-ai/baum/internal/repos"
)

type registry struct{ clones []repos.Clone }

func (r registry) List(context.Context) ([]repos.Clone, error) { return r.clones, nil }

type cache struct {
	prs          []discovery.PullRequest
	observations []discovery.Observation
	// next is the snapshot's NextPoll.
	next  time.Time
	calls int
}

func (c *cache) Cached(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
	c.calls++
	return discovery.Snapshot{Repository: repo.String(), PullRequests: c.prs, Observations: c.observations, NextPoll: c.next}, nil
}

// revisedCache is a cache that stamps its writes, as discovery does.
type revisedCache struct {
	cache
	writes    uint64
	revisions int
}

func (c *revisedCache) Revision(context.Context, github.Repository) (discovery.Revision, error) {
	c.revisions++
	return discovery.Revision{Writes: c.writes}, nil
}

// write changes the cached pull requests as a poll does.
func (c *revisedCache) write(prs ...discovery.PullRequest) {
	c.prs = prs
	c.writes++
}

// The resolver reads a cache again only once discovery wrote it, and calls it
// stale, as Cached would, once its next poll is due though nothing wrote it.
func TestResolverReadsTheCacheOnlyAfterAWrite(t *testing.T) {
	c := &clock{t0}
	cached := &revisedCache{cache: cache{prs: []discovery.PullRequest{{Number: 7, HeadRef: "feature"}}, next: t0.Add(time.Minute)}}
	r := &Resolver{Discovery: cached, Now: c.Now}
	ctx := context.Background()
	work := prlink.Work{Repository: "acme/demo", Branch: "feature"}
	first, ok := r.Generation(ctx, "acme/demo")
	for range 3 {
		if link, _ := r.resolve(ctx, "acme/demo", work); link.PR != 7 || link.Status != "resolved" {
			t.Fatalf("link %+v", link)
		}
	}
	if again, _ := r.Generation(ctx, "acme/demo"); !ok || again != first || cached.calls != 1 {
		t.Fatalf("generation %+v then %+v, %d reads", first, again, cached.calls)
	}
	cached.write(discovery.PullRequest{Number: 8, HeadRef: "feature"})
	if link, _ := r.resolve(ctx, "acme/demo", work); link.PR != 8 || cached.calls != 2 {
		t.Fatalf("after a write: %+v, %d reads", link, cached.calls)
	}
	written, _ := r.Generation(ctx, "acme/demo")
	c.advance(time.Minute)
	if link, _ := r.resolve(ctx, "acme/demo", work); link.PR != 8 || link.Status != "stale" || cached.calls != 2 {
		t.Fatalf("once the poll is due: %+v, %d reads", link, cached.calls)
	}
	if due, _ := r.Generation(ctx, "acme/demo"); due == written {
		t.Fatal("a due poll kept the generation")
	}
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
		if !ok || got.Repository != tc.want.Repository || got.Branch != tc.want.Branch || got.PR != tc.want.PR {
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
	if got, _ := r.Link(ctx, clone); got.PR != 0 || got.Status != "ambiguous" {
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
	root := t.TempDir()
	now := time.Now()
	reg := &failingRegistry{}
	r := &Resolver{Registry: reg, Now: func() time.Time { return now }}
	for i := range 5 {
		now = now.Add(time.Second + time.Duration(i))
		r.Link(context.Background(), filepath.Join(root, fmt.Sprint(i)))
	}
	if reg.calls != 1 {
		t.Fatalf("listed %d times in 10 seconds", reg.calls)
	}
	now = now.Add(clonesFor)
	r.Link(context.Background(), filepath.Join(root, "later"))
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

// A commit links to the branch it was made on, as its hook recorded it, though
// the worktree has checked out another branch since; a push to the branch it
// names, in the repository its remote pushes to.
func TestResolverLinksRecordedCommitsAndPushes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "demo")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q", "-b", "main")
	git(t, clone, "remote", "add", "origin", "https://github.com/acme/demo.git")
	git(t, clone, "remote", "add", "fork", "https://github.com/someone/demo.git")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, clone, "checkout", "-q", "-b", "fix")
	git(t, clone, "checkout", "-q", "main")
	r := &Resolver{
		Registry: registry{[]repos.Clone{{ID: "c", Path: clone, CommonDir: filepath.Join(clone, ".git"), Repository: "acme/demo", Available: true}}},
		Discovery: &cache{prs: []discovery.PullRequest{
			{Number: 12, HeadRef: "fix"},
			{Number: 13, HeadRef: "topic", HeadRepository: "someone/demo", Fork: true},
		}},
	}
	ctx := context.Background()
	head := strings.Repeat("c", 40)
	if link, ok := r.LinkCommit(ctx, clone, "fix", head); !ok || link.PR != 12 || link.Branch != "fix" || link.HeadSHA != head {
		t.Errorf("commit = %+v, %t", link, ok)
	}
	if link, ok := r.LinkPush(ctx, clone, "fork", "topic", head); !ok || link.PR != 13 || link.HeadRepository != "someone/demo" {
		t.Errorf("push to the fork = %+v, %t", link, ok)
	}
	if link, ok := r.LinkPush(ctx, clone, "origin", "topic", head); !ok || link.PR != 0 {
		t.Errorf("push of a fork's branch name to origin = %+v, %t", link, ok)
	}
	if _, ok := r.LinkCommit(ctx, filepath.Join(root, "gone"), "fix", head); ok {
		t.Error("a directory in no clone linked")
	}
}

func TestResolverLinksMergedOrClosedHead(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			cached := &cache{observations: []discovery.Observation{{Number: 1040, HeadSHA: "abc", State: state}}}
			resolver := &Resolver{Discovery: cached}
			work := prlink.Work{Repository: "acme/demo", Branch: "feature", Head: "abc"}
			got, ok := resolver.resolve(context.Background(), "acme/demo", work)
			if !ok || got.PR != 1040 || got.Method != "head" || got.PRState != state {
				t.Fatalf("link %+v, ok %v", got, ok)
			}
			work.Head = "different"
			if got, _ := resolver.resolve(context.Background(), "acme/demo", work); got.PR != 0 {
				t.Fatalf("reused branch linked old PR: %+v", got)
			}
			work.Head, work.Repository = "abc", "other/fork"
			if got, _ := resolver.resolve(context.Background(), "acme/demo", work); got.PR != 0 {
				t.Fatalf("different head repository linked old PR: %+v", got)
			}
		})
	}
}

func TestResolverRefusesAmbiguousClosedHead(t *testing.T) {
	cached := &cache{observations: []discovery.Observation{{Number: 1, HeadSHA: "abc", State: "MERGED"}, {Number: 2, HeadSHA: "abc", State: "CLOSED"}}}
	resolver := &Resolver{Discovery: cached}
	got, _ := resolver.resolve(context.Background(), "acme/demo", prlink.Work{Repository: "acme/demo", Head: "abc"})
	if got.PR != 0 || got.Status != "ambiguous" {
		t.Fatalf("ambiguous head %+v", got)
	}
}

func TestPRStateChangeNotifiesSessionUI(t *testing.T) {
	cached := &cache{prs: []discovery.PullRequest{{Number: 1040, HeadRef: "feature", HeadSHA: "abc"}}}
	resolver := &Resolver{Discovery: cached}
	work := prlink.Work{Repository: "acme/demo", Branch: "feature", Head: "abc"}
	open, _ := resolver.resolve(context.Background(), "acme/demo", work)
	if open.PRState != "OPEN" {
		t.Fatalf("open state %+v", open)
	}
	item := &entry{}
	now := time.Now()
	item.setLink(open, now)
	before := item.Session
	cached.prs = nil
	cached.observations = []discovery.Observation{{Number: 1040, HeadSHA: "abc", State: "MERGED"}}
	merged, _ := resolver.resolve(context.Background(), "acme/demo", work)
	item.setLink(merged, now)
	if item.PR != 1040 || item.PRState != "MERGED" || !significant(before, item.Session, true, true) {
		t.Fatalf("merged state not published: %+v", item.Session)
	}
}
