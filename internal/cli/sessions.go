package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/redact"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/vendors"
)

type sessionsOptions struct {
	json          bool
	vendor        string
	since         string
	previousPaths []string
	dump          string
	claudeDir     string
	codexDir      string
	cursorStores  []string
}

func newSessionsCommand() *cobra.Command {
	var opts sessionsOptions
	cmd := &cobra.Command{
		Use:   "sessions [dir]",
		Short: "List coding-agent sessions recorded for the repository containing dir",
		Long: `Sessions lists the Claude Code, Codex, Cursor and Entire sessions recorded on
this machine for the git repository containing dir (default: the current
directory), with the commits and files each one touched. A session recorded in
any working tree of the clone, the main checkout or one of its git worktrees,
counts, whichever of them dir is in. Each Entire session is listed once, from
the newest checkpoint holding its transcript. Nothing leaves the machine; any text
printed is redacted first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runSessions(cmd, dir, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "print sessions as JSON")
	cmd.Flags().StringVar(&opts.vendor, "vendor", "", "only sessions from this source: claude_code, codex, cursor or entire")
	cmd.Flags().StringVar(&opts.since, "since", "", "only sessions modified after this time: a duration such as 72h, or a date such as 2026-09-01")
	cmd.Flags().StringArrayVar(&opts.previousPaths, "previous-path", nil, "directory the repository used to live at (repeatable)")
	cmd.Flags().StringVar(&opts.dump, "dump", "", "print the redacted session graph of this session id instead of the list")
	cmd.Flags().StringVar(&opts.claudeDir, "claude-dir", "", "Claude Code projects directory (default ~/.claude/projects)")
	cmd.Flags().StringVar(&opts.codexDir, "codex-dir", "", "Codex sessions directory (default ~/.codex/sessions)")
	cmd.Flags().StringArrayVar(&opts.cursorStores, "cursor-store", nil, "Cursor state.vscdb file or directory holding one (repeatable)")
	for _, hidden := range []string{"claude-dir", "codex-dir", "cursor-store"} {
		_ = cmd.Flags().MarkHidden(hidden)
	}
	return cmd
}

// sessionRow is one listed session, and the JSON shape of --json; "baum view"
// summarises sessions with the same code.
type sessionRow = locallink.Row

func runSessions(cmd *cobra.Command, dir string, opts sessionsOptions) error {
	load := sessions.Options{
		PreviousPaths:     opts.previousPaths,
		ClaudeProjectsDir: opts.claudeDir,
		CodexSessionsDir:  opts.codexDir,
		CursorStorePaths:  opts.cursorStores,
	}
	if opts.vendor != "" {
		source, err := sessions.ParseSource(opts.vendor)
		if err != nil {
			return err
		}
		load.Sources = []vendors.Slug{source}
	}
	if opts.since != "" {
		since, err := parseSince(opts.since, time.Now())
		if err != nil {
			return err
		}
		load.Since = since
	}
	engine, err := redact.New()
	if err != nil {
		return err
	}

	found, report, err := sessions.Load(cmd.Context(), dir, load)
	if err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	for _, skipped := range report.Skipped {
		line, _, err := engine.Redact(fmt.Sprintf("warning: skipped %s: %s", skipped.Path, skipped.Reason))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stderr, line)
	}
	for _, failure := range report.Failures {
		line, _, err := engine.Redact(fmt.Sprintf("warning: %s session %s: %v", failure.Source, failure.Path, failure.Err))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stderr, line)
	}

	if opts.dump != "" {
		return dumpSession(cmd.Context(), cmd.OutOrStdout(), engine, found, opts.dump)
	}
	rows := make([]sessionRow, 0, len(found))
	for _, s := range found {
		row, err := locallink.RowOf(engine, s)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), rows)
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no sessions recorded for this repository")
		return nil
	}
	return writeSessionTable(cmd.OutOrStdout(), rows)
}

func dumpSession(ctx context.Context, w io.Writer, engine *redact.Engine, found []sessions.Session, id string) error {
	for _, s := range found {
		if s.ID != id {
			continue
		}
		redacted, err := locallink.RedactGraph(ctx, engine, s.Graph, true)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(redacted))
		return err
	}
	return fmt.Errorf("no session %s recorded for this repository", id)
}

func writeSessionTable(w io.Writer, rows []sessionRow) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tSESSION\tTITLE\tBRANCH\tSTARTED\tENDED\tOUTCOME\tCOMMITS\tEDITED")
	for _, row := range rows {
		source := row.Source
		if row.Vendor != "" && row.Vendor != row.Source {
			source += "/" + row.Vendor
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			source, row.ID, truncate(row.Title, 48), row.Branch,
			formatTime(row.StartedAt), formatTime(row.EndedAt), row.Outcome, formatCommits(row.Commits), row.EditedFiles)
	}
	return tw.Flush()
}

func formatCommits(byRole map[string][]string) string {
	if len(byRole) == 0 {
		return "-"
	}
	roles := make([]string, 0, len(byRole))
	for role := range byRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		parts = append(parts, fmt.Sprintf("%s %d", role, len(byRole[role])))
	}
	return strings.Join(parts, ", ")
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func truncate(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	cut := limit - 1
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// parseSince accepts a duration back from now or a date.
func parseSince(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return time.Time{}, errors.New("--since duration must be positive")
		}
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: use a duration such as 72h or a date such as 2026-09-01", value)
}

func writeJSON(w io.Writer, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}
