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

func (b *featureBuilder) PlanFeatureBuild(ctx context.Context, repository, dir string) (jobs.FeatureBuildPlan, error) {
	w := b.worker.Load()
	if w == nil {
		return jobs.FeatureBuildPlan{}, errFeatureLogOff
	}
	backend, model, reason, err := w.PlanBuild(ctx, repository, dir, decisionlog.BuildOptions{Gated: true})
	return jobs.FeatureBuildPlan{Backend: backend, Model: model, Reason: reason}, err
}

func (b *featureBuilder) BuildFeatures(ctx context.Context, repository, dir string, progress func(jobs.Progress)) (float64, error) {
	w := b.worker.Load()
	if w == nil {
		return 0, errFeatureLogOff
	}
	report, err := w.Build(ctx, repository, dir, decisionlog.BuildOptions{Gated: true, Progress: func(p decisionlog.BuildProgress) {
		progress(jobs.Progress{Phase: p.Phase, Calls: p.Calls, CostReason: unpricedReason(p.Unpriced)})
	}})
	if report.UnpricedCalls > 0 {
		progress(jobs.Progress{Phase: "finished", Calls: report.Calls, CostReason: jobs.ReasonUnpricedCalls})
	}
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
