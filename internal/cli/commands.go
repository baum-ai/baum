package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newViewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view [document.json]",
		Short: "Serve the embedded viewer for a review document on localhost",
		Long: `View serves the embedded viewer for a baum.review/1 document on 127.0.0.1 and
opens it in the browser. It reads the file given, the built-in sample or a
locally stored pull request version selected by --pr (latest, or --version), and
never contacts a server or fetches anything. When the document's pull request
belongs to the clone containing the current directory and the clone already has
its commits, the viewer also shows the coding-agent sessions on this machine
linked to them, with their text redacted, and fills in the code excerpts the
document left out from the same clone's commits. A document with schema or rule
findings is still served, and the findings print to stderr. The document must
be valid JSON of at most 32 MiB. Give exactly one of a file, --sample and
--pr.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runView,
	}
	cmd.Flags().Bool("sample", false, "serve the built-in example document")
	cmd.Flags().Int("pr", 0, "view the latest generated document for this pull request")
	storedVersionFlags(cmd)
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
		Use:   "validate [document.json]",
		Short: "Validate a review document against the schema and its rules",
		Long: `Validate checks a stored baum.review/1 document against the schema and the
document's own rules, and prints one finding per line to stderr. It exits
non-zero when the document is invalid. With --bundle it also checks the
document against that bundle: its hunks, checks and sessions, so a session
restricted since the document was written is reported. Give a document file
or --pr for the latest local version; --version selects an earlier version.
--repo and --store override the repository and store only with --pr.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkStoredFlags(cmd); err != nil {
				return err
			}
			bundlePath, _ := cmd.Flags().GetString("bundle")
			if cmd.Flags().Changed("pr") {
				if len(args) != 0 {
					return fmt.Errorf("give exactly one of a document file and --pr")
				}
				_, _, v, err := loadStoredVersion(cmd)
				if err != nil {
					return err
				}
				return validateDocument(cmd, v.Document, fmt.Sprintf("stored version %d", v.Metadata.Version), bundlePath)
			}
			if len(args) != 1 {
				return fmt.Errorf("accepts 1 arg (document file), or --pr")
			}
			return runValidate(cmd, args[0], bundlePath)
		},
	}
	cmd.Flags().Int("pr", 0, "validate a stored pull request review")
	storedVersionFlags(cmd)
	cmd.Flags().String("bundle", "", "generation bundle to check the document against")
	return cmd
}
