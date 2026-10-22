package cli

import "github.com/spf13/cobra"

// newInternalCommand groups the engine commands behind "baum internal". They
// are debugging and diagnostic tools for working on baum itself; the desktop
// app and the hooks are what users run, so the group stays out of the
// top-level help.
func newInternalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "internal",
		Short: "Debugging and diagnostic commands for the baum engine",
		Long: `The internal commands run the engine behind the desktop app by hand: they
generate, bundle, view and validate review documents, list sessions and
registered clones, and report on the machine. They exist for debugging and
diagnosis and are not part of the everyday workflow, which is the desktop app
and the agent hooks.`,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newGenerateCommand(),
		newBundleCommand(),
		newViewCommand(),
		newValidateCommand(),
		newRefreshCommand(),
		newSessionsCommand(),
		newReposCommand(),
		newReviewsCommand(),
		newDoctorCommand(),
	)
	return cmd
}
