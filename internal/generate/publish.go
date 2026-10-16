package generate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// Publish runs the same validation/repair pipeline for CLI and queued jobs and
// atomically appends only a completed document to the version store.
func Publish(ctx context.Context, versions *store.Store, key store.Key, o Options, cost func(Result) *float64) (result Result, err error) {
	defer func() {
		if err != nil && o.DiagnosticsOut != "" {
			_ = WriteDiagnostics(o.DiagnosticsOut, err, result.Outcome)
		}
	}()
	// Publication is fenced on this exact document: a refresh that replaces it
	// during generation makes the generated version a conflict, not latest.
	latest, err := versions.Latest(ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	if err == nil {
		o.Request.Previous, err = review.PreviousVersionOf(latest.Document, latest.Metadata.Version)
		if err != nil {
			return result, err
		}
	}
	runID, err := versions.BeginRun(ctx, key, time.Now().Add(2*time.Minute))
	if err != nil {
		return result, err
	}
	heartbeat, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeat.Done():
				return
			case <-ticker.C:
				lease, cancel := context.WithTimeout(heartbeat, 10*time.Second)
				_ = versions.HeartbeatRun(lease, runID, time.Now().Add(2*time.Minute))
				cancel()
			}
		}
	}()
	published := false
	defer func() {
		stop()
		<-done
		reason := ""
		if !published {
			reason = string(CategoryOf(err))
			if reason == "" {
				reason = "internal"
			}
		}
		finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := versions.FinishRun(finish, runID, reason); e != nil && err == nil {
			err = fmt.Errorf("record generation outcome: %w", e)
		}
	}()
	result, err = Run(ctx, o)
	if err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
	}
	if cost != nil {
		result.Metadata.RecordedCostUSD = cost(result)
	}
	if err = versions.Put(ctx, key, latest, store.Version{Metadata: result.Metadata, Document: result.Document, Attachments: result.Attachments}); err != nil {
		return result, fmt.Errorf("store generated version: %w", err)
	}
	published = true
	return result, nil
}
