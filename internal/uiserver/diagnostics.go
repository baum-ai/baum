package uiserver

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// diagnosticsTTL is how long a diagnostics report is served without running
// the doctor again. The tray asks for it on a timer; the app asks for a fresh
// run with ?refresh=1 when the user opens or re-runs the diagnostics.
const diagnosticsTTL = 30 * time.Minute

// diagnosticsCache keeps the last successful diagnostics report. Failures
// (such as a timeout) are never kept.
type diagnosticsCache struct {
	ttl time.Duration
	now func() time.Time

	mu     sync.Mutex
	report any
	at     time.Time
	have   bool
}

func newDiagnosticsCache() *diagnosticsCache {
	return &diagnosticsCache{ttl: diagnosticsTTL, now: time.Now}
}

// doChan returns the cached report while it is fresh, and otherwise runs
// `run` through the group, so concurrent requests share one run. A refresh
// skips the cached report and replaces it on success. Refreshing requests
// use their own group key so they never join a run that may be a cache hit.
func (c *diagnosticsCache) doChan(group *singleflight.Group, refresh bool, run func() (any, error)) <-chan singleflight.Result {
	key := "report"
	if refresh {
		key = "refresh"
	} else if report, ok := c.fresh(); ok {
		out := make(chan singleflight.Result, 1)
		out <- singleflight.Result{Val: report}
		return out
	}
	return group.DoChan(key, func() (any, error) {
		report, err := run()
		if err == nil {
			c.mu.Lock()
			c.report, c.at, c.have = report, c.now(), true
			c.mu.Unlock()
		}
		return report, err
	})
}

func (c *diagnosticsCache) fresh() (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.have || c.now().Sub(c.at) >= c.ttl {
		return nil, false
	}
	return c.report, true
}
