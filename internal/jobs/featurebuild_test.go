package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakeBuilder struct {
	estimate FeatureBuildEstimate
	caps     chan float64
	result   error
}

func (b *fakeBuilder) EstimateFeatureBuild(context.Context, string, string) (FeatureBuildEstimate, error) {
	return b.estimate, nil
}

func (b *fakeBuilder) BuildFeatures(_ context.Context, _, _ string, capUSD float64, progress func(Progress)) (float64, error) {
	progress(Progress{Phase: "placing", Calls: 1})
	b.caps <- capUSD
	return 0.5, b.result
}

// A feature build is confirmed on its own from an estimate, runs with the
// estimate's cap, is one job per repository, and fails with the reason it
// stopped for.
func TestFeatureBuildJob(t *testing.T) {
	registry, clone := registeredClone(t)
	price := 1.0
	builder := &fakeBuilder{caps: make(chan float64, 2), result: &StoppedError{Reason: "cost_cap"},
		estimate: FeatureBuildEstimate{Backend: "claude", Model: "claude-haiku-5-5", PullRequests: 40, EstimateUSD: &price, CapUSD: 1.4}}
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
	if p := e.Items[0]; p.BuildCapUSD != 1.4 || p.BuildPullRequests != 40 || p.Repository != "owner/repo" || p.BlockedReason != "" {
		t.Fatalf("prepared = %+v", p)
	}
	builder.estimate.CapUSD = 2
	if _, err := q.Submit(context.Background(), e.ID, true, false); err == nil {
		t.Fatal("a build whose cap changed since its estimate is queued")
	}
	builder.estimate.CapUSD = 1.4
	e = estimate(t, q, "single", item)
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-builder.caps; got != 1.4 {
		t.Fatalf("cap = %v", got)
	}
	done := waitState(t, q, jobs[0].ID, "failed")
	if done.Reason != "cost_cap" || done.ActualCostUSD == nil || *done.ActualCostUSD != 0.5 {
		t.Fatalf("job = %+v", done)
	}
	builder.estimate.EstimateUSD = nil
	if e := estimate(t, q, "single", item); e.Items[0].BlockedReason != "unknown_model_price" {
		t.Fatalf("an unpriced build is not blocked: %+v", e.Items[0])
	}
}
