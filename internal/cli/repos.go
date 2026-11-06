package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/repos"
)

func newReposCommand() *cobra.Command {
	root := &cobra.Command{Use: "repos", Short: "Register an existing local GitHub clone for the desktop app", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	root.AddCommand(&cobra.Command{Use: "add <path>", Short: "Register a local clone", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := repos.Open("")
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		clone, err := r.AddPatch(cmd.Context(), args[0], func(s repos.Settings) (repos.Settings, error) { return s, nil })
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), clone.ID)
		return err
	}})
	return root
}
