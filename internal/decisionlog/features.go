package decisionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions"
)

// FeatureRegistry is what the feature worker reads of the repository
// registry; *repos.Registry is one.
type FeatureRegistry interface {
	Preferences(context.Context) (repos.Preferences, error)
	Repositories(context.Context) ([]repos.Repository, error)
	RepositorySettings(ctx context.Context, repository string) (repos.Settings, bool, error)
}

// FeatureWorker runs the feature log's stages for every repository whose
// decisions are captured (README.md, "Feature log"). Run it once in the
// daemon, beside the capture scheduler and on the same Store.
type FeatureWorker struct {
	Store    *Store
	Registry FeatureRegistry
	// GitHub opens a REST reader for a repository.
	GitHub func(context.Context, github.Repository) (github.Reader, error)
	// Sessions narrows the session stores linked; zero reads the defaults.
	Sessions sessions.Options
	// Config, Backends and Prices make the placement and text calls as
	// capture's (Capturer).
	Config   config.Config
	Backends generate.Backends
	Prices   func() *pricing.List
	Now      func() time.Time
	// Tick is how often the stages run; zero is FeatureTick.
	Tick time.Duration
	// Window is how far back the first sync lists pull requests; zero is
	// FeatureFirstBuildWindow.
	Window time.Duration
	Log    *slog.Logger

	once     sync.Once
	wake     chan struct{}
	redactor *bundle.Redactor
	initErr  error
	// windowLinked is each repository's last re-link of the session window,
	// retryAt when a failed repository is tried again; only Run's goroutine
	// touches them.
	windowLinked, retryAt map[string]time.Time
}

func (w *FeatureWorker) init() {
	w.once.Do(func() {
		w.wake = make(chan struct{}, 1)
		w.windowLinked, w.retryAt = map[string]time.Time{}, map[string]time.Time{}
		w.redactor, w.initErr = bundle.NewRedactor()
	})
}

func (w *FeatureWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *FeatureWorker) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// Wake runs the stages soon, for a settings change. A nil worker ignores it.
func (w *FeatureWorker) Wake() {
	if w == nil {
		return
	}
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run runs the stages every Tick and when woken, until ctx ends. It returns
// once the stage in progress has.
func (w *FeatureWorker) Run(ctx context.Context) {
	w.init()
	tick := w.Tick
	if tick <= 0 {
		tick = FeatureTick
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// RunOnce runs the stages (sync, index, placement and text) for every
// repository whose decisions are captured and that has a clone. A repository turned off is not read and makes no
// GitHub call.
func (w *FeatureWorker) RunOnce(ctx context.Context) {
	w.init()
	prefs, err := w.Registry.Preferences(ctx)
	if err != nil {
		w.logger().Warn("feature log: preferences not read", "error", err)
		return
	}
	repositories, err := w.Registry.Repositories(ctx)
	if err != nil {
		w.logger().Warn("feature log: repositories not read", "error", err)
		return
	}
	for _, repository := range repositories {
		if ctx.Err() != nil {
			return
		}
		if !repository.Settings.DecisionsEnabled(prefs) || w.now().Before(w.retryAt[repository.Repository]) {
			continue
		}
		i := slices.IndexFunc(repository.Clones, func(c repos.Clone) bool { return c.Available })
		if i < 0 {
			continue
		}
		dir := repository.Clones[i].Path
		synced, err := w.Sync(ctx, repository.Repository, dir)
		// The gate again: a repository turned off meanwhile has no
		// sessions read.
		if err == nil && !synced.Stopped && w.enabled(ctx, repository.Repository) {
			_, err = w.Index(ctx, repository.Repository, dir, synced.commits, false)
		}
		if err == nil && !synced.Stopped {
			_, err = w.Assign(ctx, repository.Repository, dir, FeatureOptions{})
		}
		if err == nil && !synced.Stopped {
			_, err = w.Summarize(ctx, repository.Repository, dir, FeatureOptions{})
		}
		if err != nil && ctx.Err() == nil {
			w.retryAt[repository.Repository] = w.now().Add(FeatureQuiet)
			w.logger().Warn("feature log: repository not brought up to date", "repository", repository.Repository, "error", err)
		}
	}
}

// enabled reads the gate again for the repository during a long sync; a
// worker without a registry (the command line's) has none.
func (w *FeatureWorker) enabled(ctx context.Context, repository string) bool {
	if w.Registry == nil {
		return true
	}
	prefs, err := w.Registry.Preferences(ctx)
	if err != nil {
		return false
	}
	settings, picked, err := w.Registry.RepositorySettings(ctx, repository)
	return err == nil && picked && settings.DecisionsEnabled(prefs)
}

// SyncReport says what Sync did.
type SyncReport struct {
	Repository  string `json:"repository"`
	Calls       int    `json:"github_calls"`
	NotModified bool   `json:"not_modified"`
	Listed      int    `json:"pull_requests_listed"`
	// Truncated is a listing that stopped at its bound: older pull requests
	// still due were skipped.
	Truncated bool `json:"truncated,omitempty"`
	PathsRead int  `json:"paths_read"`
	// PathsFailed are pull requests whose commits failed to be read, to be
	// tried again; PathsUnknown those of them that failed FeaturePathTries
	// times at their head and now have unknown paths.
	PathsFailed  int `json:"paths_failed"`
	PathsUnknown int `json:"paths_unknown"`
	// Stopped is set when the repository was turned off during the sync.
	Stopped bool `json:"stopped,omitempty"`
	Commits int  `json:"commits_recorded"`
	// commits are those recorded within FeatureLookback, for Index.
	commits []gitlocal.Commit
}

// Sync lists the repository's pull requests updated since the last sync
// (the first lists those of Window) and reads the paths and commits of each
// whose head changed from the clone in dir, fetching missing heads.
func (w *FeatureWorker) Sync(ctx context.Context, repository, dir string) (SyncReport, error) {
	w.init()
	report := SyncReport{Repository: repository}
	repo, err := github.ParseRepository(repository)
	if err != nil {
		return report, err
	}
	state, err := w.Store.Activate(ctx, repository)
	if err != nil {
		return report, err
	}
	since := state.Cursor
	if since.IsZero() {
		window := w.Window
		if window <= 0 {
			window = FeatureFirstBuildWindow
		}
		since = w.now().Add(-window)
	}
	reader, err := w.GitHub(ctx, repo)
	if err != nil {
		return report, err
	}
	list, err := github.ListPullRequests(ctx, reader, repo, state.ETag, since)
	report.Calls, report.NotModified, report.Listed, report.Truncated = list.Calls, list.NotModified, len(list.Pulls), list.Truncated
	if err != nil {
		return report, err
	}
	if !list.NotModified {
		// The cursor moves to the latest update listed, or to since when
		// none was. A truncated listing's older tail is skipped.
		cursor := since
		for _, p := range list.Pulls {
			if p.UpdatedAt.After(cursor) {
				cursor = p.UpdatedAt
			}
		}
		if list.Truncated {
			w.logger().Warn("feature log: older pull requests skipped, too many to list", "repository", repository, "listed", len(list.Pulls))
		}
		if err := w.Store.SavePulls(ctx, repository, list.Pulls, list.ETag, cursor); err != nil {
			return report, err
		}
	}
	pulls, err := w.Store.PullsWithoutPaths(ctx, repository)
	if err != nil || len(pulls) == 0 {
		return report, err
	}
	lookback := w.now().Add(-FeatureLookback)
	if !w.enabled(ctx, repository) {
		report.Stopped = true
		return report, nil
	}
	clone, remote, err := openClone(ctx, dir, repository)
	if err != nil {
		return report, err
	}
	defer func() { _ = clone.Close() }()
	wanted := make([]gitlocal.PullRequest, 0, len(pulls))
	for _, p := range pulls {
		wanted = append(wanted, gitlocal.PullRequest{Number: p.Number, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, BaseRef: p.BaseRef})
	}
	// The pass's one network step: when it fails, nothing is counted and the
	// repository waits.
	if err := clone.FetchPulls(ctx, remote, wanted); err != nil {
		return report, fmt.Errorf("fetching pull request heads: %w", err)
	}
	for _, p := range pulls {
		if !w.enabled(ctx, repository) {
			report.Stopped = true
			return report, nil
		}
		commits, err := pullCommits(ctx, clone, p)
		var paths []string
		var recorded []PullCommit
		switch {
		case ctx.Err() != nil:
			return report, ctx.Err()
		case err != nil:
			report.PathsFailed++
			w.logger().Debug("feature log: pull request commits not read", "repository", repository, "number", p.Number, "error", err)
			unknown, err := w.Store.PathsFailed(ctx, repository, p.Number, p.HeadSHA)
			if err != nil {
				return report, err
			}
			if unknown {
				report.PathsUnknown++
			}
			continue
		default:
			paths = []string{}
			for _, c := range commits {
				paths = append(paths, c.Files...)
				if at, err := time.Parse(time.RFC3339, c.CommittedAt); err == nil {
					recorded = append(recorded, PullCommit{SHA: c.OID, CommittedAt: at})
					if !at.Before(lookback) {
						report.commits = append(report.commits, c)
					}
				}
			}
			slices.Sort(paths)
			paths = slices.Compact(paths)
			report.PathsRead++
			report.Commits += len(recorded)
		}
		if err := w.Store.SetPullPaths(ctx, repository, p.Number, p.HeadSHA, paths, recorded); err != nil {
			return report, err
		}
	}
	return report, nil
}

// openClone opens the clone in dir and names its remote for repository.
func openClone(ctx context.Context, dir, repository string) (*gitlocal.Repo, string, error) {
	clone, err := gitlocal.Open(ctx, dir, gitlocal.Options{})
	if err != nil {
		return nil, "", err
	}
	remotes, err := clone.RemoteURLs(ctx)
	if err != nil {
		_ = clone.Close()
		return nil, "", err
	}
	for _, remote := range remotes {
		if gitlocal.SameRepository(remote.URL, repository) {
			return clone, remote.Name, nil
		}
	}
	_ = clone.Close()
	return nil, "", fmt.Errorf("no remote of the clone names %s", repository)
}

// pullCommits reads the pull request's commits, from its merge base to its
// head, from the clone's own objects; it never fetches.
func pullCommits(ctx context.Context, clone *gitlocal.Repo, p StoredPull) ([]gitlocal.Commit, error) {
	return clone.Commits(ctx, p.BaseSHA, p.HeadSHA)
}

// IndexReport says what Index did.
type IndexReport struct {
	Repository string `json:"repository"`
	Full       bool   `json:"full"`
	Commits    int    `json:"commits_linked"`
	Links      int    `json:"links"`
	Sessions   int    `json:"sessions"`
}

// agents names the live session agent of each session vendor, as
// capture_session keys its sessions.
var agents = map[string]string{
	"claude_code": livesessions.AgentClaudeCode,
	"codex":       livesessions.AgentCodex,
	"cursor":      livesessions.AgentCursor,
}

// Index links commits to the sessions on this machine and replaces their
// links: the fresh commits Sync recorded within FeatureLookback; every
// FeatureQuiet, the commits within the session window of now, which
// sessions still running may yet link to; and every FeatureRelinkEvery, or
// with full, every commit within FeatureLookback.
func (w *FeatureWorker) Index(ctx context.Context, repository, dir string, fresh []gitlocal.Commit, full bool) (IndexReport, error) {
	w.init()
	report := IndexReport{Repository: repository}
	now := w.now()
	state, err := w.Store.Activate(ctx, repository)
	if err != nil {
		return report, err
	}
	lookback := now.Add(-FeatureLookback)
	full = full || now.Sub(state.RelinkedAt) >= FeatureRelinkEvery
	window := full || now.Sub(w.windowLinked[repository]) >= FeatureQuiet
	report.Full = full
	commits := map[string]gitlocal.Commit{}
	add := func(c gitlocal.Commit, since time.Time) {
		if at, err := time.Parse(time.RFC3339, c.CommittedAt); err == nil && !at.Before(since) {
			commits[c.OID] = c
		}
	}
	// Sync keeps only the fresh commits within the lookback.
	for _, c := range fresh {
		commits[c.OID] = c
	}
	if full {
		// Pull requests whose commits could not be read get another try.
		if err := w.Store.RetryUnknownPaths(ctx, repository); err != nil {
			return report, err
		}
	}
	if window {
		since := now.Add(-locallink.SessionWindow)
		if full {
			since = lookback
		}
		pulls, err := w.Store.PullsWithCommitsSince(ctx, repository, since)
		if err != nil {
			return report, err
		}
		if len(pulls) > 0 {
			clone, _, err := openClone(ctx, dir, repository)
			if err != nil {
				return report, err
			}
			for _, p := range pulls {
				read, err := pullCommits(ctx, clone, p)
				if err != nil {
					if ctx.Err() != nil {
						_ = clone.Close()
						return report, err
					}
					continue
				}
				for _, c := range read {
					add(c, since)
				}
			}
			_ = clone.Close()
		}
	}
	if len(commits) == 0 && !full {
		if window {
			w.windowLinked[repository] = now
		}
		return report, nil
	}
	list := make([]gitlocal.Commit, 0, len(commits))
	shas := make([]string, 0, len(commits))
	for sha, c := range commits {
		list = append(list, c)
		shas = append(shas, sha)
	}
	slices.SortFunc(list, func(a, b gitlocal.Commit) int { return strings.Compare(a.OID, b.OID) })
	links, err := locallink.LinkCommits(ctx, dir, list, locallink.LinkOptions{Repository: repository, Sessions: w.Sessions})
	if err != nil {
		return report, err
	}
	rows := make([]SessionCommit, 0, len(links))
	linked := map[[2]string]bool{}
	for _, l := range links {
		agent, ok := agents[l.Vendor]
		if !ok {
			continue
		}
		rows = append(rows, SessionCommit{Agent: agent, SessionID: l.Session, SHA: l.Commit, Method: l.Method, Confidence: l.Confidence})
		linked[[2]string{agent, l.Session}] = true
	}
	var relinked time.Time
	if full {
		relinked = now
	}
	if err := w.Store.ReplaceSessionCommits(ctx, repository, shas, rows, relinked); err != nil {
		return report, err
	}
	if window {
		w.windowLinked[repository] = now
	}
	report.Commits, report.Links, report.Sessions = len(list), len(rows), len(linked)
	return report, nil
}

// FeatureOptions change one run of placement or the text stage, for testing
// by hand.
type FeatureOptions struct {
	// DryRun renders placement's calls without making them, and writes
	// nothing.
	DryRun bool
	// Limit bounds the items or features taken; zero is the stage's limit.
	Limit int
	// Manual ignores the activation and the text stage's debounce.
	Manual bool
	// Backend and Model, when set, replace the configured ones.
	Backend, Model string
}

// featureRun is one repository's run of placement or the text stage.
type featureRun struct {
	w               *FeatureWorker
	repository, dir string
	prefs           repos.Preferences
	settings        repos.Settings
	opts            FeatureOptions
	calls           caller
	version         int64
	now             time.Time
	reviews         map[int]string
	versions        *store.Store
	versionsRead    bool
}

// newRun reads the preferences and the repository's settings and its
// feature version, made unless the run is dry.
func (w *FeatureWorker) newRun(ctx context.Context, repository, dir string, o FeatureOptions) (*featureRun, error) {
	w.init()
	if w.initErr != nil {
		return nil, w.initErr
	}
	r := &featureRun{w: w, repository: repository, dir: dir, opts: o, now: w.now(), reviews: map[int]string{}, prefs: repos.DefaultPreferences()}
	if w.Registry != nil {
		var err error
		if r.prefs, err = w.Registry.Preferences(ctx); err != nil {
			return nil, err
		}
		if r.settings, _, err = w.Registry.RepositorySettings(ctx, repository); err != nil {
			return nil, err
		}
	}
	prices := pricing.Active()
	if w.Prices != nil {
		prices = w.Prices()
	}
	r.calls = caller{store: w.Store, config: w.Config, backends: w.Backends, prices: prices, redactor: w.redactor, log: w.logger(), now: r.now}
	var err error
	r.version, err = liveVersion(ctx, w.Store.db, repository, !o.DryRun, r.now)
	return r, err
}

// reviewSummary is the summary of the pull request's latest review saved
// in the clone, "" for none.
func (r *featureRun) reviewSummary(ctx context.Context, number int) string {
	if summary, ok := r.reviews[number]; ok {
		return summary
	}
	r.reviews[number] = ""
	if !r.versionsRead {
		r.versionsRead = true
		clone, err := gitlocal.Open(ctx, r.dir, gitlocal.Options{})
		if err != nil {
			return ""
		}
		common := clone.CommonDir()
		_ = clone.Close()
		if r.versions, err = store.Open(common, "", false); err != nil {
			r.versions = nil
		}
	}
	if r.versions == nil {
		return ""
	}
	latest, err := r.versions.Latest(ctx, store.Key{Repository: r.repository, Number: number})
	if err != nil {
		return ""
	}
	var doc struct {
		Summary struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	if json.Unmarshal(latest.Document, &doc) == nil {
		r.reviews[number] = strings.TrimSpace(doc.Summary.Text)
	}
	return r.reviews[number]
}
