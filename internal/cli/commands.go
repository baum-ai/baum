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
		Long: `View serves the embedded viewer for a baum.review/1 document on 127.0.0.1 and
opens it in the browser. It reads the file given, or the built-in sample, and
never contacts a server or fetches anything. When the document's pull request
belongs to the clone containing the current directory and the clone already has
its commits, the viewer also shows the coding-agent sessions on this machine
linked to them, with their text redacted. A document with schema or rule
findings is still served, and the findings print to stderr. The document must
be valid JSON of at most 32 MiB. Give exactly one of a file, --sample and
--pr.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runView,
	}
	cmd.Flags().Bool("sample", false, "serve the built-in example document")
	cmd.Flags().Int("pr", 0, "view the latest generated document for this pull request (needs the local version store, not in this build)")
	cmd.Flags().Int("port", 0, "listen on this port instead of a free one")
	cmd.Flags().Bool("no-open", false, "print the URL without opening a browser")
	cmd.Flags().Bool("watch", false, "reload the viewer when the document file changes")
	cmd.Flags().String("claude-dir", "", "Claude Code projects directory (default ~/.claude/projects)")
	cmd.Flags().String("codex-dir", "", "Codex sessions directory (default ~/.codex/sessions)")
	cmd.Flags().StringArray("cursor-store", nil, "Cursor state.vscdb file or directory holding one (repeatable)")
	for _, hidden := range []string{"claude-dir", "codex-dir", "cursor-store"} {
		_ = cmd.Flags().MarkHidden(hidden)
	}
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
