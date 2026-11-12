package jobs

import (
	"context"
	"errors"
	"github.com/baum-ai/baum/features/adaptive"
	"path/filepath"
	"testing"
)

type fakeBuilder struct {
	plan   FeatureBuildPlan
	ran    chan struct{}
	result error
}

func (b *fakeBuilder) PlanFeatureBuild(context.Context, string, string, string) (FeatureBuildPlan, error) {
	return b.plan, nil
}

func (b *fakeBuilder) BuildFeatures(_ context.Context, _, _, _ string, progress func(Progress)) (float64, error) {
	progress(Progress{Phase: "placing", Calls: 1, BuildWorkflow: adaptive.Version, BuildCoverage: &adaptive.Coverage{Protocol: adaptive.Version, Catalogued: 120, Inspected: 3, Selected: 4, Completed: 2, Remaining: 2, Source: &adaptive.SourceCoverage{Revision: "fixture", Limitations: []string{"Committed source only"}}}})
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
	if done.BuildCoverage == nil || done.BuildCoverage.Catalogued != 120 || done.BuildCoverage.Remaining != 2 || done.BuildWorkflow != adaptive.Version {
		t.Fatalf("job progress lost %+v", done)
	}
	done.BuildCoverage.Source.Limitations[0] = "mutated by reader"
	reread, _ := q.Find(done.ID)
	if reread.BuildCoverage.Source.Limitations[0] != "Committed source only" {
		t.Fatal("mutable coverage escaped queue")
	}
	if done.Reason != "backend_unavailable" || done.ActualCostUSD == nil || *done.ActualCostUSD != 0.5 {
		t.Fatalf("job = %+v", done)
	}
	builder.plan.Reason = "backend_unavailable"
	var blocked *BlockedError
	if _, err := q.Generate(context.Background(), []Item{item}, "single", false); !errors.As(err, &blocked) || blocked.Reason != "backend_unavailable" {
		t.Fatalf("a build that cannot run started: %v", err)
	}
}

func TestFreshFeatureModeValidationAndRepositoryGuard(t *testing.T) {
	q, f := newQueue(t)
	for _, item := range []Item{{Clone: "c", PR: 1, FeatureBuildMode: "fresh"}, {Clone: "c", Kind: KindFix, PR: 1, FeatureBuildMode: "fresh"}, {Clone: "c", Kind: KindFeatureBuild, FeatureBuildMode: "unknown"}} {
		if _, err := q.Generate(context.Background(), []Item{item}, "single", false); err == nil {
			t.Fatalf("invalid mode admitted %+v", item)
		}
	}
	first, err := q.Generate(context.Background(), []Item{{Clone: "clone-a", Kind: KindFeatureBuild}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	if _, err = q.Generate(context.Background(), []Item{{Clone: "clone-b", Kind: KindFeatureBuild, FeatureBuildMode: "fresh"}}, "single", false); err == nil {
		t.Fatal("fresh admitted across another clone of active repository")
	}
	if job, _ := q.Find(first[0].ID); job.State != "running" {
		t.Fatal("rejection changed active job")
	}
	close(f.release)
	waitState(t, q, first[0].ID, "completed")
}

func TestDefaultFeatureRequestJoinsQueuedFreshBuildAcrossClones(t *testing.T) {
	executor := &fakeExecutor{started: make(chan Prepared, 2), release: make(chan struct{})}
	q, err := OpenHeld(filepath.Join(privateDir(t), "jobs.sqlite"), executor, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	first, err := q.Generate(context.Background(), []Item{{Clone: "clone-a", Kind: KindFeatureBuild, FeatureBuildMode: "fresh"}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Generate(context.Background(), []Item{{Clone: "clone-b", Kind: KindFeatureBuild}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != first[0].ID || second[0].FeatureBuildMode != "fresh" {
		t.Fatalf("queued fresh was replaced: %+v %+v", first, second)
	}
}
