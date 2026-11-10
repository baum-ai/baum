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
// one run between concurrent callers. Only an answer without a warning is kept:
// the app's warning clears as soon as the user trusts hooks in Codex, so reads
// while a warning shows always run fresh. A healthy answer that changes outside
// the daemon is seen when the entry expires or on a forced read.
type hookTrustCache struct {
	read func(context.Context) agentplugins.HookTrust
	now  func() time.Time

	mu      sync.Mutex
	value   agentplugins.HookTrust
	expires time.Time
	flight  singleflight.Group
}

func newHookTrustCache(read func(context.Context) agentplugins.HookTrust) *hookTrustCache {
	return &hookTrustCache{read: read, now: time.Now}
}

// Get returns the cached answer, or runs the read when there is none or
// refresh is set. Only trusted and not_installed answers (the states the app
// shows no warning for) are kept.
func (c *hookTrustCache) Get(refresh bool) agentplugins.HookTrust {
	c.mu.Lock()
	if !refresh && c.now().Before(c.expires) {
		v := c.value
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	res, _, _ := c.flight.Do("codex", func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*agentplugins.StatusTimeout)
		defer cancel()
		v := c.read(ctx)
		c.mu.Lock()
		if v.State == "trusted" || v.State == "not_installed" {
			c.value, c.expires = v, c.now().Add(hookTrustTTL)
		}
		c.mu.Unlock()
		return v, nil
	})
	return res.(agentplugins.HookTrust)
}
