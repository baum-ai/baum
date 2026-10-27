package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/repos"
)

// reviewSettings is what "reviews show" prints: the effective choices, the
// app's preferences over the config file.
type reviewSettings struct {
	Backend      config.Backend `json:"backend"`
	Model        string         `json:"model"`
	Automatic    bool           `json:"automatic"`
	PullRequests string         `json:"pull_requests"`
	// DecisionsModel is the decision capture model the app chose, empty for
	// the backend's default.
	DecisionsModel string `json:"decisions_model"`
	// DecisionsEffectiveModel is the model capture uses with the effective
	// backend (decisionlog.EffectiveModel), and DecisionsModelIgnored says
	// it does not use DecisionsModel.
	DecisionsEffectiveModel string `json:"decisions_effective_model"`
	DecisionsModelIgnored   bool   `json:"decisions_model_ignored"`
}

const pullRequestsBoth = "both"

func newReviewsCommand() *cobra.Command {
	root := &cobra.Command{Use: "reviews", Short: "Show or change how the app generates reviews"}
	open := func() (*repos.Registry, error) { return repos.Open("") }
	show := &cobra.Command{Use: "show", Short: "Print the effective review settings as JSON", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		prefs, e := r.Preferences(cmd.Context())
		if e != nil {
			return e
		}
		cfg, e := config.Load(config.Overrides{})
		if e != nil {
			return e
		}
		effective := decisionlog.EffectiveModel(cmd.Context(), cfg, prefs, generate.Backends{}, nil)
		cfg = prefs.Resolve(cfg)
		settings := reviewSettings{
			Backend:      cfg.Backend,
			Model:        cfg.Model,
			Automatic:    prefs.Automatic,
			PullRequests: cmp.Or(prefs.PullRequests, pullRequestsBoth),
		}
		if prefs.Decisions.Model != nil {
			settings.DecisionsModel = *prefs.Decisions.Model
		}
		settings.DecisionsEffectiveModel, settings.DecisionsModelIgnored = effective.Model, effective.Ignored
		return json.NewEncoder(cmd.OutOrStdout()).Encode(settings)
	}}
	set := &cobra.Command{Use: "set", Short: "Change the backend, model, automatic generation, which pull requests it covers or the decision capture model", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		flags := cmd.Flags()
		var backend *config.Backend
		if flags.Changed("backend") {
			v, _ := flags.GetString("backend")
			b, e := config.ParseBackend(v)
			if e != nil {
				return e
			}
			backend = &b
		}
		var pullRequests *string
		if flags.Changed("pull-requests") {
			v, _ := flags.GetString("pull-requests")
			v = strings.ToLower(strings.TrimSpace(v))
			if v == pullRequestsBoth {
				v = ""
			}
			pullRequests = &v
		}
		r, e := open()
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		var invalid error
		_, e = r.UpdatePreferences(cmd.Context(), func(p repos.Preferences) repos.Preferences {
			next := p
			if backend != nil {
				next.Backend = backend
			}
			if flags.Changed("model") {
				v, _ := flags.GetString("model")
				v = strings.TrimSpace(v)
				next.Model = &v
			}
			if flags.Changed("automatic") {
				next.Automatic, _ = flags.GetBool("automatic")
			}
			if pullRequests != nil {
				next.PullRequests = *pullRequests
			}
			if flags.Changed("decisions-model") {
				v, _ := flags.GetString("decisions-model")
				next.Decisions.Model = nil
				if v = strings.TrimSpace(v); v != "" {
					next.Decisions.Model = &v
				}
			}
			// Choosing cursor-agent here is the acknowledgement that its
			// cost cannot be estimated, so its automatic reviews run.
			if backend != nil && *backend == config.BackendCursorAgent {
				next.CursorAgentAcknowledged = true
			}
			if invalid = next.Validate(); invalid != nil {
				return p
			}
			return next
		})
		if e != nil {
			return e
		}
		if invalid != nil {
			return invalid
		}
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "Saved review settings.")
		return e
	}}
	set.Flags().String("backend", "", "backend for reviews the app generates: "+strings.Join(backendNames(), ", "))
	set.Flags().String("model", "", "model for reviews the app generates (empty uses the backend's default)")
	set.Flags().Bool("automatic", true, "generate reviews automatically (--automatic=false turns it off)")
	set.Flags().String("pull-requests", "", "which pull requests get automatic reviews: authored, requested, both or all")
	set.Flags().String("decisions-model", "", "model for capturing the decisions of agent sessions (empty uses the backend's default)")
	root.AddCommand(show, set)
	return root
}

func backendNames() []string {
	names := make([]string, len(config.Backends))
	for i, b := range config.Backends {
		names[i] = string(b)
	}
	return names
}
