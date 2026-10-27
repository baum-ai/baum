package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
			return runFeatures(cmd, repository, clone, days, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Sync(ctx, repository, dir)
			})
		},
	}
	flags(sync)
	sync.Flags().IntVar(&days, "days", int(decisionlog.FeatureFirstBuildWindow/(24*time.Hour)), "days the first sync lists")
	link := &cobra.Command{
		Use:   "link",
		Short: "Link the commits of the last week's pull requests to this machine's sessions",
		Long: `Link runs the feature worker's full re-link once and prints its counts as
JSON. It writes the daemon's store: run it with the daemon stopped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeatures(cmd, repository, clone, 0, func(ctx context.Context, w *decisionlog.FeatureWorker, dir string) (any, error) {
				return w.Index(ctx, repository, dir, nil, true)
			})
		},
	}
	flags(link)
	cmd.AddCommand(sync, link)
	return cmd
}

func runFeatures(cmd *cobra.Command, repository, clone string, days int, stage func(context.Context, *decisionlog.FeatureWorker, string) (any, error)) error {
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
	worker := &decisionlog.FeatureWorker{Store: store, GitHub: githubReaders(cfg), Window: time.Duration(days) * 24 * time.Hour}
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
