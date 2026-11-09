package livesessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/workstats"
)

func gitProcesses() int64 { return workstats.Take().Counters["git.processes"] }

func TestResolverRunsGitOnlyWhenTheCheckoutChanged(t *testing.T) {
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
	git(t, clone, "checkout", "-q", "-b", "feature")
	prs := &cache{prs: []discovery.PullRequest{{Number: 7, HeadRef: "feature"}}}
	now := time.Now()
	r := &Resolver{
		Registry:  registry{[]repos.Clone{{ID: "c", Path: clone, CommonDir: filepath.Join(clone, ".git"), Repository: "acme/demo", Available: true}}},
		Discovery: prs,
		Now:       func() time.Time { return now },
	}
	ctx := context.Background()
	if got, ok := r.Link(ctx, clone); !ok || got.PR != 7 {
		t.Fatalf("first link = %+v %v", got, ok)
	}

	// Nothing changed: no git, and a pull request opened since still links.
	prs.prs = append(prs.prs, discovery.PullRequest{Number: 8, HeadRef: "other"})
	before := gitProcesses()
	now = now.Add(time.Minute)
	if got, ok := r.Link(ctx, clone); !ok || got.PR != 7 || got.Branch != "feature" {
		t.Errorf("unchanged checkout = %+v %v", got, ok)
	}
	if ran := gitProcesses() - before; ran != 0 {
		t.Errorf("an unchanged checkout ran git %d times", ran)
	}

	// A branch switch is seen.
	git(t, clone, "checkout", "-q", "-b", "other")
	now = now.Add(time.Minute)
	if got, ok := r.Link(ctx, clone); !ok || got.Branch != "other" || got.PR != 8 {
		t.Errorf("after a checkout = %+v %v", got, ok)
	}

	// The reuse ends after workedFor even when nothing changed.
	before = gitProcesses()
	now = now.Add(workedFor)
	r.Link(ctx, clone)
	if gitProcesses() == before {
		t.Error("a reuse older than workedFor ran no git")
	}
}

func TestWatchSlowsDownWithoutRunningSessions(t *testing.T) {
	tracker := &Tracker{}
	transcripts := &Transcripts{Tracker: tracker}
	if got := transcripts.recheckEvery(); got != QuietRecheckEvery {
		t.Errorf("no sessions: %v, want %v", got, QuietRecheckEvery)
	}
}
