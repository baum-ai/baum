package uiserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// WatchIndex notices terminal generation/store changes as well as API writes.
// Its snapshot callback must read local state only and honor cancellation.
func (s *Server) WatchIndex(ctx context.Context, interval time.Duration, snapshot func(context.Context) (any, error)) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var previous [sha256.Size]byte
	initialized := false
	for {
		value, err := snapshot(ctx)
		if err == nil {
			data, encodeErr := json.Marshal(value)
			if encodeErr == nil {
				digest := sha256.Sum256(data)
				if !initialized || digest != previous {
					_ = s.Publish("index", struct{}{})
					previous = digest
					initialized = true
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
