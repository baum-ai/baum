package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakeBuilder struct {
	estimate FeatureBuildEstimate
	ran      chan struct{}
	result   error
}

func (b *fakeBuilder) EstimateFeatureBuild(context.Context, string, string) (FeatureBuildEstimate, error) {
	return b.estimate, nil
}

func (b *fakeBuilder) BuildFeatures(_ context.Context, _, _ string, progress func(Progress)) (float64, error) {
	progress(Progress{Phase: "placing", Calls: 1})
	b.ran <- struct{}{}
	return 0.5, b.result
}

// A feature build is confirmed on its own from an estimate, is one job per
// repository, and fails with the reason it stopped for.
func TestFeatureBuildJob(t *testing.T) {
	registry, clone := registeredClone(t)
	price := 1.0
	builder := &fakeBuilder{ran: make(chan struct{}, 2), result: &StoppedError{Reason: "backend_unavailable"},
		estimate: FeatureBuildEstimate{Backend: "claude", Model: "claude-haiku-5-5", PullRequests: 40, EstimateUSD: &price, DiscoveryUSD: 5, HistoryTruncated: true}}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), &Local{Registry: registry, Features: builder}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	item := Item{Clone: clone.ID, Kind: KindFeatureBuild}
	if _, err := q.Estimate(context.Background(), []Item{item, item}, "batch"); err == nil {
		t.Fatal("a feature build is estimated with other items")
	}
	if _, err := q.Generate(context.Background(), []Item{item}, "single", false); !errors.Is(err, ErrFixNeedsConfirmation) {
		t.Fatalf("a feature build runs without a confirmation: %v", err)
	}
	e := estimate(t, q, "single", item)
	if p := e.Items[0]; p.BuildPullRequests != 40 || p.BuildDiscoveryUSD != 5 || !p.BuildHistoryTruncated || p.Repository != "owner/repo" || p.BlockedReason != "" {
		t.Fatalf("prepared = %+v", p)
	}
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-builder.ran
	done := waitState(t, q, jobs[0].ID, "failed")
	if done.Reason != "backend_unavailable" || done.ActualCostUSD == nil || *done.ActualCostUSD != 0.5 {
		t.Fatalf("job = %+v", done)
	}
	builder.estimate.EstimateUSD = nil
	if e := estimate(t, q, "single", item); e.Items[0].BlockedReason != "unknown_model_price" {
		t.Fatalf("an unpriced build is not blocked: %+v", e.Items[0])
	}
}
