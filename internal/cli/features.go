package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/features/discover"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
)

// githubReaders opens the feature log's GitHub REST readers.
func githubReaders(cfg config.Config) func(context.Context, github.Repository) (github.Reader, error) {
	client := github.NewFromConfig(cfg, github.Options{})
	return func(ctx context.Context, repository github.Repository) (github.Reader, error) {
		return client.REST(ctx, repository)
	}
}

// newFeaturesCommand is "baum internal features", for testing the local
// feature log's stages by hand.
func newFeaturesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "features", Short: "Test the local feature log", Hidden: true, Args: cobra.NoArgs}
	var repository, clone string
	var days int
	flags := func(c *cobra.Command) {
		c.Flags().StringVar(&repository, "repo", "", "repository (owner/name)")
		c.Flags().StringVar(&clone, "clone", "", "clone to read; default the registered clone of the repository")
		_ = c.MarkFlagRequired("repo")
	}
	sync := &cobra.Command{
		Use:   "sync",
		Short: "List the repository's pull requests and record their paths and commits",
		Long: `Sync runs the feature worker's pull request sync once, without its gate, and
prints what it did as JSON. The first sync lists the pull requests updated in
the last --days days. It writes the daemon's store: run it with the daemon
stopped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeatures(cmd, repository, clone, days, false, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Sync(ctx, repository, dir)
			})
		},
	}
	flags(sync)
	sync.Flags().IntVar(&days, "days", int(decisionlog.FeatureFirstSyncWindow/(24*time.Hour)), "days the first sync lists")
	link := &cobra.Command{
		Use:   "link",
		Short: "Link the commits of the last week's pull requests to this machine's sessions",
		Long: `Link runs the feature worker's full re-link once and prints its counts as
JSON. It writes the daemon's store: run it with the daemon stopped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeatures(cmd, repository, clone, 0, false, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Index(ctx, repository, dir, nil, true)
			})
		},
	}
	flags(link)
	var o decisionlog.FeatureOptions
	callFlags := func(c *cobra.Command) {
		flags(c)
		c.Flags().IntVar(&o.Limit, "limit", 0, "most items or features to take; default the stage's limit")
		c.Flags().StringVar(&o.Backend, "backend", "", "backend to call instead of the configured one")
		c.Flags().StringVar(&o.Model, "model", "", "model to call instead of the configured one")
	}
	place := &cobra.Command{
		Use:   "assign",
		Short: "Place the repository's pending pull requests and sessions in features",
		Long: `Assign runs the feature worker's placement once, without its gate and
ignoring the activation, and prints each item taken as JSON. --dry-run prints
each item's candidates and input size and makes no call.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Manual = true
			return runFeatures(cmd, repository, clone, 0, true, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Assign(ctx, repository, dir, o)
			})
		},
	}
	callFlags(place)
	place.Flags().BoolVar(&o.DryRun, "dry-run", false, "render the calls without making them; write nothing")
	summarize := &cobra.Command{
		Use:   "summarize",
		Short: "Write the text of the repository's features whose members changed",
		Long: `Summarize runs the feature worker's text stage once, without its gate and
its debounce, and prints each feature taken as JSON.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Manual = true
			return runFeatures(cmd, repository, clone, 0, true, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Summarize(ctx, repository, dir, o)
			})
		},
	}
	callFlags(summarize)
	consolidate := &cobra.Command{
		Use:   "consolidate",
		Short: "Consolidate the repository's features once",
		Long: `Consolidate makes one consolidation pass over the repository's current
features, without its gate, weekly interval or fingerprint check, and prints
what it did as JSON.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Manual = true
			return runFeatures(cmd, repository, clone, 0, true, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Consolidate(ctx, repository, dir, o)
			})
		},
	}
	flags(consolidate)
	consolidate.Flags().StringVar(&o.Backend, "backend", "", "backend to call instead of the configured one")
	consolidate.Flags().StringVar(&o.Model, "model", "", "model to call instead of the configured one")
	var build decisionlog.BuildOptions
	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build the repository's features from its pull request history",
		Long: `Build runs a first build of the repository's features once, without its
gate, and prints what it did as JSON, with a line a batch on standard error.
It lists the repository's whole pull request history once, reads the paths
of its pull requests, and builds a new version of its features from that
history, or resumes the one being built, and makes it current. A new version
is first seeded with a feature taxonomy (--taxonomy): auto reuses one made
after the version or runs feature discovery, which spends up to $5 with the
review model; none seeds nothing; latest or an id uses a stored one.
--dry-run lists the history and prints the estimate; it writes no feature.
It writes the daemon's store: run it with the daemon stopped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeatures(cmd, repository, clone, 0, true, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				if build.DryRun {
					return w.EstimateBuild(ctx, repository, dir, build)
				}
				build.Progress = func(p decisionlog.BuildProgress) {
					atLeast := ""
					if p.Unpriced > 0 {
						atLeast = fmt.Sprintf("at least (%d calls not priced) ", p.Unpriced)
					}
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s: %d of %d pull requests, %d calls, %s$%.4f\n", p.Phase, p.Placed, p.Total, p.Calls, atLeast, p.SpentUSD)
				}
				return w.Build(ctx, repository, dir, build)
			})
		},
	}
	flags(buildCmd)
	buildCmd.Flags().BoolVar(&build.DryRun, "dry-run", false, "print the estimate; write nothing")
	buildCmd.Flags().StringVar(&build.Backend, "backend", "", "backend to call instead of the configured one")
	buildCmd.Flags().StringVar(&build.Model, "model", "", "model to call instead of the configured one")
	buildCmd.Flags().StringVar(&build.Taxonomy, "taxonomy", decisionlog.TaxonomyAuto, "what a new version is seeded with: auto, none, latest or a taxonomy id")
	var show string
	discoverCmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover the repository's features from its default branch and history",
		Long: `Discover runs feature discovery once: an agent of the review harness reads a
read-only snapshot of the repository's default branch and its pull request
history digest (titles, branches and paths, no descriptions) and replies with
the repository's features, stored as a taxonomy a build can seed from. It
uses the review backend and model and spends up to about $5. It prints what
it did as JSON; --show latest or an id prints a stored taxonomy instead. It
writes the daemon's store: run it with the daemon stopped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeatures(cmd, repository, clone, 0, true, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				if show != "" {
					id, taxonomy, err := w.Store.Taxonomy(ctx, repository, show)
					return struct {
						ID int64 `json:"id"`
						discover.FeatureTaxonomy
					}{id, taxonomy}, err
				}
				return w.Discover(ctx, repository, dir)
			})
		},
	}
	flags(discoverCmd)
	discoverCmd.Flags().StringVar(&show, "show", "", "print a stored taxonomy: latest or its id")
	cmd.AddCommand(sync, link, place, summarize, consolidate, buildCmd, discoverCmd)
	return cmd
}

// runFeatures runs one stage; calls, for placement and text, reads the
// preferences and the repository's settings, which choose the backend.
func runFeatures(cmd *cobra.Command, repository, clone string, days int, calls bool, stage func(context.Context, *decisionlog.FeatureWorker, string) (any, error)) error {
	ctx := cmd.Context()
	parsed, err := github.ParseRepository(repository)
	if err != nil {
		return err
	}
	repository = strings.ToLower(parsed.String())
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return err
	}
	if clone == "" {
		if clone, err = registeredClone(ctx, repository); err != nil {
			return err
		}
	}
	state, err := jobs.Directory()
	if err != nil {
		return err
	}
	store, err := decisionlog.Open(filepath.Join(state, "decisions", "decisions.sqlite"), decisionlog.OpenOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	worker := &decisionlog.FeatureWorker{Store: store, GitHub: githubReaders(cfg), Config: cfg, Window: time.Duration(days) * 24 * time.Hour}
	if calls {
		registry, err := repos.OpenReadOnly("")
		if err != nil {
			return fmt.Errorf("reading the preferences: %w", err)
		}
		defer func() { _ = registry.Close() }()
		worker.Registry = registry
	}
	report, err := stage(ctx, worker, clone)
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); err == nil {
		err = encodeErr
	}
	return err
}

// registeredClone is the first available registered clone of repository.
func registeredClone(ctx context.Context, repository string) (string, error) {
	registry, err := repos.OpenReadOnly("")
	if err != nil {
		return "", err
	}
	defer func() { _ = registry.Close() }()
	picked, err := registry.Repositories(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range picked {
		if r.Repository != repository {
			continue
		}
		for _, c := range r.Clones {
			if c.Available {
				return c.Path, nil
			}
		}
	}
	return "", fmt.Errorf("no registered clone of %s; pass --clone", repository)
}

// featureBuilder runs the job queue's feature builds (jobs.KindFeatureBuild)
// on the daemon's feature worker, once it has one.
type featureBuilder struct {
	worker atomic.Pointer[decisionlog.FeatureWorker]
}

var errFeatureLogOff = errors.New("the feature log is off: its store did not open")

func (b *featureBuilder) EstimateFeatureBuild(ctx context.Context, repository, dir string) (jobs.FeatureBuildEstimate, error) {
	w := b.worker.Load()
	if w == nil {
		return jobs.FeatureBuildEstimate{}, errFeatureLogOff
	}
	e, err := w.EstimateBuild(ctx, repository, dir, decisionlog.BuildOptions{Gated: true})
	return jobs.FeatureBuildEstimate{Backend: e.Backend, Model: e.Model, PullRequests: e.PullRequests, EstimateUSD: e.EstimateUSD,
		DiscoveryUSD: e.DiscoveryUSD, Reason: e.Reason}, err
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
