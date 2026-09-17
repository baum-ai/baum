package cli

import "github.com/spf13/cobra"

func newGenerateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate the review document for a pull request",
		Long: `Generate builds the generation bundle for a pull request from local sessions,
the local clone and GitHub, runs the review agent passes with the configured
model backend, validates the result and writes a recountable.review/1 document.`,
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
		RunE:  notImplemented,
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

func newSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions [dir]",
		Short: "List coding-agent sessions recorded for the repository containing dir",
		RunE:  notImplemented,
	}
	cmd.Flags().Bool("json", false, "print sessions as JSON")
	cmd.Flags().String("vendor", "", "only sessions from this vendor: claude_code, codex, cursor or entire")
	return cmd
}
