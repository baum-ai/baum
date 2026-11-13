package clonefind

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
)

func TestForkCloneRegisteredToTheForkIsNeverMoved(t *testing.T) {
	ctx := context.Background()
	h := home(t)
	fork := repo(t, filepath.Join(h, "code", "fork"), "origin", gh("me/app"), "upstream", gh("acme/app"))
	f := start(t, h)
	f.service.Interval = 20 * time.Millisecond
	if _, err := f.registry.Add(ctx, fork, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Pick(ctx, "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.scan(t)
	f.events <- agentEvent(fork, time.Now())
	f.service.Trigger()
	time.Sleep(400 * time.Millisecond) // several ticks and events

	got := f.clones(t)
	if len(got) != 1 || got[0].Repository != "me/app" || got[0].Path != fork || !got[0].Available {
		t.Fatalf("clones = %+v", got)
	}
	if n := f.changes.Load(); n != 0 {
		t.Fatalf("the app was told %d times", n)
	}
	// The registry itself refuses to move a registered clone.
	if _, err := f.registry.AttachFound(ctx, fork, "acme/app"); err == nil {
		t.Fatal("AttachFound moved a registered clone")
	}
}

// countingRegistry is a registry in which every picked repository has a clone,
// and which counts what it is asked.
type countingRegistry struct {
	repositories, withoutClones atomic.Int32
}

func (c *countingRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	c.repositories.Add(1)
	return nil, nil
}
func (c *countingRegistry) PickedWithoutClones(context.Context) ([]string, error) {
	c.withoutClones.Add(1)
	return nil, nil
}
func (c *countingRegistry) CloneRows(context.Context) ([]repos.Clone, error) { return nil, nil }
func (c *countingRegistry) AttachFound(context.Context, string, string) (repos.Clone, error) {
	panic("nothing needs a clone")
}

func TestEventsCostNoGitWhileEveryPickedRepositoryHasAClone(t *testing.T) {
	h := home(t)
	var probes atomic.Int32
	registry := &countingRegistry{}
	o := options(h)
	o.Probe = func(ctx context.Context, dir string) (gitlocal.Light, error) {
		probes.Add(1)
		return gitlocal.Light{}, nil
	}
	s := &Service{Registry: registry, Options: o}
	for i := range 20 {
		dir := repo(t, filepath.Join(h, "elsewhere", string(rune('a'+i))), "origin", gh("acme/app"))
		event := agentEvent(dir, time.Now())
		s.handleEvent(context.Background(), event)
		s.handleEvent(context.Background(), event) // the same directory again
	}
	if registry.repositories.Load() != 0 || probes.Load() != 0 {
		t.Fatalf("Repositories calls = %d, git probes = %d", registry.repositories.Load(), probes.Load())
	}
	// One cheap query per new directory; a directory seen before costs none.
	if n := registry.withoutClones.Load(); n != 20 {
		t.Fatalf("registry queries = %d", n)
	}
}

// staleRegistry reports a repository as picked and without a clone, as the
// registry did just before the user unpicked it.
type staleRegistry struct{ *repos.Registry }

func (staleRegistry) PickedWithoutClones(context.Context) ([]string, error) {
	return []string{"acme/app"}, nil
}
func (staleRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return []repos.Repository{{Repository: "acme/app"}}, nil
}

func TestAnUnpickedRepositoryIsNotPickedAgainByAnAttach(t *testing.T) {
	ctx := context.Background()
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	f := start(t, h)
	f.service.Registry = staleRegistry{f.registry}
	f.service.scan(ctx, false) // finds the clone, tries to attach it
	f.service.handleEvent(ctx, agentEvent(clone, time.Now()))
	picked, err := f.registry.Repositories(ctx)
	if err != nil || len(picked) != 0 || len(f.clones(t)) != 0 || f.changes.Load() != 0 {
		t.Fatalf("picked = %+v, %v, clones = %+v, changes = %d", picked, err, f.clones(t), f.changes.Load())
	}
}

func TestEventInAnAgentWorktreeAttachesTheMainClone(t *testing.T) {
	ctx := context.Background()
	h := home(t)
	main := repo(t, filepath.Join(h, "elsewhere", "app"), "origin", gh("acme/app"))
	git(t, main, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(main, ".claude", "worktrees", "x")
	git(t, main, "worktree", "add", "-q", "-b", "x", linked)
	f := start(t, h)
	if _, err := f.registry.Pick(ctx, "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.service.handleEvent(ctx, agentEvent(linked, time.Now()))
	got := f.clones(t)
	if len(got) != 1 || got[0].Path != main || f.changes.Load() != 1 {
		t.Fatalf("clones = %+v, changes = %d", got, f.changes.Load())
	}
}
