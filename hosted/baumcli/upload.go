package baumcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/apiclient"
	"github.com/baum-ai/baum/hosted/traceupload"
	"github.com/baum-ai/baum/sessions/collector"
)

func newUploadCommand(stdout, stderr io.Writer) *cobra.Command {
	return newUploadCommandWithResult(stdout, stderr, nil)
}

func newUploadCommandWithResult(stdout, stderr io.Writer, uploaded *bool) *cobra.Command {
	var (
		server        string
		previousPaths []string
		all           bool
		yes           bool
	)
	cmd := &cobra.Command{
		Use:   "upload [dir]",
		Short: "Upload locally recorded agent sessions for a repository",
		Long: `Finds Claude Code, Codex, and Cursor sessions recorded in the git repository
containing [dir] (default: the current directory), redacts detected secrets
locally, and uploads a compressed archive to the connected repository.

If the repository moved, pass each prior location with --previous-path so
sessions recorded at those locations are included.

--all uploads every repository with a local checkout that has recorded agent
sessions instead of just the one containing [dir].`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("expected at most one directory, got %d arguments", len(args))
			}
			if all && len(args) > 0 {
				return errors.New("--all does not take a directory argument")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				return uploadAllRepositories(cmd.Context(), cmd.InOrStdin(), stdout, stderr, server, yes, uploaded)
			}
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			root, err := collector.RepoRoot(dir)
			if err != nil {
				return err
			}
			sessions, report, err := collector.FindSessions(root, collector.FindOptions{PreviousPaths: previousPaths, ExcludeNestedClones: true})
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				// Nothing found because nothing is readable is a wrong answer,
				// not an empty one, and it must not exit 0 as if the store were
				// empty.
				if len(report.Skipped) > 0 {
					return unreadableSessionsError(report)
				}
				_, _ = fmt.Fprintln(stdout, "No agent sessions found.")
				return nil
			}
			writeSkippedSessionFiles(stderr, report)
			writeUploadSessionSummary(stdout, sessions)
			if !yes {
				confirmed, err := confirmUpload(cmd.InOrStdin(), stdout)
				if err != nil {
					return err
				}
				if !confirmed {
					_, _ = fmt.Fprintln(stdout, "Upload cancelled.")
					return nil
				}
			}

			client, _, err := authenticatedClient(server)
			if err != nil {
				return err
			}
			fullName, err := originRepository(cmd.Context(), root)
			if err != nil {
				return err
			}
			repository, err := client.FindGitHubRepository(cmd.Context(), fullName)
			if errors.Is(err, apiclient.ErrUnauthenticated) {
				return errors.New("token is expired or revoked; run `baum login`")
			}
			if err != nil {
				return err
			}
			if !repository.Enabled {
				writeRepositoryDisabled(stdout, repository, "upload")
				return nil
			}

			inventory := verifiedRepositoryRootInventory(
				cmd.Context(), root, repository, previousPaths, sessions,
			)
			if err := postRepositoryTraces(cmd.Context(), client, repository, sessions, inventory, stdout, stderr); err != nil {
				return err
			}
			if uploaded != nil {
				*uploaded = true
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&server, "server", "", "server base URL")
	cmd.Flags().StringArrayVar(&previousPaths, "previous-path", nil, "prior filesystem location of the repository (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "upload every repository with a local checkout that has recorded agent sessions")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.MarkFlagsMutuallyExclusive("all", "previous-path")
	return cmd
}

// uploadPlanRow is one line of the --all upload plan: a local checkout and
// how many agent sessions it holds, keyed by the repository full name.
type uploadPlanRow struct {
	fullName   string
	repository collector.Repository
}

func uploadAllRepositories(ctx context.Context, in io.Reader, stdout, stderr io.Writer, server string, yes bool, uploaded *bool) error {
	local, report, err := collector.FindRepositories(ctx, collector.StoreOptions{})
	if err != nil {
		return err
	}
	if len(report.Skipped) > 0 {
		if len(local) == 0 {
			return unreadableSessionsError(report)
		}
		writeSkippedSessionFiles(stderr, report)
	}
	if len(local) == 0 {
		_, _ = fmt.Fprintln(stdout, "No agent sessions found.")
		return nil
	}

	rows := make([]uploadPlanRow, 0, len(local))
	for fullName, repository := range local {
		rows = append(rows, uploadPlanRow{fullName: fullName, repository: repository})
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].fullName) < strings.ToLower(rows[j].fullName)
	})
	if err := writeUploadPlan(stdout, rows); err != nil {
		return err
	}
	for _, row := range rows {
		for _, other := range row.repository.OtherCheckouts {
			_, _ = fmt.Fprintf(stderr, "warning: %s also has sessions at %s; using %s\n", row.fullName, other, row.repository.Root)
		}
	}

	if !yes {
		confirmed, confirmErr := confirmUpload(in, stdout)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(stdout, "Upload cancelled.")
			return nil
		}
	}

	client, _, err := authenticatedClient(server)
	if err != nil {
		return err
	}

	resolved := make(map[string]apiclient.GitHubRepository, len(rows))
	resolveErrors := make(map[string]error)
	var inventory []traceupload.RepositoryRoots
	for _, row := range rows {
		repository, findErr := client.FindGitHubRepository(ctx, row.fullName)
		if errors.Is(findErr, apiclient.ErrUnauthenticated) {
			return errors.New("token is expired or revoked; run `baum login`")
		}
		if findErr != nil {
			resolveErrors[row.fullName] = findErr
			continue
		}
		resolved[row.fullName] = repository
		inventory = append(inventory, traceupload.RepositoryRoots{
			ID: repository.ID, FullName: repository.FullName, Roots: row.repository.Roots,
		})
	}
	if len(resolveErrors) > 0 {
		// A shortened inventory would authoritatively withdraw evidence for a
		// checkout that only failed to resolve on this invocation.
		inventory = nil
	}

	succeeded, skipped, failed := 0, 0, 0
	for _, row := range rows {
		repository := resolved[row.fullName]
		findErr := resolveErrors[row.fullName]
		if findErr == nil && !repository.Enabled {
			_, _ = fmt.Fprintln(stdout)
			writeRepositoryDisabled(stdout, repository, "upload")
			skipped++
			continue
		}
		_, _ = fmt.Fprintf(stdout, "\nUploading %s at %s...\n", row.fullName, row.repository.Root)
		if findErr == nil {
			var uploadedRepo bool
			uploadedRepo, findErr = uploadRepositoryTraces(ctx, client, row.repository.Root, repository, nil, inventory, stdout, stderr)
			if findErr == nil && uploadedRepo && uploaded != nil {
				*uploaded = true
			}
		}
		if findErr != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "error: upload failed for %s: %v\n", row.fullName, findErr)
			continue
		}
		succeeded++
	}
	_, _ = fmt.Fprintf(stdout, "\nUpload complete: %d succeeded, %d skipped, %d failed.\n", succeeded, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("upload failed for %d %s", failed, repositoryNoun(failed))
	}
	return nil
}

func writeUploadPlan(w io.Writer, rows []uploadPlanRow) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "REPOSITORY\tSESSIONS\tLOCAL PATH"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%d\t%s\n", row.fullName, row.repository.SessionCount, row.repository.Root); err != nil {
			return err
		}
	}
	return table.Flush()
}

func uploadRepositoryTraces(ctx context.Context, client *apiclient.Client, root string, repository apiclient.GitHubRepository, previousPaths []string, inventory []traceupload.RepositoryRoots, stdout, stderr io.Writer) (bool, error) {
	sessions, report, err := collector.FindSessions(root, collector.FindOptions{PreviousPaths: previousPaths, ExcludeNestedClones: true})
	if err != nil {
		return false, err
	}
	if len(sessions) == 0 {
		if len(report.Skipped) > 0 {
			return false, unreadableSessionsError(report)
		}
		_, _ = fmt.Fprintln(stdout, "No agent sessions found.")
		return false, nil
	}
	writeSkippedSessionFiles(stderr, report)
	return true, postRepositoryTraces(ctx, client, repository, sessions, inventory, stdout, stderr)
}

func verifiedRepositoryRootInventory(ctx context.Context, root string, repository apiclient.GitHubRepository, previousPaths []string, sessions []collector.Session) []traceupload.RepositoryRoots {
	candidates := append([]string{root}, previousPaths...)
	for _, session := range sessions {
		candidates = append(candidates, session.RepoRoot, session.WorktreeRoot)
	}
	return []traceupload.RepositoryRoots{{
		ID: repository.ID, FullName: repository.FullName,
		Roots: collector.VerifyRepositoryRoots(ctx, repository.FullName, candidates),
	}}
}

// maxUnpackedBytes is the most the server unpacks from one archive. A test
// lowers it.
var maxUnpackedBytes = traceupload.MaxUnpackedBytes

func postRepositoryTraces(ctx context.Context, client *apiclient.Client, repository apiclient.GitHubRepository, sessions []collector.Session, inventory []traceupload.RepositoryRoots, stdout, stderr io.Writer) error {
	progress := newPreparationProgress(stderr, len(sessions))
	progress.Start()
	bundle, err := traceupload.BuildWithProgress(ctx, sessions, traceupload.Repository{
		ID: repository.ID, FullName: repository.FullName, RootInventory: inventory,
	}, version(), progress.Update)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Finish()
	defer func() { _ = bundle.Close() }()

	sessionCount := len(bundle.Manifest.Sessions)
	// The server would accept an oversized archive and only fail it once its
	// worker unpacked it, so it is refused here, before any upload.
	if bundle.UnpackedSize > maxUnpackedBytes {
		return fmt.Errorf("the %d %s for %s unpack to %s, over the %s the server accepts in one upload; nothing was uploaded",
			sessionCount, sessionNoun(sessionCount), repository.FullName,
			humanize.IBytes(uint64(bundle.UnpackedSize)), humanize.IBytes(uint64(maxUnpackedBytes)))
	}

	counts := map[string]int{}
	for _, session := range bundle.Manifest.Sessions {
		counts[string(session.Source)]++
	}
	findings := 0
	for _, count := range bundle.Manifest.Redaction.FindingsByType {
		findings += count
	}
	_, _ = fmt.Fprintf(stdout, "Uploading %d %s for %s (claude_code=%d, codex=%d, cursor=%d; redacted findings=%d; archive=%s)\n",
		sessionCount, sessionNoun(sessionCount), repository.FullName, counts["claude_code"], counts["codex"], counts["cursor"], findings, humanize.IBytes(uint64(bundle.Size)))
	if err := client.UploadRepositoryTraces(ctx, repository.ID, bundle.File, bundle.Size); err != nil {
		if errors.Is(err, apiclient.ErrUnauthenticated) {
			return errors.New("token is expired or revoked; run `baum login`")
		}
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Uploaded %d %s for %s.\n", sessionCount, sessionNoun(sessionCount), repository.FullName)
	return nil
}

func originRepository(ctx context.Context, root string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if _, ok := errors.AsType[*exec.Error](err); ok {
			return "", errors.New("git is required to resolve the repository origin")
		}
		return "", errors.New("repository has no origin remote; add a GitHub origin remote")
	}
	fullName, err := normalizeGitHubRemote(strings.TrimSpace(string(output)))
	if err != nil {
		return "", err
	}
	return fullName, nil
}

func normalizeGitHubRemote(remote string) (string, error) {
	return collector.NormalizeGitHubRemote(remote)
}
