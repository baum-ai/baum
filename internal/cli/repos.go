package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

func newReposCommand() *cobra.Command {
	root := &cobra.Command{Use: "repos", Short: "Register existing local GitHub clones and manage repository settings"}
	open := func() (*repos.Registry, error) { return repos.Open("") }
	add := &cobra.Command{Use: "add <path>", Short: "Register a local clone (automatic generation follows the global setting, on by default)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		if cmd.Flags().Changed("daily-cap") && cmd.Flags().Changed("clear-daily-cap") {
			return fmt.Errorf("--daily-cap and --clear-daily-cap cannot be combined")
		}
		if cmd.Flags().Changed("automatic") && cmd.Flags().Changed("clear-automatic") {
			return fmt.Errorf("--automatic and --clear-automatic cannot be combined")
		}
		c, e := r.AddPatch(cmd.Context(), args[0], func(s repos.Settings) (repos.Settings, error) {
			if cmd.Flags().Changed("automatic") {
				v, _ := cmd.Flags().GetBool("automatic")
				s.Automatic = &v
			}
			if clear, _ := cmd.Flags().GetBool("clear-automatic"); clear {
				s.Automatic = nil
			}
			if cmd.Flags().Changed("daily-cap") {
				v, _ := cmd.Flags().GetFloat64("daily-cap")
				s.DailyCap = &v
			}
			if clear, _ := cmd.Flags().GetBool("clear-daily-cap"); clear {
				s.DailyCap = nil
			}
			if cmd.Flags().Changed("backend") {
				v, _ := cmd.Flags().GetString("backend")
				s.Backend = nil
				if v != "" {
					b, e := config.ParseBackend(v)
					if e != nil {
						return s, e
					}
					s.Backend = &b
				}
			}
			if cmd.Flags().Changed("model") {
				v, _ := cmd.Flags().GetString("model")
				s.Model = nil
				if v != "" {
					s.Model = &v
				}
			}
			return s, s.Validate()
		})
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(cmd.OutOrStdout(), c.ID)
		return e
	}}
	add.Flags().Bool("automatic", false, "generate reviews of this repository automatically (--automatic=false turns it off), whatever the global setting")
	add.Flags().Bool("clear-automatic", false, "follow the global automatic generation setting again")
	add.Flags().Float64("daily-cap", 0, "daily automatic spending cap in USD (zero permits no spend)")
	add.Flags().Bool("clear-daily-cap", false, "remove the daily cap override so the repository inherits again")
	add.Flags().String("backend", "", "repository backend override (empty resets to global)")
	add.Flags().String("model", "", "repository model override (empty resets to global)")
	remove := &cobra.Command{Use: "remove <path-or-id>", Short: "Remove a registered clone, even if its path is gone", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		return r.Remove(cmd.Context(), args[0])
	}}
	list := &cobra.Command{Use: "list", Short: "List clones, availability and repository settings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		clones, e := r.List(cmd.Context())
		if e != nil {
			return e
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(clones)
		}
		for _, c := range clones {
			status := "unavailable"
			if c.Available {
				status = "available"
			}
			data, _ := json.Marshal(c.Settings)
			if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Repository, c.Path, c.CommonDir, status, data); e != nil {
				return e
			}
		}
		return nil
	}}
	list.Flags().Bool("json", false, "print structured clone records and settings")
	root.AddCommand(add, remove, list)
	return root
}
