package cli

import "github.com/spf13/cobra"

// newInternalCommand contains the desktop's private support commands.
func newInternalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "internal", Short: "Desktop app support commands", Hidden: true,
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newReposCommand(), newReviewsCommand(), newDoctorCommand(), newGitHubCredentialCommand())
	return cmd
}
