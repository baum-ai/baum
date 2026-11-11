package repos

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
)

func gitProcesses() int64 { return workstats.Take().Counters["git.processes"] }

func TestListReusesProbesWhileNothingChanged(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/owner/probe")
	c, err := r.Add(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := func() Clone {
		t.Helper()
		clones, err := r.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, clone := range clones {
			if clone.ID == c.ID {
				return clone
			}
		}
		t.Fatalf("clone %s not listed", c.ID)
		return Clone{}
	}
	if !list().Available {
		t.Fatal("a fresh clone is not available")
	}
	before := gitProcesses()
	for range 5 {
		if !list().Available {
			t.Fatal("an unchanged clone became unavailable")
		}
	}
	if ran := gitProcesses() - before; ran != 0 {
		t.Errorf("listing an unchanged clone 5 times ran git %d times, want 0", ran)
	}

	// A remote change rewrites the config the stamp covers.
	git(t, p, "remote", "set-url", "origin", "https://github.com/owner/elsewhere")
	if list().Available {
		t.Error("a clone whose remote no longer names its repository is still available")
	}
	git(t, p, "remote", "set-url", "origin", "https://github.com/owner/probe")
	if !list().Available {
		t.Error("a clone whose remote names its repository again is not available")
	}

	// A clone that is gone is unavailable without running git.
	if err := os.Rename(filepath.Join(p, ".git"), filepath.Join(p, "moved.git")); err != nil {
		t.Fatal(err)
	}
	before = gitProcesses()
	if list().Available {
		t.Error("a clone without its .git is available")
	}
	if ran := gitProcesses() - before; ran != 0 {
		t.Errorf("a missing .git ran git %d times", ran)
	}
}

func TestProbeCacheExpires(t *testing.T) {
	ctx := context.Background()
	p := clone(t, "https://github.com/owner/expire")
	c, err := inspect(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	cache := &probeCache{entries: map[string]probeEntry{}}
	if !cache.available(ctx, c) {
		t.Fatal("not available")
	}
	for key, entry := range cache.entries {
		entry.at = entry.at.Add(-ProbeMaxAge - time.Second)
		cache.entries[key] = entry
	}
	before := gitProcesses()
	if !cache.available(ctx, c) {
		t.Fatal("not available after expiry")
	}
	if gitProcesses() == before {
		t.Error("an expired answer was reused without running git")
	}
}

func TestProbeIgnoresDirectoryMtimes(t *testing.T) {
	ctx := context.Background()
	p := clone(t, "https://github.com/owner/mtimes")
	c, err := inspect(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	cache := &probeCache{entries: map[string]probeEntry{}}
	if !cache.available(ctx, c) {
		t.Fatal("not available")
	}
	before := gitProcesses()
	for i := range 3 {
		future := time.Now().Add(time.Duration(i+1) * time.Hour)
		for _, dir := range []string{p, filepath.Join(p, ".git")} {
			if err := os.Chtimes(dir, future, future); err != nil {
				t.Fatal(err)
			}
		}
		lock := filepath.Join(p, ".git", "index.lock")
		if err := os.WriteFile(lock, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		if !cache.available(ctx, c) {
			t.Fatal("not available after churn")
		}
	}
	if ran := gitProcesses() - before; ran != 0 {
		t.Errorf("directory mtime churn ran git %d times, want 0", ran)
	}
	git(t, p, "remote", "set-url", "origin", "https://github.com/owner/elsewhere")
	if cache.available(ctx, c) {
		t.Error("a changed remote was not re-probed")
	}
}

func TestProbeSharesConcurrentMisses(t *testing.T) {
	ctx := context.Background()
	p := clone(t, "https://github.com/owner/shared")
	c, err := inspect(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	cache := &probeCache{entries: map[string]probeEntry{}}
	before := gitProcesses()
	if !cache.available(ctx, c) {
		t.Fatal("not available")
	}
	single := gitProcesses() - before
	cache.mu.Lock()
	cache.entries = map[string]probeEntry{}
	cache.mu.Unlock()

	before = gitProcesses()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if !cache.available(ctx, c) {
				t.Error("not available")
			}
		}()
	}
	close(start)
	wg.Wait()
	if ran := gitProcesses() - before; ran > single {
		t.Errorf("16 concurrent misses ran git %d times, one probe runs it %d", ran, single)
	}
}
