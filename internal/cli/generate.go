package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions"
)

// generateBackends builds the local backends; tests replace it with stubs.
var generateBackends = generate.DefaultBackends

// maxProviderAttempts and maxRepairAttempts are the hosted service's defaults.
const (
	maxProviderAttempts = 3
	maxRepairAttempts   = 2
)

func newGenerateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate the review document for a pull request",
		Long: `Generate assembles the generation bundle of a pull request, as "baum bundle"
does, and has a coding agent installed on this machine (Claude Code or Codex,
under your own login) read the bundle and a read-only checkout of the pull
request and write the review. The document is repaired against the validator,
has secrets masked, is completed with the fields baum derives and is checked
strictly; only then is it stored as the next local version and written to
stdout or to the file given with -o. The previous local version supplies
section and risk history. --store overrides the local store directory. A
document that cannot be made valid is never written.

The pull request is --pr, or the one for the current branch; the repository is
--repo, or the one the clone's origin remote names. It checks nothing out,
does not touch your working tree, index or refs, and fetches nothing. The local
version store is written under the shared git directory, or the fallback or
override selected by --store.

--backend auto uses claude when it is installed and passes its checks, and
codex otherwise. The anthropic and cursor-agent backends are not available in
this version. The agent runs in a private temporary directory that holds the
bundle and the pull request's head and merge-base files, with its tools
restricted to reading, and is removed afterwards.

Progress goes to stderr; stdout holds only the document. Budgets come from the
config file: run_timeout (the whole run), pass_timeout (each model call),
max_calls and max_tokens. --model names a model of one backend, so with
--backend auto it applies to claude only; if auto falls back to codex, codex
uses its own default model and stderr says why claude was skipped.

--diagnostics writes how the run ended and its redacted outcome, also when it
fails after the command line was accepted. --keep-run-dir keeps the agent's run
directory and prints its path; it can hold partially masked rejected documents
(rejected-N.json) and read-only trees, so run chmod -R u+w on it before rm -rf.

Exit codes: 0 success; 1 other failure; 2 input (bad flags, no such pull
request, commits missing from the clone, bundle over its caps); 3 backend (not
installed, too old, sandbox, provider unavailable); 4 budget (calls, tokens or
wall clock spent); 5 validation (rejected after the repairs, or a secret was
written); 130 interrupted.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return &generate.Error{Category: generate.CategoryInput, Err: err}
			}
			return nil
		},
		RunE: runGenerate,
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &generate.Error{Category: generate.CategoryInput, Err: err}
	})
	cmd.Flags().Int("pr", 0, "pull request number (default: the pull request of the current branch)")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (defaults to the origin remote)")
	cmd.Flags().String("backend", "", "model backend: auto, claude or codex (anthropic and cursor-agent are not available yet)")
	cmd.Flags().String("model", "", "model name for the selected backend")
	cmd.Flags().String("store", "", "local version store directory (default: shared git directory, with XDG data fallback)")
	cmd.Flags().StringP("output", "o", "", "write the document to this file instead of stdout")
	cmd.Flags().String("bundle-out", "", "also write the exact bundle the agent was given to this file, for debugging")
	cmd.Flags().String("diagnostics", "", "write the run's redacted outcome (usage, repairs, findings, bounded events) to this file, also when the run fails")
	cmd.Flags().Bool("keep-run-dir", false, "keep the agent's run directory and print its path (it may hold unmasked-in-part rejected documents and read-only trees)")
	cmd.Flags().StringArray("previous-path", nil, "directory the repository used to live at (repeatable)")
	cmd.Flags().String("claude-dir", "", "Claude Code projects directory (default ~/.claude/projects)")
	cmd.Flags().String("codex-dir", "", "Codex sessions directory (default ~/.codex/sessions)")
	cmd.Flags().StringArray("cursor-store", nil, "Cursor state.vscdb file or directory holding one (repeatable)")
	for _, hidden := range []string{"claude-dir", "codex-dir", "cursor-store"} {
		_ = cmd.Flags().MarkHidden(hidden)
	}
	return cmd
}

func interruptedError() error {
	return &generate.Error{Category: generate.CategoryInterrupted, Err: errors.New("interrupted")}
}

// runGenerate runs the command and, for a failure before the pipeline started,
// writes the minimal --diagnostics file the pipeline would have written.
func runGenerate(cmd *cobra.Command, _ []string) error {
	started := false
	err := doGenerate(cmd, &started)
	if err != nil && !started {
		if path, _ := cmd.Flags().GetString("diagnostics"); path != "" {
			if info, statErr := os.Stat(filepath.Dir(path)); statErr == nil && info.IsDir() {
				_ = generate.WriteDiagnostics(path, err, agent.Outcome{})
			}
		}
	}
	return err
}

func doGenerate(cmd *cobra.Command, started *bool) (runErr error) {
	flags := cmd.Flags()
	number, _ := flags.GetInt("pr")
	repoFlag, _ := flags.GetString("repo")
	backendFlag, _ := flags.GetString("backend")
	modelFlag, _ := flags.GetString("model")
	output, _ := flags.GetString("output")
	bundleOut, _ := flags.GetString("bundle-out")
	diagnostics, _ := flags.GetString("diagnostics")
	keepRunDir, _ := flags.GetBool("keep-run-dir")
	stores := sessions.Options{}
	stores.PreviousPaths, _ = flags.GetStringArray("previous-path")
	stores.ClaudeProjectsDir, _ = flags.GetString("claude-dir")
	stores.CodexSessionsDir, _ = flags.GetString("codex-dir")
	stores.CursorStorePaths, _ = flags.GetStringArray("cursor-store")
	input := func(err error) error { return &generate.Error{Category: generate.CategoryInput, Err: err} }
	if number < 0 {
		return input(fmt.Errorf("--pr must be a pull request number, got %d", number))
	}
	// Every file is checked before the agent starts, so a long run does not
	// end at an unwritable path.
	for _, path := range []string{output, bundleOut, diagnostics} {
		if path == "" {
			continue
		}
		if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
			return input(fmt.Errorf("cannot write %s: its directory does not exist", path))
		}
	}

	cfg, err := config.Load(config.Overrides{Backend: backendFlag, Model: modelFlag})
	if err != nil {
		return input(err)
	}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return input(err)
	}
	budgets := agent.Budgets{
		WallClock: time.Duration(cfg.Budgets.RunTimeout), MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens,
		MaxProviderAttempts: maxProviderAttempts, MaxRepairAttempts: maxRepairAttempts,
	}
	if budgets.WallClock <= 0 || budgets.MaxCalls <= 0 || budgets.MaxTokens <= 0 {
		return input(errors.New("the budgets run_timeout, max_calls and max_tokens must be positive"))
	}

	// Ctrl-C cancels the work, which kills the backend's process group and
	// removes the run directory.
	ctx, stop := bundleContext(cmd.Context())
	defer stop()
	stderr := cmd.ErrOrStderr()

	clone, err := gitlocal.Open(ctx, ".", gitlocal.Options{})
	if err != nil {
		if ctx.Err() != nil {
			return interruptedError()
		}
		return input(fmt.Errorf("generate works inside a git clone of the repository: %w", err))
	}
	defer func() { _ = clone.Close() }()

	client := newGitHub(cfg)
	target, err := client.ResolvePR(ctx, clone, github.ResolveOptions{Repo: repoFlag, PR: number})
	if err != nil {
		if ctx.Err() != nil {
			return interruptedError()
		}
		return input(err)
	}

	storePath, _ := flags.GetString("store")
	versions, err := store.Open(clone.CommonDir(), storePath, true)
	if err != nil {
		return input(fmt.Errorf("open version store: %w", err))
	}
	key := store.Key{Repository: target.Repository.String(), Number: target.Number}
	selected, err := generate.Select(ctx, cfg.Backend, cfg.Model, generateBackends(), stderr)
	if err != nil {
		// Preflight failures are attempts too: keep the terminal's existing failure
		// status visible even though the shared publication pipeline has not begun.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		reason := string(generate.CategoryOf(err))
		if reason == "" {
			reason = "internal"
		}
		if id, recordErr := versions.BeginRun(recordCtx, key, time.Now().Add(time.Minute)); recordErr == nil {
			_ = versions.FinishRun(recordCtx, id, reason)
		}
		return err
	}

	assembler := &assemble.Assembler{
		GitHub: client, Git: clone,
		Sessions: assemble.LocalSessions{Dir: clone.Dir(), Stores: stores, Warn: stderr},
		Images:   newImageFetcher(),
		Warn:     stderr,
	}
	*started = true
	result, err := generate.Publish(ctx, versions, key, generate.Options{
		Assembler: assembler,
		Request:   assemble.Request{Repository: target.Repository, Number: target.Number, Limits: assemble.LimitsFrom(caps)},
		Backend:   selected.Backend, BackendVersion: selected.Version, Model: selected.Model,
		Budgets: budgets, PassTimeout: time.Duration(cfg.Budgets.PassTimeout),
		Progress: stderr, KeepRunDir: keepRunDir, BundleOut: bundleOut, DiagnosticsOut: diagnostics,
	}, nil)
	if err != nil {
		return err
	}

	hint := fmt.Sprintf("baum view --pr %d --repo %q", target.Number, target.Repository.String())
	if storePath != "" {
		hint += fmt.Sprintf(" --store %q", storePath)
	}
	_, _ = fmt.Fprintf(stderr, "stored version %d; view it with: %s\n", result.Metadata.Version, hint)
	document := result.Document
	if len(document) == 0 || document[len(document)-1] != '\n' {
		document = append(append([]byte(nil), document...), '\n')
	}
	if output == "" {
		if _, err := cmd.OutOrStdout().Write(document); err != nil {
			return fmt.Errorf("writing the document: %w", err)
		}
		_, _ = fmt.Fprintln(stderr, "the document went to stdout; save it with -o review.json, then view it with: baum view review.json")
		return nil
	}
	if err := generate.WriteFileAtomic(output, document); err != nil {
		return fmt.Errorf("writing the document: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "wrote %s\nview it with: baum view %s\n", output, output)
	return nil
}
