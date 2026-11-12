package cli

import (
	"cmp"
	"context"
	"errors"
	"sync/atomic"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
)

// githubReaders opens the feature log's GitHub REST readers.
func githubReaders(cfg config.Config) func(context.Context, github.Repository) (github.Reader, error) {
	client := github.NewFromConfig(cfg, github.Options{})
	return func(ctx context.Context, repository github.Repository) (github.Reader, error) {
		return client.REST(ctx, repository)
	}
}

// featureBuilder runs the job queue's feature builds (jobs.KindFeatureBuild)
// on the daemon's feature worker, once it has one.
type featureBuilder struct {
	worker atomic.Pointer[decisionlog.FeatureWorker]
}

var errFeatureLogOff = errors.New("the feature log is off: its store did not open")

func (b *featureBuilder) PlanFeatureBuild(ctx context.Context, repository, dir, mode string) (jobs.FeatureBuildPlan, error) {
	w := b.worker.Load()
	if w == nil {
		return jobs.FeatureBuildPlan{}, errFeatureLogOff
	}
	workflow, err := w.Store.FeatureBuildWorkflow(ctx, repository)
	if err != nil {
		return jobs.FeatureBuildPlan{}, err
	}
	backend, model, reason, err := w.PlanBuild(ctx, repository, dir, decisionlog.BuildOptions{Gated: true, Adaptive: mode == "fresh" || workflow != "legacy"})
	return jobs.FeatureBuildPlan{Backend: backend, Model: model, Reason: reason}, err
}

func (b *featureBuilder) BuildFeatures(ctx context.Context, repository, dir, mode string, progress func(jobs.Progress)) (float64, error) {
	w := b.worker.Load()
	if w == nil {
		return 0, errFeatureLogOff
	}
	// Recheck eligibility at execution, before replacing any saved candidate.
	plan, err := b.PlanFeatureBuild(ctx, repository, dir, mode)
	if err != nil {
		return 0, err
	}
	if plan.Reason != "" {
		return 0, &jobs.StoppedError{Reason: plan.Reason}
	}
	if mode == "fresh" {
		if err := w.Store.ArchiveFeatureBuild(ctx, repository); err != nil {
			return 0, err
		}
	}
	workflow, err := w.Store.FeatureBuildWorkflow(ctx, repository)
	if err != nil {
		return 0, err
	}
	report, err := w.Build(ctx, repository, dir, decisionlog.BuildOptions{Adaptive: workflow != "legacy", Gated: true, Progress: func(p decisionlog.BuildProgress) {
		progress(jobs.Progress{Phase: p.Phase, Calls: p.Calls, CostReason: unpricedReason(p.Unpriced), BuildCoverage: p.Coverage, BuildWorkflow: workflow})
	}})
	progress(jobs.Progress{Phase: "finished", Calls: report.Calls, CostReason: unpricedReason(report.UnpricedCalls), BuildCoverage: report.Coverage, BuildWorkflow: workflow})

	if !report.Current && ctx.Err() == nil {
		// A store error stops it too, as failed.
		err = &jobs.StoppedError{Reason: cmp.Or(report.Stopped, "failed"), Err: err}
	}
	return report.SpentUSD, err
}

// unpricedReason is a build job's cost reason: its cost is a lower bound
// when some of its calls could not be priced.
func unpricedReason(unpriced int) string {
	if unpriced > 0 {
		return jobs.ReasonUnpricedCalls
	}
	return ""
}
