package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/transcript"
)

// newDecisionsCommand is "baum internal decisions", for testing local
// decision capture by hand.
func newDecisionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "decisions", Short: "Test local decision capture", Hidden: true, Args: cobra.NoArgs}
	var target decisionlog.Target
	var options decisionlog.Options
	capture := &cobra.Command{
		Use:   "capture",
		Short: "Capture one session's decisions into the local store, or show what a capture would send",
		Long: `Capture runs decision capture for one session as the daemon does, without its
gate, throttle or unchanged check (the caps still apply), and prints what it
did as JSON. With --dry-run it renders the transcript and prints the turns, the
token estimate and the cost reservation, and makes no model call and no write.
A Claude Code session is looked up in ~/.claude/projects by its id unless
--path names its transcript.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Force = true
			return runDecisionCapture(cmd.Context(), cmd, target, options)
		},
	}
	capture.Flags().StringVar(&target.Agent, "agent", livesessions.AgentClaudeCode, "agent: claude-code, codex or cursor")
	capture.Flags().StringVar(&target.ID, "session", "", "session id")
	capture.Flags().StringVar(&target.Path, "path", "", "transcript path")
	capture.Flags().StringVar(&target.Repository, "repository", "", "repository the session belongs to (owner/name)")
	capture.Flags().BoolVar(&options.DryRun, "dry-run", false, "stop before the model call and write nothing")
	capture.Flags().StringVar(&options.Backend, "backend", "", "backend: claude, codex or anthropic")
	capture.Flags().StringVar(&options.Model, "model", "", "model")
	_ = capture.MarkFlagRequired("session")
	cmd.AddCommand(capture)
	return cmd
}

func runDecisionCapture(ctx context.Context, cmd *cobra.Command, target decisionlog.Target, options decisionlog.Options) error {
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return err
	}
	var registry decisionlog.Registry = defaultRegistry{}
	if found, err := repos.OpenReadOnly(""); err == nil {
		defer found.Close()
		registry = found
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	state, err := jobs.Directory()
	if err != nil {
		return err
	}
	store, err := decisionlog.Open(filepath.Join(state, "decisions", "decisions.sqlite"))
	if err != nil {
		return err
	}
	defer store.Close()
	if target.Path == "" && target.Agent == livesessions.AgentClaudeCode {
		matches, _ := filepath.Glob(filepath.Join(transcript.UserHome(), ".claude", "projects", "*", target.ID+".jsonl"))
		if len(matches) > 0 {
			target.Path = matches[0]
		}
	}
	capturer := &decisionlog.Capturer{Store: store, Registry: registry, Config: cfg}
	report, err := capturer.Capture(ctx, target, options)
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); err == nil {
		err = encodeErr
	}
	return err
}

// defaultRegistry stands for a registry that does not exist yet.
type defaultRegistry struct{}

func (defaultRegistry) Preferences(context.Context) (repos.Preferences, error) {
	return repos.DefaultPreferences(), nil
}

func (defaultRegistry) RepositorySettings(context.Context, string) (repos.Settings, bool, error) {
	return repos.Settings{}, false, nil
}
