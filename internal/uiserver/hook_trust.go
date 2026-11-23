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
	started int // reads begun
	applied int // start number of the newest read that updated the entry
	flight  singleflight.Group
}

func newHookTrustCache(read func(context.Context) agentplugins.HookTrust) *hookTrustCache {
	return &hookTrustCache{read: read, now: time.Now}
}

// Get returns the cached answer, or runs the read when there is none or
// refresh is set. Only trusted and not_installed answers (the states the app
// shows no warning for) are kept, and any other answer drops the entry, so the
// cache holds the newest answer only when it is healthy.
func (c *hookTrustCache) Get(refresh bool) agentplugins.HookTrust {
	c.mu.Lock()
	if !refresh && c.now().Before(c.expires) {
		v := c.value
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	res, _, _ := c.flight.Do("codex", func() (any, error) {
		seq := c.begin()
		ctx, cancel := context.WithTimeout(context.Background(), 2*agentplugins.StatusTimeout)
		defer cancel()
		v := c.read(ctx)
		c.finish(seq, v)
		return v, nil
	})
	return res.(agentplugins.HookTrust)
}

func (c *hookTrustCache) begin() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started++
	return c.started
}

// finish records the answer of the read numbered seq unless a newer read has
// already updated the entry.
func (c *hookTrustCache) finish(seq int, v agentplugins.HookTrust) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq <= c.applied {
		return
	}
	c.applied = seq
	if v.State == "trusted" || v.State == "not_installed" {
		c.value, c.expires = v, c.now().Add(hookTrustTTL)
	} else {
		c.expires = time.Time{}
	}
}

// LastHookTrust remembers the newest Codex hook answer a read returned, for a
// reader that must never start Codex itself: the MCP server's Settings tools
// report what the app last saw instead of asking again (a read of
// /api/agent-hooks/codex runs Codex whenever the route's cache is stale or
// holds a warning). The zero value has seen none.
type LastHookTrust struct {
	mu    sync.Mutex
	state string
	at    time.Time
}

// Wrap returns read with every answer it gives remembered; nil stays nil, so
// the route still reports the check unavailable.
func (l *LastHookTrust) Wrap(read func(context.Context) agentplugins.HookTrust) func(context.Context) agentplugins.HookTrust {
	if read == nil {
		return nil
	}
	return func(ctx context.Context) agentplugins.HookTrust {
		v := read(ctx)
		l.mu.Lock()
		l.state, l.at = v.State, time.Now()
		l.mu.Unlock()
		return v
	}
}

// Get returns the last answer's state and when it was read; ok is false until
// a read answered. It never reads.
func (l *LastHookTrust) Get() (state string, at time.Time, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state, l.at, !l.at.IsZero()
}
