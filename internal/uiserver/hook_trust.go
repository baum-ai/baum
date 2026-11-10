package uiserver

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/agentplugins"
)

// hookTrustTTL bounds how long a Codex hook answer is reused. Each fresh read
// starts the Codex CLI and its app-server.
const hookTrustTTL = 5 * time.Minute

// hookTrustCache reuses the last Codex hook answer for hookTrustTTL and shares
// one run between concurrent callers. Trust changes made outside the daemon
// (Codex's /hooks, the desktop app's install actions) are not seen until the
// entry expires, so callers that just acted ask for a fresh read.
type hookTrustCache struct {
	read func(context.Context) agentplugins.HookTrust
	now  func() time.Time

	mu      sync.Mutex
	value   agentplugins.HookTrust
	expires time.Time
	epoch   int
	flight  singleflight.Group
}

func newHookTrustCache(read func(context.Context) agentplugins.HookTrust) *hookTrustCache {
	return &hookTrustCache{read: read, now: time.Now}
}

// Invalidate drops the cached answer, including one a running read is about to
// store.
func (c *hookTrustCache) Invalidate() {
	c.mu.Lock()
	c.expires = time.Time{}
	c.epoch++
	c.mu.Unlock()
}

// Get returns the cached answer, or runs the read when there is none or
// refresh is set. An unknown answer means the read failed and is not kept.
func (c *hookTrustCache) Get(refresh bool) agentplugins.HookTrust {
	c.mu.Lock()
	if !refresh && c.now().Before(c.expires) {
		v := c.value
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	res, _, _ := c.flight.Do("codex", func() (any, error) {
		c.mu.Lock()
		epoch := c.epoch
		c.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*agentplugins.StatusTimeout)
		defer cancel()
		v := c.read(ctx)
		c.mu.Lock()
		if v.State != "unknown" && epoch == c.epoch {
			c.value, c.expires = v, c.now().Add(hookTrustTTL)
		}
		c.mu.Unlock()
		return v, nil
	})
	return res.(agentplugins.HookTrust)
}
