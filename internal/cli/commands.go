package cli

import "github.com/spf13/cobra"

func newGenerateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate the review document for a pull request",
		Long: `Generate builds the generation bundle for a pull request from local sessions,
the local clone and GitHub, runs the review agent passes with the configured
model backend, validates the result and writes a baum.review/1 document.`,
		RunE: notImplemented,
	}
	cmd.Flags().Int("pr", 0, "pull request number")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (defaults to the origin remote)")
	cmd.Flags().String("backend", "", "model backend: auto, anthropic, claude, codex or cursor-agent")
	cmd.Flags().String("model", "", "model name for the selected backend")
	cmd.Flags().StringP("output", "o", "", "write the document to this file instead of stdout")
	return cmd
}

func newBundleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Assemble the generation input for a pull request without calling a model",
		RunE:  notImplemented,
	}
	cmd.Flags().Int("pr", 0, "pull request number")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (defaults to the origin remote)")
	cmd.Flags().StringP("output", "o", "", "write the bundle to this file instead of stdout")
	return cmd
}

func newViewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view [document.json]",
		Short: "Serve the embedded viewer for a review document on localhost",
		RunE:  notImplemented,
	}
	cmd.Flags().Int("pr", 0, "view the latest generated document for this pull request")
	cmd.Flags().Int("port", 0, "listen on this port instead of a free one")
	cmd.Flags().Bool("watch", false, "reload the viewer when the document changes")
	return cmd
}

func newValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <document.json>",
		Short: "Validate a review document against the schema and its rules",
		Long: `Validate checks a stored baum.review/1 document against the schema and the
document's own rules, and prints one finding per line to stderr. It exits
non-zero when the document is invalid. With --bundle it also checks the
document against that bundle: its hunks, checks and sessions, so a session
restricted since the document was written is reported.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bundlePath, _ := cmd.Flags().GetString("bundle")
			return runValidate(cmd, args[0], bundlePath)
		},
	}
	cmd.Flags().String("bundle", "", "generation bundle to check the document against")
	return cmd
}

func newRefreshCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Update checks, threads and session evidence in a generated document without a model call",
		RunE:  notImplemented,
	}
	cmd.Flags().Int("pr", 0, "pull request number")
	return cmd
}
