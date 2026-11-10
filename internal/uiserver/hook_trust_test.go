package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentplugins"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

func TestCodexHookTrustRouteForcedReadAndHandlesUnavailableProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	state := "untrusted"
	api := LocalAPI{Registry: registry, HookTrust: func(ctx context.Context) agentplugins.HookTrust {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("query has no deadline")
		}
		return agentplugins.HookTrust{State: state}
	}}
	handler := api.Handler()
	defer handler.Close()
	for _, want := range []string{"untrusted", "trusted", "unknown"} {
		state = want
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/agent-hooks/codex?refresh=1", nil))
		var got agentplugins.HookTrust
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.State != want {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
	api.HookTrust = nil
	old := api.Handler()
	defer old.Close()
	w := httptest.NewRecorder()
	old.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/agent-hooks/codex", nil))
	if w.Code != 200 || w.Body.String() != "{\"available\":false}\n" {
		t.Fatalf("unavailable=%d %s", w.Code, w.Body.String())
	}
}

func newTestHookTrust(state *string, calls *int) (*hookTrustCache, *time.Time) {
	clock := time.Unix(1000, 0)
	c := newHookTrustCache(func(context.Context) agentplugins.HookTrust {
		*calls++
		return agentplugins.HookTrust{State: *state}
	})
	c.now = func() time.Time { return clock }
	return c, &clock
}

func TestHookTrustCacheHitAndExpiry(t *testing.T) {
	state, calls := "trusted", 0
	c, clock := newTestHookTrust(&state, &calls)
	c.Get(false)
	state = "untrusted"
	if got := c.Get(false); got.State != "trusted" || calls != 1 {
		t.Fatalf("hit: %v calls=%d", got, calls)
	}
	*clock = clock.Add(hookTrustTTL - time.Second)
	if got := c.Get(false); got.State != "trusted" || calls != 1 {
		t.Fatalf("before expiry: %v calls=%d", got, calls)
	}
	*clock = clock.Add(time.Second)
	if got := c.Get(false); got.State != "untrusted" || calls != 2 {
		t.Fatalf("after expiry: %v calls=%d", got, calls)
	}
}

func TestHookTrustCacheForcedRefresh(t *testing.T) {
	state, calls := "trusted", 0
	c, _ := newTestHookTrust(&state, &calls)
	c.Get(false)
	state = "not_installed"
	if got := c.Get(true); got.State != "not_installed" || calls != 2 {
		t.Fatalf("refresh: %v calls=%d", got, calls)
	}
	if got := c.Get(false); got.State != "not_installed" || calls != 2 {
		t.Fatalf("refresh result is not cached: %v calls=%d", got, calls)
	}
}

func TestHookTrustCacheKeepsOnlyHealthyAnswers(t *testing.T) {
	for _, warn := range []string{"unknown", "untrusted", "hooks_disabled", "disabled", "missing"} {
		state, calls := warn, 0
		c, _ := newTestHookTrust(&state, &calls)
		c.Get(false)
		state = "trusted"
		if got := c.Get(false); got.State != "trusted" || calls != 2 {
			t.Fatalf("%s was cached: %v calls=%d", warn, got, calls)
		}
		state = "untrusted"
		if got := c.Get(false); got.State != "trusted" || calls != 2 {
			t.Fatalf("trusted was not cached: %v calls=%d", got, calls)
		}
	}
}

func TestHookTrustCacheFailedRefreshDropsHealthyEntry(t *testing.T) {
	for _, bad := range []string{"unknown", "untrusted"} {
		state, calls := "trusted", 0
		c, _ := newTestHookTrust(&state, &calls)
		c.Get(false)
		state = bad
		if got := c.Get(true); got.State != bad || calls != 2 {
			t.Fatalf("%s refresh: %v calls=%d", bad, got, calls)
		}
		if got := c.Get(false); got.State != bad || calls != 3 {
			t.Fatalf("%s: old healthy answer served: %v calls=%d", bad, got, calls)
		}
	}
}

func TestHookTrustCacheOlderReadCannotOverwriteNewer(t *testing.T) {
	state, calls := "untrusted", 0
	c, _ := newTestHookTrust(&state, &calls)
	older, newer := c.begin(), c.begin()
	c.finish(newer, agentplugins.HookTrust{State: "untrusted"})
	c.finish(older, agentplugins.HookTrust{State: "trusted"})
	state = "untrusted"
	if got := c.Get(false); got.State != "untrusted" || calls != 1 {
		t.Fatalf("older healthy read was stored: %v calls=%d", got, calls)
	}
}

func TestHookTrustCacheSharesConcurrentReads(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	c := newHookTrustCache(func(context.Context) agentplugins.HookTrust {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return agentplugins.HookTrust{State: "trusted"}
	})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); c.Get(true) }()
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
