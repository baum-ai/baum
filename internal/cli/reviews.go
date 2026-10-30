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
	// Effort is the reviews' reasoning effort, empty for the agent's own.
	Effort string `json:"effort"`
	// FixBackend, FixModel and FixEffort are empty when fixes follow the
	// reviews.
	FixBackend string `json:"fix_backend"`
	FixModel   string `json:"fix_model"`
	FixEffort  string `json:"fix_effort"`
	// DecisionsChosenBackend and DecisionsModel are the data extraction
	// backend and model the app chose, decision capture's and the feature
	// log's: empty for the reviews' backend and its default model.
	DecisionsChosenBackend string `json:"decisions_chosen_backend"`
	DecisionsModel         string `json:"decisions_model"`
	// DecisionsEffort is data extraction's reasoning effort, empty for the
	// agent's own.
	DecisionsEffort string `json:"decisions_effort"`
	// DecisionsBackend and DecisionsEffectiveModel are the backend and model
	// extraction uses (decisionlog.EffectiveModel; empty when unknown), and
	// DecisionsModelIgnored says it does not use DecisionsModel.
	DecisionsBackend        string `json:"decisions_backend"`
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
			Effort:       cfg.Effort,
			Automatic:    prefs.Automatic,
			PullRequests: cmp.Or(prefs.PullRequests, pullRequestsBoth),
		}
		if prefs.FixBackend != nil {
			settings.FixBackend = string(*prefs.FixBackend)
		}
		if prefs.FixModel != nil {
			settings.FixModel = *prefs.FixModel
		}
		if prefs.FixEffort != nil {
			settings.FixEffort = *prefs.FixEffort
		}
		if prefs.Decisions.Effort != nil {
			settings.DecisionsEffort = *prefs.Decisions.Effort
		}
		if prefs.Decisions.Backend != nil {
			settings.DecisionsChosenBackend = string(*prefs.Decisions.Backend)
		}
		if prefs.Decisions.Model != nil {
			settings.DecisionsModel = *prefs.Decisions.Model
		}
		settings.DecisionsBackend, settings.DecisionsEffectiveModel, settings.DecisionsModelIgnored = effective.Backend, effective.Model, effective.Ignored
		return json.NewEncoder(cmd.OutOrStdout()).Encode(settings)
	}}
	set := &cobra.Command{Use: "set", Short: "Change the backend, model and reasoning effort of reviews, fixes and data extraction, automatic generation or which pull requests it covers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		var fixBackend *config.Backend
		clearFix := false
		if flags.Changed("fix-backend") {
			v, _ := flags.GetString("fix-backend")
			if strings.TrimSpace(v) == "" {
				clearFix = true
			} else {
				b, e := config.ParseBackend(v)
				if e != nil {
					return e
				}
				fixBackend = &b
			}
		}
		var decisionsBackend *config.Backend
		if flags.Changed("decisions-backend") {
			if v, _ := flags.GetString("decisions-backend"); strings.TrimSpace(v) != "" {
				b, e := config.ParseBackend(v)
				if e != nil {
					return e
				}
				decisionsBackend = &b
			}
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
			if clearFix {
				next.FixBackend, next.FixModel, next.FixEffort = nil, nil, nil
			}
			if fixBackend != nil {
				next.FixBackend = fixBackend
			}
			if flags.Changed("fix-model") {
				v, _ := flags.GetString("fix-model")
				v = strings.TrimSpace(v)
				next.FixModel = &v
			}
			for _, f := range []struct {
				flag   string
				target **string
			}{{"effort", &next.Effort}, {"fix-effort", &next.FixEffort}, {"decisions-effort", &next.Decisions.Effort}} {
				if flags.Changed(f.flag) {
					v, _ := flags.GetString(f.flag)
					if *f.target, invalid = config.ParseEffort(v); invalid != nil {
						return p
					}
				}
			}
			if flags.Changed("automatic") {
				next.Automatic, _ = flags.GetBool("automatic")
			}
			if pullRequests != nil {
				next.PullRequests = *pullRequests
			}
			if flags.Changed("decisions-backend") {
				next.Decisions.Backend = decisionsBackend
			}
			if flags.Changed("decisions-model") {
				v, _ := flags.GetString("decisions-model")
				next.Decisions.Model = nil
				if v = strings.TrimSpace(v); v != "" {
					next.Decisions.Model = &v
				}
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
	set.Flags().String("fix-backend", "", "agent that fixes pull requests: claude or codex (empty follows reviews again)")
	set.Flags().String("fix-model", "", "model for fixes (empty uses the fix agent's default)")
	efforts := strings.Join(config.Efforts, ", ")
	set.Flags().String("effort", "", "reasoning effort for reviews: "+efforts+" (empty uses the agent's own)")
	set.Flags().String("fix-effort", "", "reasoning effort for fixes: "+efforts+" (empty uses the fix agent's own)")
	set.Flags().String("decisions-effort", "", "reasoning effort for data extraction: "+efforts+" (empty uses the agent's own)")
	set.Flags().Bool("automatic", true, "generate reviews automatically (--automatic=false turns it off)")
	set.Flags().String("pull-requests", "", "which pull requests get automatic reviews: authored, requested, both or all")
	set.Flags().String("decisions-backend", "", "backend for data extraction, the decisions of agent sessions and the feature log: claude, codex or anthropic (empty follows reviews again)")
	set.Flags().String("decisions-model", "", "model for data extraction (empty uses the backend's default)")
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
