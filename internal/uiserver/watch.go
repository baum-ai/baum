package uiserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// IndexWatch is what WatchIndex reads: a cached index and a cheap check for
// changes made outside this process. Only Snapshot is required; without the
// others every tick reads a snapshot.
type IndexWatch struct {
	// Fingerprint digests what the index is built from that other processes
	// change. It must be cheap: it runs every tick.
	Fingerprint func(context.Context) string
	// Invalidate drops the cached index; it is called when Fingerprint moves.
	Invalidate func()
	// Fresh reports the version of the cached index while it is still valid.
	Fresh func() (uint64, bool)
	// Snapshot returns the index and its version, building it if needed. It
	// must read local state only and honor cancellation.
	Snapshot func(context.Context) (any, uint64, error)
}

// WatchIndex publishes an index event for changes nothing in this process
// announced: terminal generation, other processes' store and database
// writes, and changes that come with time. Each tick reads the index again
// only when the fingerprint moved, the cached index expired or was replaced,
// and publishes only when its JSON differs from the last one published.
func (s *Server) WatchIndex(ctx context.Context, interval time.Duration, w IndexWatch) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var previous [sha256.Size]byte
	initialized := false
	var fingerprint string
	var version uint64
	for {
		seen := ""
		if w.Fingerprint != nil {
			seen = w.Fingerprint(ctx)
		}
		current, fresh := uint64(0), false
		if w.Fresh != nil {
			current, fresh = w.Fresh()
		}
		if !initialized || seen != fingerprint || !fresh || current != version {
			if initialized && seen != fingerprint && w.Invalidate != nil {
				w.Invalidate()
			}
			value, v, err := w.Snapshot(ctx)
			if err == nil {
				data, encodeErr := json.Marshal(value)
				if encodeErr == nil {
					fingerprint, version = seen, v
					digest := sha256.Sum256(data)
					if !initialized || digest != previous {
						_ = s.Publish("index", struct{}{})
						previous = digest
						initialized = true
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
