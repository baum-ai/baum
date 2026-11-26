package cli

import (
	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/credentialstore"
)

func newGitHubCredentialCommand() *cobra.Command {
	return &cobra.Command{Use: "github-credential", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return credentialstore.ServeCredentialHelper(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
	}}
}
