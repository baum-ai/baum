package baumcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/apiclient"
	"github.com/baum-ai/baum/hosted/traceupload"
	"github.com/baum-ai/baum/sessions/collector"
)

// setupScanDepth bounds how deep `setup --all` looks for checkouts below the
// working directory.
const setupScanDepth = 3

func newSetupCommand(stdout, stderr io.Writer) *cobra.Command {
	var (
		server        string
		previousPaths []string
		all           bool
		yes           bool
		skipEntire    bool
		skipUserHooks bool
		syncRepo      bool
		upload        bool
	)
	cmd := &cobra.Command{
		Use:   "setup [PATH...]",
		Short: "Install baum and Entire hooks in local repositories",
		Long: `Installs baum's hooks in git repositories without signing in. Setup
never contacts the server unless --sync or --upload is passed. With no PATH it
sets up the current repository; with PATHs it sets up each checkout. With --all
it finds checkouts from local agent session metadata and from Git checkouts up
to three levels below the current directory, then asks once before changing
anything. Every repository needs a GitHub origin. Setup installs and enables
Entire for detected coding agents unless --skip-entire is set. For a hosted
install or a logged-in user it also installs baum's user-wide Claude Code and
Codex marketplace plugins (see baum hooks install --user) unless --skip-user-hooks or
--skip-entire is set.

--sync requests a server-side GitHub repository sync and --upload uploads
locally recorded agent sessions. Both need credentials: run baum login first.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("--all cannot be combined with checkout paths")
			}
			if len(previousPaths) > 0 && !upload {
				return errors.New("--previous-path requires --upload")
			}
			if len(previousPaths) > 0 && len(args) > 1 {
				return errors.New("--previous-path applies to a single repository")
			}
			var client *apiclient.Client
			if syncRepo || upload {
				var err error
				client, _, err = authenticatedClient(server)
				if err != nil {
					return err
				}
			}
			// One reader for every prompt: a piped stdin is buffered whole by
			// the first one, which would leave the next prompts at EOF.
			in := newPromptInput(cmd.InOrStdin())
			opts := setupRepositoryOptions{
				yes: yes, skipEntire: skipEntire, sync: syncRepo, upload: upload,
				previousPaths: previousPaths,
			}
			if !skipEntire && !skipUserHooks && setupInstallsUserHooks() {
				opts.userHooks = &userHooksStep{}
			}
			if all {
				opts.aggregateConfirmed = true
				return setupAllRepositories(cmd.Context(), in, client, opts, stdout, stderr)
			}
			if len(args) == 0 {
				args = []string{"."}
			}
			roots := make([]string, 0, len(args))
			seenRoots := map[string]bool{}
			for _, path := range args {
				root, err := collector.RepoRoot(path)
				if err != nil {
					return err
				}
				// Two paths can reach one checkout through a symlink.
				key := canonicalPath(root)
				if seenRoots[key] {
					continue
				}
				seenRoots[key] = true
				roots = append(roots, root)
			}
			if len(roots) == 1 {
				return setupRepository(cmd.Context(), in, client, roots[0], apiclient.GitHubRepository{}, opts, stdout, stderr)
			}
			succeeded, failed := 0, 0
			for _, root := range roots {
				_, _ = fmt.Fprintf(stdout, "\nSetting up %s...\n", root)
				if err := setupRepository(cmd.Context(), in, client, root, apiclient.GitHubRepository{}, opts, stdout, stderr); err != nil {
					failed++
					_, _ = fmt.Fprintf(stderr, "error: setup failed for %s: %v\n", root, err)
					continue
				}
				succeeded++
			}
			return writeSetupSummary(stdout, succeeded, failed)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&server, "server", "", "server base URL (only used with --sync or --upload)")
	cmd.Flags().BoolVar(&all, "all", false, "set up every GitHub checkout found locally")
	cmd.Flags().StringArrayVar(&previousPaths, "previous-path", nil, "prior filesystem location of the repository, with --upload (repeatable)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&skipEntire, "skip-entire", false, "skip installing and configuring Entire")
	cmd.Flags().BoolVar(&skipUserHooks, "skip-user-hooks", false, "skip user-scoped Claude Code and Codex marketplace plugin installation")
	cmd.Flags().BoolVar(&syncRepo, "sync", false, "request a server-side GitHub repository sync (requires login)")
	cmd.Flags().BoolVar(&upload, "upload", false, "upload locally recorded agent sessions (requires login)")
	cmd.MarkFlagsMutuallyExclusive("all", "previous-path")
	return cmd
}

type setupRepositoryOptions struct {
	yes                bool
	skipEntire         bool
	sync               bool
	upload             bool
	aggregateConfirmed bool
	previousPaths      []string
	inventory          []traceupload.RepositoryRoots
	// synced records repository IDs whose sync was already requested during
	// one --all run, so duplicate clones request it once.
	synced map[string]bool
	// userHooks is set when setup installs the user-wide hooks; every
	// repository of one run shares it, so they are installed once.
	userHooks *userHooksStep
}

type userHooksStep struct{ done bool }

type setupPlanRow struct {
	repository apiclient.GitHubRepository
	local      collector.Repository
	state      string
}

const (
	setupStateReady     = "ready"
	setupStateDone      = "already set up"
	setupStateNotLinked = "ready (not linked: no sync or upload)"
)

func writeSetupSummary(stdout io.Writer, succeeded, failed int) error {
	_, _ = fmt.Fprintf(stdout, "\nSetup complete: %d succeeded, %d failed.\n", succeeded, failed)
	if failed > 0 {
		return fmt.Errorf("setup failed for %d %s", failed, repositoryNoun(failed))
	}
	return nil
}

// discoverCheckouts lists local GitHub checkouts without the server: session
// metadata is a hint, and Git checkouts under the working directory fill in
// the rest. Results are deduplicated by symlink-resolved repository root. It
// also returns the directory it scanned.
func discoverCheckouts(ctx context.Context, stderr io.Writer) ([]collector.Repository, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	// WalkDir does not follow a symlinked root, so scan where it points.
	cwd = canonicalPath(cwd)
	if filepath.Dir(cwd) == cwd {
		return nil, "", errors.New("refusing to scan the filesystem root; run `baum setup --all` from a projects directory or pass checkout paths")
	}
	scanHome := false
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		scanHome = canonicalPath(cwd) == canonicalPath(home)
	}
	var found []collector.Repository
	seen := map[string]bool{}
	add := func(repository collector.Repository) {
		key := canonicalPath(repository.Root)
		if seen[key] {
			return
		}
		seen[key] = true
		found = append(found, repository)
	}
	local, report, err := collector.FindRepositories(ctx, collector.StoreOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: could not read local agent session metadata: %v\n", err)
	}
	writeSkippedSessionFiles(stderr, report)
	names := make([]string, 0, len(local))
	for name := range local {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repository := local[name]
		repository.FullName = name
		add(repository)
	}
	if root, rootErr := collector.RepoRoot(cwd); rootErr == nil {
		if fullName, originErr := originRepository(ctx, root); originErr == nil {
			add(collector.Repository{FullName: fullName, Root: root, Roots: []string{root}})
		}
	}
	err = filepath.WalkDir(cwd, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			return filepath.SkipDir
		}
		if rel != "." {
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if depth > setupScanDepth || skipScanDir(entry.Name()) {
				return filepath.SkipDir
			}
			if scanHome && depth == 1 && skipUserDataDir(entry.Name()) {
				return filepath.SkipDir
			}
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
			return nil
		}
		if fullName, originErr := originRepository(ctx, path); originErr == nil {
			add(collector.Repository{FullName: fullName, Root: path, Roots: []string{path}})
		}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, "", err
	}
	return found, cwd, nil
}

// canonicalPath resolves symlinks so one checkout reached two ways compares
// equal. It falls back to the cleaned path when resolution fails.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// skipUserDataDir reports well-known folders of a home directory that hold
// personal files or cloud mounts, not projects. They are skipped only when the
// scan starts at the home directory.
func skipUserDataDir(name string) bool {
	switch name {
	case "Desktop", "Documents", "Downloads", "Pictures", "Movies", "Music", "Public", "Library", "Applications", "iCloud Drive":
		return true
	}
	for _, prefix := range []string{"Google Drive", "Dropbox", "OneDrive"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func skipScanDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__", "Library", "Applications":
		return true
	}
	return false
}

func setupAllRepositories(ctx context.Context, in io.Reader, client *apiclient.Client, opts setupRepositoryOptions, stdout, stderr io.Writer) error {
	checkouts, scanRoot, err := discoverCheckouts(ctx, stderr)
	if err != nil {
		return err
	}
	linkedByName := map[string]apiclient.GitHubRepository{}
	if client != nil {
		linked, err := client.ListGitHubInstallations(ctx)
		if errors.Is(err, apiclient.ErrUnauthenticated) {
			return errors.New("token is expired or revoked; run `baum login`")
		}
		if err != nil {
			return err
		}
		for _, repository := range linked {
			linkedByName[strings.ToLower(repository.FullName)] = repository
		}
	}
	rows := make([]setupPlanRow, 0, len(checkouts))
	for _, checkout := range checkouts {
		row := setupPlanRow{repository: apiclient.GitHubRepository{FullName: checkout.FullName}, local: checkout, state: setupStateReady}
		if client != nil {
			if repository, ok := linkedByName[strings.ToLower(checkout.FullName)]; ok {
				row.repository = repository
			} else {
				row.state = setupStateNotLinked
			}
		}
		if row.state == setupStateReady && !opts.sync && !opts.upload {
			complete, markerErr := repositorySetupComplete(ctx, checkout.Root)
			if markerErr != nil {
				return markerErr
			}
			if complete {
				row.state = setupStateDone
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].repository.FullName), strings.ToLower(rows[j].repository.FullName)
		if a != b {
			return a < b
		}
		return rows[i].local.Root < rows[j].local.Root
	})
	_, _ = fmt.Fprintf(stdout, "Scanned %s (depth %d) and recorded sessions.\n", scanRoot, setupScanDepth)
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(stdout, "No GitHub checkouts found.")
		return nil
	}
	if err := writeSetupPlan(stdout, rows, opts.upload); err != nil {
		return err
	}
	if opts.upload {
		repositories, sessions := 0, 0
		for _, row := range rows {
			if row.state == setupStateDone || row.state == setupStateNotLinked || row.repository.ID == "" || !row.repository.Enabled || row.local.SessionCount == 0 {
				continue
			}
			repositories++
			sessions += row.local.SessionCount
		}
		_, _ = fmt.Fprintf(stdout, "Sessions will be uploaded for %d %s (%d %s).\n", repositories, repositoryNoun(repositories), sessions, sessionNoun(sessions))
	}
	ready := 0
	for _, row := range rows {
		for _, other := range row.local.OtherCheckouts {
			_, _ = fmt.Fprintf(stderr, "warning: %s also has sessions at %s; using %s\n", row.repository.FullName, other, row.local.Root)
		}
		if row.state != setupStateDone {
			ready++
		}
	}
	// User-wide hooks can be missing when every repository is set up, for
	// example after `baum hooks uninstall --user`, so they count as pending
	// work before the no-op return.
	userHooksPending := opts.userHooks != nil && !opts.userHooks.done && userHooksPendingForSetup(ctx)
	if ready == 0 && !userHooksPending {
		_, _ = fmt.Fprintln(stdout, "No repositories need setup.")
		return nil
	}
	if ready == 0 {
		_, _ = fmt.Fprintln(stdout, "No repositories need setup.")
	}
	if userHooksPending {
		writeUserHooksPlan(ctx, stdout)
	}
	if !opts.yes {
		confirmed, confirmErr := confirmUpload(in, stdout)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(stdout, "Setup cancelled.")
			return nil
		}
	}
	// One inventory entry per repository ID: duplicate clones merge roots.
	opts.synced = map[string]bool{}
	opts.inventory = make([]traceupload.RepositoryRoots, 0, len(rows))
	inventoryIndex := map[string]int{}
	for _, row := range rows {
		if row.repository.ID == "" || row.local.Root == "" {
			continue
		}
		index, ok := inventoryIndex[row.repository.ID]
		if !ok {
			inventoryIndex[row.repository.ID] = len(opts.inventory)
			opts.inventory = append(opts.inventory, traceupload.RepositoryRoots{
				ID: row.repository.ID, FullName: row.repository.FullName,
			})
			index = len(opts.inventory) - 1
		}
		entry := &opts.inventory[index]
		for _, root := range row.local.Roots {
			if !slices.Contains(entry.Roots, root) {
				entry.Roots = append(entry.Roots, root)
			}
		}
	}

	if ready == 0 {
		opts.userHooks.done = true
		installUserHooksDuringSetup(ctx, stdout, stderr)
		return nil
	}

	succeeded, failed := 0, 0
	for _, row := range rows {
		if row.state == setupStateDone {
			continue
		}
		rowOpts := opts
		if row.state == setupStateNotLinked {
			rowOpts.sync, rowOpts.upload = false, false
			_, _ = fmt.Fprintf(stdout, "\nSetting up %s at %s (not linked on the server: skipping sync and upload)...\n", row.repository.FullName, row.local.Root)
		} else {
			_, _ = fmt.Fprintf(stdout, "\nSetting up %s at %s...\n", row.repository.FullName, row.local.Root)
		}
		if err := setupRepository(ctx, in, client, row.local.Root, row.repository, rowOpts, stdout, stderr); err != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "error: setup failed for %s: %v\n", row.repository.FullName, err)
			continue
		}
		succeeded++
	}
	return writeSetupSummary(stdout, succeeded, failed)
}

func writeSetupPlan(w io.Writer, rows []setupPlanRow, showSessions bool) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	header := "REPOSITORY\tSTATE\tLOCAL PATH"
	if showSessions {
		header = "REPOSITORY\tSTATE\tSESSIONS\tLOCAL PATH"
	}
	if _, err := fmt.Fprintln(table, header); err != nil {
		return err
	}
	for _, row := range rows {
		path := "-"
		if row.local.Root != "" {
			path = row.local.Root
		}
		line := fmt.Sprintf("%s\t%s\t%s", row.repository.FullName, row.state, path)
		if showSessions {
			line = fmt.Sprintf("%s\t%s\t%d\t%s", row.repository.FullName, row.state, row.local.SessionCount, path)
		}
		if _, err := fmt.Fprintln(table, line); err != nil {
			return err
		}
	}
	return table.Flush()
}

// setupRepository installs hooks for the checkout at root. The repository
// identity comes from its GitHub origin remote. client is nil unless the user
// asked for --sync or --upload, the only steps that talk to the server. Hooks
// go in first, so a server error never blocks local setup.
func setupRepository(ctx context.Context, in io.Reader, client *apiclient.Client, root string, repository apiclient.GitHubRepository, opts setupRepositoryOptions, stdout, stderr io.Writer) error {
	if repository.FullName == "" {
		fullName, err := originRepository(ctx, root)
		if err != nil {
			return err
		}
		repository.FullName = fullName
	}
	if (opts.sync || opts.upload) && client == nil {
		return errors.New("not logged in; run `baum login`")
	}
	var err error
	if !opts.skipEntire {
		if configured, err := setupEntire(ctx, root, stdout, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: entire setup failed: %v\nCheckpoint recording is not configured; run `entire doctor` or re-run `baum setup`.\n", err)
		} else if configured {
			// The marker means hooks are configured, so only a successful
			// Entire setup that found an agent writes it; otherwise the next
			// run retries.
			complete, err := repositorySetupComplete(ctx, root)
			if err != nil {
				return err
			}
			if !complete {
				if err := markRepositorySetupComplete(ctx, root); err != nil {
					return err
				}
			}
		}
	}
	if opts.userHooks != nil && !opts.userHooks.done {
		opts.userHooks.done = true
		installUserHooksDuringSetup(ctx, stdout, stderr)
	}
	if opts.sync && repository.ID == "" {
		repository, err = client.FindGitHubRepository(ctx, repository.FullName)
		if errors.Is(err, apiclient.ErrUnauthenticated) {
			return errors.New("token is expired or revoked; run `baum login`")
		}
		if err != nil {
			return fmt.Errorf("hooks are set up, but looking up %s for sync failed: %w", repository.FullName, err)
		}
	}
	if opts.sync && !repository.Enabled {
		writeRepositoryDisabled(stdout, repository, "sync")
		opts.sync = false
	}
	if opts.sync && opts.synced[repository.ID] {
		_, _ = fmt.Fprintf(stdout, "Sync for %s was already requested.\n", repository.FullName)
		opts.sync = false
	}
	if opts.sync {
		if err := client.RequestGitHubRepositorySync(ctx, repository.ID); err != nil {
			if errors.Is(err, apiclient.ErrUnauthenticated) {
				return errors.New("token is expired or revoked; run `baum login`")
			}
			return fmt.Errorf("hooks are set up, but the sync request for %s failed: %w", repository.FullName, err)
		}
		if opts.synced != nil {
			opts.synced[repository.ID] = true
		}
		_, _ = fmt.Fprintf(stdout, "Requested sync for %s.\n", repository.FullName)
	}
	if !opts.upload {
		return nil
	}
	if repository.ID != "" && !repository.Enabled {
		writeRepositoryDisabled(stdout, repository, "upload")
		return nil
	}
	if !opts.aggregateConfirmed {
		sessions, report, findErr := collector.FindSessions(root, collector.FindOptions{PreviousPaths: opts.previousPaths, ExcludeNestedClones: true})
		if findErr != nil {
			return findErr
		}
		if len(sessions) == 0 {
			if len(report.Skipped) > 0 {
				return unreadableSessionsError(report)
			}
			_, _ = fmt.Fprintln(stdout, "No agent sessions found.")
			return nil
		}
		writeSkippedSessionFiles(stderr, report)
		writeUploadSessionSummary(stdout, sessions)
		if !opts.yes {
			confirmed, confirmErr := confirmUpload(in, stdout)
			if confirmErr != nil {
				return confirmErr
			}
			if !confirmed {
				_, _ = fmt.Fprintln(stdout, "Upload cancelled.")
				return nil
			}
		}
		if repository.ID == "" {
			repository, err = client.FindGitHubRepository(ctx, repository.FullName)
			if errors.Is(err, apiclient.ErrUnauthenticated) {
				return errors.New("token is expired or revoked; run `baum login`")
			}
			if err != nil {
				return err
			}
		}
		if !repository.Enabled {
			writeRepositoryDisabled(stdout, repository, "upload")
			return nil
		}
		inventory := verifiedRepositoryRootInventory(ctx, root, repository, opts.previousPaths, sessions)
		return postRepositoryTraces(ctx, client, repository, sessions, inventory, stdout, stderr)
	}
	// A checkout without sessions has nothing to upload; it is not a failure.
	// uploadRepositoryTraces already said so.
	_, err = uploadRepositoryTraces(ctx, client, root, repository, opts.previousPaths, opts.inventory, stdout, stderr)
	return err
}

// writeRepositoryDisabled explains why a server step was skipped. Hooks are
// still installed: the switch only controls what the server works on.
func writeRepositoryDisabled(stdout io.Writer, repository apiclient.GitHubRepository, step string) {
	_, _ = fmt.Fprintf(stdout, "Skipping %s for %s: not enabled in baum; turn it on in the repositories page.\n", step, repository.FullName)
}

func repositoryNoun(count int) string {
	if count == 1 {
		return "repository"
	}
	return "repositories"
}

func repositorySetupComplete(ctx context.Context, root string) (bool, error) {
	marker, err := repositorySetupMarker(ctx, root)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(marker); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("inspect repository setup marker: %w", err)
	}
}

func markRepositorySetupComplete(ctx context.Context, root string) error {
	marker, err := repositorySetupMarker(ctx, root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return fmt.Errorf("create repository setup state directory: %w", err)
	}
	if err := os.WriteFile(marker, []byte("complete\n"), 0o644); err != nil {
		return fmt.Errorf("mark repository setup complete: %w", err)
	}
	return nil
}

// setupMarkerName is the file in the repository's .git/baum directory saying
// setup configured its hooks.
const setupMarkerName = "setup-complete"

// repositorySetupMarker is the setup marker of the checkout containing root,
// in that worktree's own Git directory: setup tracks each worktree apart. The
// hook opt-in also reads the main worktree's marker from the common Git
// directory, so linked worktrees of a set-up clone are opted in too.
func repositorySetupMarker(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", commandError(cmd, err)
	}
	return filepath.Join(filepath.Clean(string(bytes.TrimSpace(output))), "baum", setupMarkerName), nil
}
