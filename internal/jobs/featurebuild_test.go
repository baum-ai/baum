package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakeBuilder struct {
	plan   FeatureBuildPlan
	ran    chan struct{}
	result error
}

func (b *fakeBuilder) PlanFeatureBuild(context.Context, string, string) (FeatureBuildPlan, error) {
	return b.plan, nil
}

func (b *fakeBuilder) BuildFeatures(_ context.Context, _, _ string, progress func(Progress)) (float64, error) {
	progress(Progress{Phase: "placing", Calls: 1})
	b.ran <- struct{}{}
	return 0.5, b.result
}

// A feature build starts at once on its own, with no estimate, whatever its
// model's price; it fails with the reason it stopped for, and one that cannot
// run now is refused with its reason.
func TestFeatureBuildJob(t *testing.T) {
	registry, clone := registeredClone(t)
	builder := &fakeBuilder{ran: make(chan struct{}, 2), result: &StoppedError{Reason: "backend_unavailable"},
		plan: FeatureBuildPlan{Backend: "claude", Model: "no-such-model"}}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), &Local{Registry: registry, Features: builder}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	item := Item{Clone: clone.ID, Kind: KindFeatureBuild}
	if _, err := q.Generate(context.Background(), []Item{item, item}, "batch", false); err == nil {
		t.Fatal("a feature build is started with other items")
	}
	jobs, err := q.Generate(context.Background(), []Item{item}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	if j := jobs[0]; j.Repository != "owner/repo" || j.Model != "no-such-model" {
		t.Fatalf("job = %+v", j)
	}
	<-builder.ran
	done := waitState(t, q, jobs[0].ID, "failed")
	if done.Reason != "backend_unavailable" || done.ActualCostUSD == nil || *done.ActualCostUSD != 0.5 {
		t.Fatalf("job = %+v", done)
	}
	builder.plan.Reason = "backend_unavailable"
	var blocked *BlockedError
	if _, err := q.Generate(context.Background(), []Item{item}, "single", false); !errors.As(err, &blocked) || blocked.Reason != "backend_unavailable" {
		t.Fatalf("a build that cannot run started: %v", err)
	}
}
