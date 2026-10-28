package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// reviewSettings is what "reviews show" prints: the effective choices, the
// app's preferences over the config file.
type reviewSettings struct {
	Backend      config.Backend `json:"backend"`
	Model        string         `json:"model"`
	Automatic    bool           `json:"automatic"`
	PullRequests string         `json:"pull_requests"`
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
		cfg = prefs.Resolve(cfg)
		return json.NewEncoder(cmd.OutOrStdout()).Encode(reviewSettings{
			Backend:      cfg.Backend,
			Model:        cfg.Model,
			Automatic:    prefs.Automatic,
			PullRequests: cmp.Or(prefs.PullRequests, pullRequestsBoth),
		})
	}}
	set := &cobra.Command{Use: "set", Short: "Change the backend, model, automatic generation or which pull requests it covers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
