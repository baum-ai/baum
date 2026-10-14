package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
)

// pullRequestSource is what "baum bundle" asks GitHub: which pull request to
// work on, and its evidence. *github.Client implements it.
type pullRequestSource interface {
	assemble.GitHub
	ResolvePR(ctx context.Context, clone github.Clone, opts github.ResolveOptions) (github.Target, error)
}

// Seams for tests: GitHub is never contacted and no image is downloaded.
var (
	newGitHub = func(cfg config.Config) pullRequestSource {
		return github.NewFromConfig(cfg, github.Options{})
	}
	newImageFetcher = func() assemble.ImageFetcher { return assemble.NewHTTPImageFetcher(nil) }
	// bundleContext ends on SIGINT or SIGTERM; tests replace it to cancel
	// without a signal.
	bundleContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	}
)

func newBundleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Assemble the generation input for a pull request without calling a model",
		Long: `Bundle assembles the generation input of a pull request, the baum.review/1
bundle that "baum generate" gives its model, and writes it as JSON to stdout or
to the file given with -o. It calls no model. Only the bundle is written to
stdout; progress and warnings go to stderr, including the bundle's SHA-256
digest, which is the digest of the JSON written without its final newline.

The pull request is --pr, or the one for the current branch. The repository is
--repo, or the one the clone's origin remote names. The pull request, its
commits, checks and conversation come from GitHub with your own token; the
diff, REVIEW.md at the base commit and the code checkout come from the clone's
git objects; sessions come from the home directory. Secrets and e-mail
addresses are masked before the bundle is trimmed to its caps, and nothing is
dropped from the diff index to fit them.

The clone is only read: bundle checks out nothing, changes no index, branch or
ref, and does not fetch. When the pull request's head or base commit is not in
the clone it fails and says which git fetch to run.`,
		Args: cobra.NoArgs,
		RunE: runBundle,
	}
	cmd.Flags().Int("pr", 0, "pull request number (default: the pull request of the current branch)")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (defaults to the origin remote)")
	cmd.Flags().StringP("output", "o", "", "write the bundle to this file instead of stdout")
	cmd.Flags().StringArray("previous-path", nil, "directory the repository used to live at (repeatable)")
	cmd.Flags().String("claude-dir", "", "Claude Code projects directory (default ~/.claude/projects)")
	cmd.Flags().String("codex-dir", "", "Codex sessions directory (default ~/.codex/sessions)")
	cmd.Flags().StringArray("cursor-store", nil, "Cursor state.vscdb file or directory holding one (repeatable)")
	for _, hidden := range []string{"claude-dir", "codex-dir", "cursor-store"} {
		_ = cmd.Flags().MarkHidden(hidden)
	}
	return cmd
}

func runBundle(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	number, _ := flags.GetInt("pr")
	repoFlag, _ := flags.GetString("repo")
	output, _ := flags.GetString("output")
	stores := sessions.Options{}
	stores.PreviousPaths, _ = flags.GetStringArray("previous-path")
	stores.ClaudeProjectsDir, _ = flags.GetString("claude-dir")
	stores.CodexSessionsDir, _ = flags.GetString("codex-dir")
	stores.CursorStorePaths, _ = flags.GetStringArray("cursor-store")
	if number < 0 {
		return fmt.Errorf("--pr must be a pull request number, got %d", number)
	}

	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return err
	}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return err
	}
	// Ctrl-C cancels the work and returns, so the clone's private repository
	// and a half-written output file are cleaned up.
	ctx, stop := bundleContext(cmd.Context())
	defer stop()
	stderr := cmd.ErrOrStderr()

	clone, err := gitlocal.Open(ctx, ".", gitlocal.Options{})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("bundle works inside a git clone of the repository: %w", err)
	}
	defer func() { _ = clone.Close() }()

	client := newGitHub(cfg)
	target, err := client.ResolvePR(ctx, clone, github.ResolveOptions{Repo: repoFlag, PR: number})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "assembling the bundle for %s#%d\n", target.Repository, target.Number)

	assembler := assemble.Assembler{
		GitHub: client, Git: clone,
		Sessions: assemble.LocalSessions{Dir: clone.Dir(), Stores: stores, Warn: stderr},
		Images:   newImageFetcher(),
		Warn:     stderr,
	}
	result, err := assembler.Assemble(ctx, assemble.Request{
		Repository: target.Repository, Number: target.Number, Limits: assemble.LimitsFrom(caps),
	})
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			return fmt.Errorf("pull request %s#%d was not found: it does not exist, or your token cannot see it: %w", target.Repository, target.Number, err)
		}
		return err
	}

	if output == "" {
		if _, err := cmd.OutOrStdout().Write(result.JSON); err != nil {
			return fmt.Errorf("writing the bundle: %w", err)
		}
	} else if err := writeFileAtomically(output, result.JSON); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "bundle digest sha256:%s, %d masked span(s), %d hunk(s), %d session(s)\n",
		result.Bundle.Digest, result.Bundle.MaskedSpanCount, len(result.Bundle.Diff), len(result.Bundle.Sessions))
	return nil
}

// writeFileAtomically writes data to path through a temporary file beside it,
// so a failed run leaves no partial bundle. The file is readable by its owner
// only: it holds session text.
func writeFileAtomically(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".baum-bundle-*")
	if err != nil {
		return fmt.Errorf("writing the bundle: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing the bundle: %w", err)
	}
	// Reach the disk before the rename makes the file visible under its name.
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing the bundle: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("writing the bundle: %w", err)
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("writing the bundle: %w", err)
	}
	return nil
}
