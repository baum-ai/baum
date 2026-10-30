package decisionlog

import (
	"cmp"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/features/discover"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/sessions"
)

// Feature discovery (README.md, "Discovery") maps a repository into its
// features from a read-only snapshot of its default branch and its pull
// request history digest, through the review harness, and stores the reply
// as a taxonomy a first build seeds its version with.

// StageDiscovery is the ledger stage of a discovery task.
const StageDiscovery = "feature_discovery"

// LatestTaxonomy names a repository's newest taxonomy.
const LatestTaxonomy = "latest"

// discoveryBudgets are the resolved configuration's run timeout (an unset one
// the default's) and its calls and tokens (zero, the default, is no limit),
// for priced and unpriced models alike: nothing about the price bounds
// discovery.
func discoveryBudgets(cfg config.Config) agent.Budgets {
	b, d := cfg.Budgets, config.Default().Budgets
	return agent.Budgets{WallClock: time.Duration(cmp.Or(b.RunTimeout, d.RunTimeout)), MaxCalls: max(b.MaxCalls, 0),
		MaxTokens: max(b.MaxTokens, 0), MaxProviderAttempts: 3, MaxRepairAttempts: 2}
}

// Reasons discovery was not run or gave no taxonomy, besides the backend
// reasons.
const (
	ReasonNoDefaultBranch = "no_default_branch"
	// ReasonDiscoveryFailed is followed by the failure's category.
	ReasonDiscoveryFailed = "discovery_failed"
)

// DiscoverReport says what Discover did; no taxonomy text.
type DiscoverReport struct {
	Repository string `json:"repository"`
	// TaxonomyID is the stored taxonomy, 0 when none was made.
	TaxonomyID int64  `json:"taxonomy_id,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Features   int    `json:"features"`
	Backend    string `json:"backend,omitempty"`
	Model      string `json:"model,omitempty"`
	// MaxTokens and MaxCalls are the run's budgets; zero is no limit.
	MaxTokens int `json:"max_tokens,omitempty"`
	MaxCalls  int `json:"max_calls,omitempty"`
	Calls     int `json:"calls"`
	// SpentUSD is nil when the run's spend cannot be priced.
	SpentUSD *float64 `json:"spent_usd"`
	// Stopped is why no taxonomy was made.
	Stopped string `json:"stopped,omitempty"`
}

// Discover runs feature discovery of the repository once, from the clone in
// dir, and stores the taxonomy it replies with. It runs on the review
// backend and model, after listing the whole history and reading its paths
// as a first build does.
func (w *FeatureWorker) Discover(ctx context.Context, repository, dir string) (DiscoverReport, error) {
	r, err := w.newRun(ctx, repository, dir, FeatureOptions{Manual: true, DryRun: true})
	if err != nil {
		return DiscoverReport{Repository: repository}, err
	}
	if _, err := w.ListHistory(ctx, repository); err != nil {
		return DiscoverReport{Repository: repository}, err
	}
	if err := w.readPaths(ctx, repository, dir, &SyncReport{}, func() bool { return false }); err != nil {
		return DiscoverReport{Repository: repository}, err
	}
	report, _, err := r.discover(ctx)
	return report, err
}

// discoveryPlan is the backend and budgets discovery runs with, or why it
// cannot run.
type discoveryPlan struct {
	selected generate.Selected
	cfg      config.Config
	budgets  agent.Budgets
	skip     string
}

// planDiscovery resolves the backend and model exactly as a review does
// (generate.Select with the review backend and model; a build's backend and
// model replace only its own calls'), with a review run's budgets.
func (r *featureRun) planDiscovery(ctx context.Context) discoveryPlan {
	cfg := r.settings.Resolve(r.prefs.Resolve(r.w.Config))
	backends := r.w.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	p := discoveryPlan{cfg: cfg}
	var err error
	if p.selected, err = generate.Select(ctx, cfg.Backend, cfg.Model, backends, io.Discard); err != nil {
		r.w.logger().Info("feature discovery: backend unavailable", "backend", cfg.Backend, "error", err)
		p.skip = ReasonUnavailable
		return p
	}
	if p.selected.Backend.Name() == agent.BackendCursorAgent {
		p.skip = ReasonUnsupported
		return p
	}
	p.budgets = discoveryBudgets(cfg)
	return p
}

// discover runs discovery and stores its taxonomy, returning it. A run that
// could not be made or failed is reported in Stopped with a nil error; the
// error is a store or clone failure, or an interruption.
func (r *featureRun) discover(ctx context.Context) (DiscoverReport, discover.FeatureTaxonomy, error) {
	report := DiscoverReport{Repository: r.repository}
	var taxonomy discover.FeatureTaxonomy
	p := r.planDiscovery(ctx)
	if p.selected.Backend != nil {
		report.Backend, report.Model = p.selected.Backend.Name(), p.selected.Model
	}
	report.MaxTokens, report.MaxCalls = p.budgets.MaxTokens, p.budgets.MaxCalls
	if report.Stopped = p.skip; p.skip != "" {
		return report, taxonomy, nil
	}
	var branch string
	err := r.w.Store.db.QueryRowContext(ctx, `SELECT default_branch FROM pull_request WHERE repository=? AND default_branch != ''
ORDER BY updated_at DESC LIMIT 1`, r.repository).Scan(&branch)
	if errors.Is(err, sql.ErrNoRows) {
		report.Stopped = ReasonNoDefaultBranch
		return report, taxonomy, nil
	}
	if err != nil {
		return report, taxonomy, err
	}
	runDir, revision, err := r.discoveryDir(ctx, p.cfg, branch)
	if runDir != "" {
		// The unpacked checkout's directories are read-only.
		defer generate.RemoveTree(runDir)
	}
	if err != nil {
		return report, taxonomy, err
	}
	report.Revision = revision
	attempt := Attempt{Repository: r.repository, Stage: StageDiscovery, Item: "revision:" + revision,
		Backend: report.Backend, Model: report.Model}
	id, err := r.w.Store.Begin(ctx, attempt)
	if err != nil {
		return report, taxonomy, err
	}
	outcome, document, runErr := generate.RunTask(ctx, generate.TaskOptions{
		Backend: p.selected.Backend, Redactor: r.w.redactor,
		// The marker keeps the run's own session out of session evidence.
		Task: agent.Task{Directory: runDir, Prompt: sessions.GenerationRunMarker + "\n" + discover.PublicPrompt,
			Schema: discover.Schema, Check: discover.Check, Budgets: p.budgets},
	})
	report.Calls = outcome.Calls
	report.SpentUSD = r.calls.prices.BackendOutcomeCost(report.Backend, cmpModel(outcome.Model, report.Model), outcome)
	var reply discover.Reply
	if runErr == nil {
		if err := json.Unmarshal(document, &reply); err != nil {
			runErr = &generate.Error{Category: generate.CategoryValidation, Err: err}
		} else if err := discover.ValidateTaxonomyFeatures(reply.Features); err != nil {
			// Masking can take a checked reply over the bounds.
			runErr = &generate.Error{Category: generate.CategoryValidation, Err: err}
		}
	}
	state, reason := AttemptSucceeded, ""
	switch {
	case runErr != nil && ctx.Err() != nil:
		state, reason = AttemptAbandoned, "cancelled"
	case runErr != nil:
		state, reason = AttemptFailed, ReasonDiscoveryFailed+": "+string(generate.CategoryOf(runErr))
	}
	write := context.WithoutCancel(ctx)
	if err := r.w.Store.settle(write, id, state, reason, report.SpentUSD, decisionUsage(outcome.Usage), outcome.StructuredOutputRejections); err != nil {
		return report, taxonomy, err
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return report, taxonomy, ctx.Err()
		}
		r.w.logger().Warn("feature discovery failed", "repository", r.repository, "error", runErr)
		report.Stopped = reason
		return report, taxonomy, nil
	}
	taxonomy = discover.FeatureTaxonomy{SchemaVersion: discover.TaxonomySchemaVersion, RepositoryID: r.repository, Revision: revision,
		CreatedAt: r.w.now().UTC(), Features: reply.Features}
	report.TaxonomyID, err = r.w.Store.saveTaxonomy(write, taxonomy)
	report.Features = len(taxonomy.Features)
	return report, taxonomy, err
}

func cmpModel(reported, configured string) string {
	if reported != "" {
		return reported
	}
	return configured
}

// discoveryDir fetches the default branch's head into gitlocal.DefaultRef
// and writes the run directory: the head's checkout as a review's is
// bounded, in head/, and history.jsonl, read-only.
func (r *featureRun) discoveryDir(ctx context.Context, cfg config.Config, branch string) (dir, revision string, err error) {
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return "", "", err
	}
	checkout := assemble.LimitsFrom(caps).Checkout
	clone, remote, err := openClone(ctx, r.dir, r.repository)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = clone.Close() }()
	if revision, err = clone.FetchBranch(ctx, remote, branch); err != nil {
		return "", "", fmt.Errorf("fetching the default branch: %w", err)
	}
	archives, err := os.MkdirTemp("", "baum-discover-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(archives) }()
	archive := filepath.Join(archives, "checkout.tar.gz")
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	compressed := gzip.NewWriter(file)
	_, err = bundle.NewCheckoutBuilder(checkout).WriteArchive(ctx, clone, revision, "", nil, compressed)
	if err = errors.Join(err, compressed.Close(), file.Close()); err != nil {
		return "", "", fmt.Errorf("writing the checkout: %w", err)
	}
	if dir, err = os.MkdirTemp("", agent.RunDirPrefix+"*"); err != nil {
		return "", "", err
	}
	if err := bundle.UnpackHeadCheckout(ctx, archive, dir, checkout); err != nil {
		return dir, "", fmt.Errorf("unpacking the checkout: %w", err)
	}
	pulls, err := r.w.Store.historyPulls(ctx, r.repository)
	if err != nil {
		return dir, "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "history.jsonl"), discover.HistoryDigest(r.repository, pulls), 0o400); err != nil {
		return dir, "", err
	}
	return dir, revision, nil
}

// historyPulls are the pull requests a build places, merged or open and not
// a bot's, in the order they were opened, with their paths.
func (s *Store) historyPulls(ctx context.Context, repository string) ([]discover.HistoryPullRequest, error) {
	return collect(func(rows *sql.Rows) (discover.HistoryPullRequest, error) {
		var p discover.HistoryPullRequest
		var opened int64
		var paths sql.NullString
		if err := rows.Scan(&p.Number, &opened, &p.Merged, &p.Title, &p.Branch, &paths); err != nil {
			return p, err
		}
		p.Opened = time.UnixMilli(opened)
		if paths.Valid {
			if err := json.Unmarshal([]byte(paths.String), &p.Paths); err != nil {
				return p, err
			}
		}
		return p, nil
	})(s.db.QueryContext(ctx, `SELECT number, created_at, merged_at IS NOT NULL, title, head_ref, paths_json FROM pull_request
WHERE repository=? AND bot=0 AND (state='open' OR merged_at IS NOT NULL) ORDER BY created_at, number`, repository))
}

// saveTaxonomy stores a taxonomy and returns its id.
func (s *Store) saveTaxonomy(ctx context.Context, taxonomy discover.FeatureTaxonomy) (int64, error) {
	document, err := json.Marshal(taxonomy)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO feature_taxonomy(repository, revision, created_at, document_json) VALUES (?,?,?,?) RETURNING id`,
		taxonomy.RepositoryID, taxonomy.Revision, taxonomy.CreatedAt.UnixMilli(), string(document)).Scan(&id)
	return id, err
}

// errNoTaxonomy is a taxonomy ref that names none.
var errNoTaxonomy = errors.New("no such feature taxonomy")

// Taxonomy reads the repository's taxonomy named by ref, its id or
// LatestTaxonomy, with its id.
func (s *Store) Taxonomy(ctx context.Context, repository, ref string) (int64, discover.FeatureTaxonomy, error) {
	if ref == LatestTaxonomy {
		return s.readTaxonomy(ctx, repository, `SELECT id, document_json FROM feature_taxonomy WHERE repository=?
ORDER BY created_at DESC, id DESC LIMIT 1`, repository)
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return 0, discover.FeatureTaxonomy{}, fmt.Errorf("a taxonomy is named by its id or %q, not %q", LatestTaxonomy, ref)
	}
	return s.readTaxonomy(ctx, repository, `SELECT id, document_json FROM feature_taxonomy WHERE repository=? AND id=?`, repository, id)
}

// taxonomyAfter is the repository's newest taxonomy created after t; 0
// when there is none.
func (s *Store) taxonomyAfter(ctx context.Context, repository string, t time.Time) (int64, discover.FeatureTaxonomy, error) {
	id, taxonomy, err := s.readTaxonomy(ctx, repository, `SELECT id, document_json FROM feature_taxonomy WHERE repository=?1 AND created_at > ?2
ORDER BY created_at DESC, id DESC LIMIT 1`, repository, t.UnixMilli())
	if errors.Is(err, errNoTaxonomy) {
		return 0, taxonomy, nil
	}
	return id, taxonomy, err
}

func (s *Store) readTaxonomy(ctx context.Context, repository, query string, args ...any) (int64, discover.FeatureTaxonomy, error) {
	var id int64
	var document string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&id, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, discover.FeatureTaxonomy{}, fmt.Errorf("%w of %s", errNoTaxonomy, repository)
	}
	if err != nil {
		return 0, discover.FeatureTaxonomy{}, err
	}
	taxonomy, err := discover.ParseFeatureTaxonomy([]byte(document), repository)
	return id, taxonomy, err
}
