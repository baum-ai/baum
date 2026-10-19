package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/refresh"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions"
)

func newRefreshCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "refresh", Short: "Update saved review evidence without a model call", Long: `Refresh updates checks, bot reports, review threads, agent reviews and local
session links in the latest saved review. It preserves authored content and
generation provenance, and never calls a model or creates a review version.
The pull request is --pr, or the one for the current branch. Older versions
stay unchanged. A failed refresh leaves the saved document unchanged.`, Args: cobra.NoArgs, RunE: runRefresh}
	cmd.Flags().Int("pr", 0, "pull request number (default: current branch)")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (default: origin remote)")
	cmd.Flags().String("store", "", "local version store directory")
	cmd.Flags().StringArray("previous-path", nil, "directory the repository used to live at (repeatable)")
	cmd.Flags().String("claude-dir", "", "Claude Code projects directory")
	cmd.Flags().String("codex-dir", "", "Codex sessions directory")
	cmd.Flags().StringArray("cursor-store", nil, "Cursor store (repeatable)")
	for _, name := range []string{"claude-dir", "codex-dir", "cursor-store"} {
		_ = cmd.Flags().MarkHidden(name)
	}
	return cmd
}

func runRefresh(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	number, _ := flags.GetInt("pr")
	if number < 0 {
		return fmt.Errorf("--pr must be positive")
	}
	repo, _ := flags.GetString("repo")
	override, _ := flags.GetString("store")
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return err
	}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return err
	}
	ctx, stop := bundleContext(cmd.Context())
	defer stop()
	clone, err := gitlocal.Open(ctx, ".", gitlocal.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = clone.Close() }()
	client := newGitHub(cfg)
	target, err := client.ResolvePR(ctx, clone, github.ResolveOptions{Repo: repo, PR: number})
	if err != nil {
		return err
	}
	// Read first: refresh on a missing review must not even create a store.
	versions, err := store.Open(clone.CommonDir(), override, false)
	if err != nil {
		return err
	}
	key := store.Key{Repository: target.Repository.String(), Number: target.Number}
	saved, err := versions.Latest(ctx, key)
	if err != nil {
		return err
	}
	stores := sessions.Options{}
	stores.PreviousPaths, _ = flags.GetStringArray("previous-path")
	stores.ClaudeProjectsDir, _ = flags.GetString("claude-dir")
	stores.CodexSessionsDir, _ = flags.GetString("codex-dir")
	stores.CursorStorePaths, _ = flags.GetStringArray("cursor-store")
	updater := refresh.Refresher{GitHub: client, Git: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Stores: stores, Warn: cmd.ErrOrStderr()}}
	document, err := updater.Refresh(ctx, target.Repository, target.Number, saved, assemble.LimitsFrom(caps).Caps, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := versions.RefreshCurrent(ctx, key, saved, document); err != nil {
		return err
	}
	hint := fmt.Sprintf("baum internal view --pr %d --repo %q", target.Number, target.Repository.String())
	if override != "" {
		hint += fmt.Sprintf(" --store %q", override)
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "refreshed %s#%d version %d; open with %s\n", target.Repository, target.Number, saved.Metadata.Version, hint)
	return err
}
