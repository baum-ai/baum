package generate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// Versions is the part of *store.Store that Publish uses.
type Versions interface {
	Latest(context.Context, store.Key) (store.Version, error)
	BeginRun(context.Context, store.Key, time.Time) (int64, error)
	HeartbeatRun(context.Context, int64, time.Time) error
	FinishRun(context.Context, int64, string) error
	Put(context.Context, store.Key, store.Version, store.Version) error
}

// Publish runs the same validation/repair pipeline for CLI and queued jobs and
// atomically appends only a completed document to the version store. Once the
// version is stored the call succeeds: the version is already visible, so a
// failure to record the run's outcome afterwards is a warning on o.Progress.
func Publish(ctx context.Context, versions Versions, key store.Key, o Options, cost func(Result) *float64) (result Result, err error) {
	defer func() {
		if err != nil && o.DiagnosticsOut != "" {
			_ = WriteDiagnostics(o.DiagnosticsOut, err, result.Outcome)
		}
	}()
	// Publication is fenced on this version: another generation that publishes
	// first makes this one a conflict. A refresh of it in the meantime does not.
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
		if e := versions.FinishRun(finish, runID, reason); e != nil && o.Progress != nil {
			_, _ = fmt.Fprintf(o.Progress, "warning: record generation outcome: %v\n", e)
		}
	}()
	result, err = Run(ctx, o)
	if err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
	}
	list := o.PriceList
	if list == nil {
		list = pricing.Active()
	}
	result.Metadata.PriceVersion = list.Version()
	result.Metadata.RecordedCostUSD, result.Metadata.CostReason = list.BackendAccountingCost(result.Metadata.Backend, result.Metadata.Accounting)
	if cost != nil {
		result.Metadata.RecordedCostUSD = cost(result)
		if result.Metadata.RecordedCostUSD != nil {
			result.Metadata.CostReason = ""
		} else if result.Metadata.CostReason == "" {
			result.Metadata.CostReason = "cost_not_calculated"
		}
	}
	if err = versions.Put(ctx, key, latest, store.Version{Metadata: result.Metadata, Document: result.Document, Attachments: result.Attachments}); err != nil {
		return result, fmt.Errorf("store generated version: %w", err)
	}
	published = true
	return result, nil
}
