package decisionlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/baum-ai/baum/features/assign"
	"github.com/baum-ai/baum/features/consolidate"
	"github.com/baum-ai/baum/features/summary"
)

// First build (README.md, "First build"): a repository's pull request
// history placed in features in a building version, which then becomes
// current.

// outcomeLeftOut settles a pull request the build gave up on, until the
// version is current.
const outcomeLeftOut = "left_out"

// Phases of a build.
const (
	BuildPlacing       = "placing"
	BuildSummarizing   = "summarizing"
	BuildConsolidating = "consolidating"
	BuildSwitching     = "switching"
)

// BuildOptions change a first build.
type BuildOptions struct {
	// MaxCostUSD caps the building version's spend over all its runs; zero
	// is what it spent and its estimate times FeatureBuildCapFactor.
	MaxCostUSD float64
	// DryRun estimates and writes nothing.
	DryRun bool
	// Backend and Model, when set, replace the configured ones.
	Backend, Model string
	// Gated refuses a repository whose decisions are not captured (the
	// gate), with Reason ReasonDisabled.
	Gated bool
	// Progress, when set, is told after every batch and stage.
	Progress func(BuildProgress)
}

// BuildProgress is a build's progress.
type BuildProgress struct {
	Phase string `json:"phase"`
	// Placed counts the pull requests placed or skipped of Total.
	Placed, Total int
	Calls         int
	SpentUSD      float64
}

// BuildEstimate is what a build of the repository would do and cost.
type BuildEstimate struct {
	Repository string `json:"repository"`
	// Version is the building version a build resumes, 0 for a new one.
	Version int64 `json:"version,omitempty"`
	// PullRequests are those left to place, in Batches calls.
	PullRequests int    `json:"pull_requests"`
	Batches      int    `json:"batches"`
	Backend      string `json:"backend,omitempty"`
	Model        string `json:"model,omitempty"`
	// EstimateUSD is the cost of what is left, nil when it cannot be
	// priced; SpentUSD what the version's earlier runs spent.
	EstimateUSD *float64 `json:"estimate_usd"`
	SpentUSD    float64  `json:"spent_usd"`
	// ReservedUSD is the most a batch call of the build is held to under the
	// cap.
	ReservedUSD float64 `json:"reserved_usd"`
	// Reason is why no call could be made (a backend or price reason).
	Reason string `json:"reason,omitempty"`
}

// Cap is the version's spend cap a build confirmed for the estimate holds:
// what it spent and the estimate times FeatureBuildCapFactor, and at least
// room for one more batch call beyond the estimate.
func (e BuildEstimate) Cap() float64 {
	if e.EstimateUSD == nil {
		return e.SpentUSD
	}
	return e.SpentUSD + max(*e.EstimateUSD*FeatureBuildCapFactor, *e.EstimateUSD+e.ReservedUSD)
}

// BuildReport says what Build did; no pull request, feature or reply text.
type BuildReport struct {
	Repository string `json:"repository"`
	Version    int64  `json:"version"`
	// PullRequests are those this run listed to place.
	PullRequests int `json:"pull_requests"`
	Placed       int `json:"placed"`
	Created      int `json:"created"`
	Skipped      int `json:"skipped"`
	// Failed counts the pull requests whose batch failed down to them alone,
	// LeftOut those that failed once more and were left out.
	Failed   int `json:"failed"`
	LeftOut  int `json:"left_out"`
	Calls    int `json:"calls"`
	Features int `json:"features"`
	// Consolidation is the pass made, if one was.
	Consolidation *ConsolidateReport `json:"consolidation,omitempty"`
	// Current says the version became current; otherwise Stopped says why
	// not (a cap or backend reason, or "failed").
	Current         bool    `json:"current"`
	Stopped         string  `json:"stopped,omitempty"`
	MaxCostUSD      float64 `json:"max_cost_usd"`
	SpentUSD        float64 `json:"spent_usd"`
	VersionSpentUSD float64 `json:"version_spent_usd"`
}

// errBuildStop stops a build: a call was not made for the reason it gives.
type errBuildStop struct{ reason string }

func (e errBuildStop) Error() string { return "the build stopped: " + e.reason }

// buildWriteError is a store write that failed after a call; it stops the
// build rather than a batch being retried.
type buildWriteError struct{ err error }

func (e *buildWriteError) Error() string { return e.err.Error() }
func (e *buildWriteError) Unwrap() error { return e.err }

// buildItem is a pull request a build places.
type buildItem struct {
	Number int
	Head   string
}

// buildItems are the pull requests a build of the run's version places,
// oldest first: merged and open ones of the last FeatureFirstBuildWindow
// not opened by a bot, in no feature of the version and not skipped or left
// out of it at their head.
func (r *featureRun) buildItems(ctx context.Context) ([]buildItem, error) {
	s := r.w.Store
	items, err := collect(func(rows *sql.Rows) (buildItem, error) {
		var item buildItem
		return item, rows.Scan(&item.Number, &item.Head)
	})(s.db.QueryContext(ctx, `SELECT number, head_sha FROM pull_request p WHERE repository=?1 AND bot=0
AND (state='open' OR merged_at IS NOT NULL) AND created_at >= ?2
AND NOT EXISTS (SELECT 1 FROM feature_pull fp WHERE fp.version_id=?3 AND fp.repository=p.repository AND fp.number=p.number)
ORDER BY created_at, number`, r.repository, r.now.Add(-FeatureFirstBuildWindow).UnixMilli(), r.version))
	if err != nil {
		return nil, err
	}
	settled, err := s.settleRows(ctx, r.version, r.repository)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(items, func(item buildItem) bool {
		row, ok := settled[[2]string{kindPull, strconv.Itoa(item.Number)}]
		return ok && row.key == item.Head && row.outcome != ""
	}), nil
}

// EstimateBuild estimates a build of the repository: what is left of its
// building version, or a new one.
func (w *FeatureWorker) EstimateBuild(ctx context.Context, repository, dir string, o BuildOptions) (BuildEstimate, error) {
	estimate := BuildEstimate{Repository: repository}
	r, err := w.newRun(ctx, repository, dir, FeatureOptions{Manual: true, DryRun: true, Backend: o.Backend, Model: o.Model, building: true})
	if err != nil {
		return estimate, err
	}
	estimate.Version = r.version
	if r.version != 0 {
		if err := w.Store.db.QueryRowContext(ctx, `SELECT spent_usd FROM feature_version WHERE id=?`, r.version).Scan(&estimate.SpentUSD); err != nil {
			return estimate, err
		}
	}
	if _, _, on, err := w.gate(ctx, repository); err != nil || (o.Gated && !on) {
		estimate.Reason = ReasonDisabled
		return estimate, err
	}
	items, err := r.buildItems(ctx)
	if err != nil {
		return estimate, err
	}
	estimate.PullRequests = len(items)
	estimate.Batches = (len(items) + FeatureBatch - 1) / FeatureBatch
	p, skip := r.calls.plan(ctx, r.prefs, r.settings, o.Backend, o.Model, 1, 1)
	estimate.Backend, estimate.Model, estimate.Reason = p.Backend, p.Model, skip
	if skip != "" {
		return estimate, nil
	}
	cost, reserved, ok, err := r.estimateCost(ctx, items, p)
	estimate.ReservedUSD = reserved
	if ok {
		estimate.EstimateUSD = &cost
	} else if err == nil {
		estimate.Reason = ReasonUnpriced
	}
	return estimate, err
}

// estimateCost prices the calls a build of items would make, from the
// sizes of their inputs (README.md, "First build"): the batches, the new
// features' text twice and the text of a resumed version's features behind
// their members, a summary of every pull request with a description, and
// the consolidation pass unless the version had it; with the most a batch
// call is held to under the cap.
func (r *featureRun) estimateCost(ctx context.Context, items []buildItem, p planned) (float64, float64, bool, error) {
	batchVersion, _ := assign.Public.BatchVersion(AssignVersion)
	batchRequest, err := assign.Public.Request(batchVersion, "", assign.MaxBatchOutputTokens)
	if err != nil {
		return 0, 0, false, err
	}
	tokens := func(bytes int) int64 { return int64(bytes/FeatureEstimateBytesPerToken) + 1 }
	total, reserved, priced := 0.0, 0.0, true
	add := func(in, out int64) {
		cost, ok := callCost(r.calls.prices, p.Backend, p.Model, in, out)
		total, priced = total+cost, priced && ok
	}
	type size struct{ bytes, body int }
	sizes := make([]size, len(items))
	for i, item := range items {
		pull, err := r.w.Store.placedPull(ctx, r.version, r.repository, item.Number)
		if err != nil {
			return 0, 0, false, err
		}
		bytes := min(len(pull.Title), 200) + min(len(pull.Body), 2000) + 100
		for _, path := range pull.Paths[:min(len(pull.Paths), 40)] {
			bytes += len(path) + 4
		}
		sizes[i] = size{bytes, len(pull.Body)}
	}
	// What a resumed version already holds: its features, those whose text
	// is behind, and whether its pass was made.
	var existing, behind int
	var consolidated bool
	if r.version != 0 {
		if err := r.w.Store.db.QueryRowContext(ctx, `SELECT
(SELECT COUNT(*) FROM feature WHERE version_id=?1 AND merged_into IS NULL),
(SELECT COUNT(*) FROM feature WHERE version_id=?1 AND merged_into IS NULL AND summarized_version < membership_version),
(SELECT consolidated_at IS NOT NULL FROM feature_version WHERE id=?1)`, r.version).Scan(&existing, &behind, &consolidated); err != nil {
			return 0, 0, false, err
		}
	}
	features, created := existing, 0
	for start := 0; start < len(items); start += FeatureBatch {
		bytes := len(batchRequest.System) + 2000
		batch := sizes[start:min(len(sizes), start+FeatureBatch)]
		for _, s := range batch {
			bytes += s.bytes
		}
		bytes += min(FeatureBuildCandidates, features) * FeatureEstimateCandidateBytes
		add(tokens(bytes), int64(len(batch)*FeatureEstimateItemOutputTokens))
		if cost, ok := callCost(r.calls.prices, p.Backend, p.Model, int64(bytes/FeatureBuildBytesPerToken), assign.MaxBatchOutputTokens); ok {
			reserved = max(reserved, cost)
		}
		created = (start + len(batch) + FeatureEstimatePullsPerFeature - 1) / FeatureEstimatePullsPerFeature
		features = existing + created
	}
	textRequest, err := summary.Public.Request(SummaryVersion, summary.Feature, "", summary.MaxOutputTokens)
	if err != nil {
		return 0, 0, false, err
	}
	for range created*FeatureEstimateTextPasses + behind {
		add(tokens(len(textRequest.System)+FeatureEstimateTextBytes), FeatureEstimateTextOutputTokens)
	}
	pullRequest, err := summary.Public.Request(SummaryVersion, summary.PullRequest, "", summary.MaxOutputTokens)
	if err != nil {
		return 0, 0, false, err
	}
	for _, s := range sizes {
		if s.body > 0 {
			add(tokens(len(pullRequest.System)+min(s.body, 4000)+1000), FeatureEstimatePullOutputTokens)
		}
	}
	if features > 0 && !consolidated {
		merge, err := consolidate.Public.Request(ConsolidateVersion, consolidate.Merge, "", consolidate.MaxOutputTokens)
		if err != nil {
			return 0, 0, false, err
		}
		add(tokens(len(merge.System)+min(features*FeatureEstimateCandidateBytes, consolidate.DefaultMaxInputBytes)), FeatureEstimateConsolidateOutputTokens)
		for range min(FeatureMaxSplits, features) {
			add(tokens(len(merge.System)+FeatureEstimateSplitBytes), FeatureEstimateConsolidateOutputTokens)
		}
	}
	return math.Round(total*10000) / 10000, reserved, priced, nil
}

// buildRun is one run of a build.
type buildRun struct {
	*featureRun
	report   *BuildReport
	items    []buildItem
	failed   []buildItem
	batch    string
	progress func(BuildProgress)
	placed   int
}

// Build builds the repository's features from its pull request history in
// its building version, made or resumed, and makes that version current
// (README.md, "First build"). It stops, leaving the version building, when
// the next call would take the version's spend past the cap, when a call
// cannot be made, or on a store error; running it again resumes the
// version. A dry run reports the estimate in Stopped's place and writes
// nothing.
func (w *FeatureWorker) Build(ctx context.Context, repository, dir string, o BuildOptions) (BuildReport, error) {
	report := BuildReport{Repository: repository}
	estimate, err := w.EstimateBuild(ctx, repository, dir, o)
	if err != nil {
		return report, err
	}
	report.PullRequests, report.MaxCostUSD = estimate.PullRequests, o.MaxCostUSD
	if report.MaxCostUSD <= 0 {
		report.MaxCostUSD = estimate.Cap()
	}
	if o.DryRun {
		report.Version, report.VersionSpentUSD, report.Stopped = estimate.Version, estimate.SpentUSD, FeatureDryRun
		return report, nil
	}
	if estimate.Reason != "" {
		report.Stopped = estimate.Reason
		return report, nil
	}
	// A gated build checks the gate before every call, as the live stages do.
	r, err := w.newRun(ctx, repository, dir, FeatureOptions{Manual: !o.Gated, Backend: o.Backend, Model: o.Model, building: true})
	if err != nil {
		return report, err
	}
	report.Version = r.version
	b := &buildRun{featureRun: r, report: &report, progress: o.Progress}
	b.batch, _ = assign.Public.BatchVersion(AssignVersion)
	var consolidated sql.NullInt64
	if err := w.Store.db.QueryRowContext(ctx, `UPDATE feature_version SET stopped='' WHERE id=? RETURNING spent_usd, consolidated_at`, r.version).
		Scan(&report.VersionSpentUSD, &consolidated); err != nil {
		return report, err
	}
	r.calls.budget = &buildBudget{version: r.version, cap: report.MaxCostUSD, spent: report.VersionSpentUSD}
	err = b.run(ctx, consolidated.Valid)
	report.SpentUSD, report.Calls = r.calls.budget.run, r.calls.budget.calls
	report.VersionSpentUSD += report.SpentUSD
	var stop errBuildStop
	switch {
	case errors.As(err, &stop):
		report.Stopped, err = stop.reason, nil
	case err != nil && ctx.Err() == nil:
		report.Stopped = "failed"
	}
	if report.Stopped != "" {
		if _, recordErr := w.Store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE feature_version SET stopped=? WHERE id=?`,
			report.Stopped, r.version); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
	}
	_ = w.Store.db.QueryRowContext(context.WithoutCancel(ctx), `SELECT COUNT(*) FROM feature WHERE version_id=? AND merged_into IS NULL`, r.version).
		Scan(&report.Features)
	w.logger().Info("feature build", "repository", repository, "version", r.version, "current", report.Current, "stopped", report.Stopped,
		"placed", report.Placed, "left_out", report.LeftOut, "spent_usd", report.SpentUSD)
	return report, err
}

// run places what is left, writes the text, makes the version's one
// consolidation pass unless consolidated, and switches the version.
func (b *buildRun) run(ctx context.Context, consolidated bool) error {
	var err error
	if b.items, err = b.buildItems(ctx); err != nil {
		return err
	}
	if err := b.recordProgress(ctx, BuildPlacing); err != nil {
		return err
	}
	chunks := (len(b.items) + FeatureBatch - 1) / FeatureBatch
	for chunk := range chunks {
		part := b.items[chunk*FeatureBatch : min(len(b.items), (chunk+1)*FeatureBatch)]
		if err := b.assignItems(ctx, part); err != nil {
			return err
		}
		if chunk == chunks-1 {
			if err := b.retryFailed(ctx); err != nil {
				return err
			}
		}
		if err := b.recordProgress(ctx, BuildPlacing); err != nil {
			return err
		}
		if (chunk+1)%FeatureBuildSummarizeEvery == 0 || chunk == chunks-1 {
			if err := b.summarizeAll(ctx); err != nil {
				return err
			}
		}
	}
	if chunks == 0 {
		if err := b.summarizeAll(ctx); err != nil {
			return err
		}
	}
	if !consolidated {
		b.notify(BuildConsolidating)
		pass, err := b.consolidate(ctx, false)
		b.report.Consolidation = &pass
		switch {
		case err != nil:
			return err
		case pass.Stopped != "":
			return errBuildStop{pass.Stopped}
		}
		// A pass that failed is not recorded: the build still switches, and
		// the weekly pass takes the version on its next run.
		consolidated = pass.Outcome == ConsolidationDone
		if consolidated {
			if _, err := b.w.Store.db.ExecContext(ctx, `UPDATE feature_version SET consolidated_at=? WHERE id=?`, b.now.UnixMilli(), b.version); err != nil {
				return err
			}
		}
		if err := b.summarizeAll(ctx); err != nil {
			return err
		}
	}
	b.notify(BuildSwitching)
	if err := b.w.Store.switchVersion(context.WithoutCancel(ctx), b.repository, b.version, consolidated, b.now); err != nil {
		return err
	}
	b.report.Current = true
	return nil
}

func (b *buildRun) notify(phase string) {
	if b.progress != nil {
		b.progress(BuildProgress{Phase: phase, Placed: b.placed, Total: len(b.items), Calls: b.calls.budget.calls, SpentUSD: b.calls.budget.run})
	}
}

// recordProgress records the version's pull requests placed or skipped of
// those a build places, and reports them.
func (b *buildRun) recordProgress(ctx context.Context, phase string) error {
	var placed, settled int
	if err := b.w.Store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM feature_pull WHERE version_id=?1),
(SELECT COUNT(*) FROM feature_settle WHERE version_id=?1 AND kind=?2 AND outcome != '')`, b.version, kindPull).Scan(&placed, &settled); err != nil {
		return err
	}
	b.placed = b.report.Placed + b.report.Skipped + b.report.LeftOut
	total := placed + settled + len(b.items) - b.placed
	if _, err := b.w.Store.db.ExecContext(ctx, `UPDATE feature_version SET total=?, placed=? WHERE id=?`, total, placed+settled, b.version); err != nil {
		return err
	}
	b.notify(phase)
	return nil
}

// summarizeAll writes the text of every feature of the version behind its
// members, without the debounce. A failed feature backs off and is left
// behind for the live text stage.
func (b *buildRun) summarizeAll(ctx context.Context) error {
	b.notify(BuildSummarizing)
	report, err := b.summarizePending(ctx, b.now.Add(time.Millisecond), math.MaxInt)
	if err != nil {
		return err
	}
	if report.Stopped != "" {
		return errBuildStop{report.Stopped}
	}
	return nil
}

// assignItems places items with one batch call, made once more when it
// fails, then split in halves down to one pull request, which is tried
// again after the last batch.
func (b *buildRun) assignItems(ctx context.Context, items []buildItem) error {
	if len(items) == 0 {
		return nil
	}
	var last error
	for range FeatureBuildTries {
		err := b.callBatch(ctx, items)
		var write *buildWriteError
		var stop errBuildStop
		switch {
		case err == nil || errors.As(err, &write) || errors.As(err, &stop):
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if last = err; errors.Is(err, assign.ErrBatchTooLarge) {
			break
		}
	}
	if len(items) == 1 {
		b.w.logger().Warn("feature build: pull request failed, to be tried again", "repository", b.repository, "number", items[0].Number, "error", last)
		b.report.Failed++
		b.failed = append(b.failed, items[0])
		return nil
	}
	half := len(items) / 2
	if err := b.assignItems(ctx, items[:half]); err != nil {
		return err
	}
	return b.assignItems(ctx, items[half:])
}

// retryFailed tries the pull requests that failed on their own once more
// together; those that fail again are left out of the version.
func (b *buildRun) retryFailed(ctx context.Context) error {
	failed := b.failed
	b.failed = nil
	for len(failed) > 0 {
		part := failed[:min(len(failed), FeatureBatch)]
		failed = failed[len(part):]
		if err := b.retryItems(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

func (b *buildRun) retryItems(ctx context.Context, items []buildItem) error {
	err := b.callBatch(ctx, items)
	var write *buildWriteError
	var stop errBuildStop
	switch {
	case err == nil || errors.As(err, &write) || errors.As(err, &stop):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, assign.ErrBatchTooLarge) && len(items) > 1:
		half := len(items) / 2
		if err := b.retryItems(ctx, items[:half]); err != nil {
			return err
		}
		return b.retryItems(ctx, items[half:])
	}
	for _, item := range items {
		b.w.logger().Warn("feature build: pull request left out", "repository", b.repository, "number", item.Number, "error", err)
		if err := b.w.Store.settleItem(context.WithoutCancel(ctx), b.version, b.repository, kindPull, strconv.Itoa(item.Number), item.Head, outcomeLeftOut); err != nil {
			return &buildWriteError{err}
		}
		b.report.LeftOut++
	}
	return nil
}

// callBatch makes one batch call for items and writes its reply: each
// item's candidates are ranked against the version as placement ranks
// them, and the call offers their union, each item's best first, at most
// FeatureBuildCandidates.
func (b *buildRun) callBatch(ctx context.Context, items []buildItem) error {
	s := b.w.Store
	pulls := make([]placedPull, 0, len(items))
	ranked := make([][]assign.Candidate, 0, len(items))
	for _, item := range items {
		pull, err := s.placedPull(ctx, b.version, b.repository, item.Number)
		if err != nil {
			return &buildWriteError{err}
		}
		input := assign.Item{PullRequest: &assign.PullRequest{Title: pull.Title, Body: pull.Body, Branch: pull.HeadRef, Paths: pull.Paths}}
		candidates, err := b.candidates(ctx, input, pull)
		if err != nil {
			return &buildWriteError{err}
		}
		pulls, ranked = append(pulls, pull), append(ranked, candidates)
	}
	var union []assign.Candidate
	offered := map[string]bool{}
	for rank := 0; len(union) < FeatureBuildCandidates; rank++ {
		added := false
		for _, candidates := range ranked {
			if rank < len(candidates) && len(union) < FeatureBuildCandidates {
				added = true
				if c := candidates[rank]; !offered[c.ID] {
					offered[c.ID] = true
					c.Current, c.Links = false, assign.Links{}
					union = append(union, c)
				}
			}
		}
		if !added {
			break
		}
	}
	version := b.batch
	if slices.ContainsFunc(union, func(c assign.Candidate) bool { return len(c.PullRequests) == 0 }) {
		version = assign.Public.SeededBatch(b.batch)
	}
	byNumber := map[int32]int{}
	for i, pull := range pulls {
		byNumber[int32(pull.Number)] = i
	}
	batch := make([]assign.BatchItem, len(pulls))
	for i, pull := range pulls {
		batch[i] = assign.BatchItem{PullRequest: assign.PullRequest{Title: pull.Title, Body: pull.Body, Branch: pull.HeadRef, Paths: pull.Paths},
			Number: int32(pull.Number), Links: map[string]assign.Links{}}
		for _, c := range ranked[i] {
			if offered[c.ID] {
				batch[i].Links[c.ID] = c.Links
			}
		}
		for _, n := range assign.MentionedPullRequests(pull.Title, pull.Body) {
			if other, ok := byNumber[n]; ok {
				batch[i].Mentions = append(batch[i].Mentions, other)
			}
		}
	}
	rendered, err := assign.Public.RenderBatch(version, batch, union)
	if err != nil {
		return err
	}
	request, err := assign.Public.Request(version, rendered, assign.MaxBatchOutputTokens)
	if err != nil {
		return err
	}
	result, callErr := b.calls.complete(ctx, b.opts.Backend, b.opts.Model, Attempt{Repository: b.repository, Stage: StageAssign,
		Item: fmt.Sprintf("batch:%d-%d", pulls[0].Number, pulls[len(pulls)-1].Number)}, request)
	if result.Skip != "" {
		return errBuildStop{result.Skip}
	}
	if callErr != nil {
		return callErr
	}
	reply, err := assign.ParseBatchReply(result.JSON, len(pulls), union)
	if err != nil {
		return err
	}
	write := context.WithoutCancel(ctx)
	created := map[int]string{}
	for i, choice := range reply.Choices {
		pull := pulls[i]
		if choice.Skip {
			if err := s.settleItem(write, b.version, b.repository, kindPull, strconv.Itoa(pull.Number), pull.HeadSHA, "settled"); err != nil {
				return &buildWriteError{err}
			}
			b.report.Skipped++
			continue
		}
		target := placeTarget{FeatureID: choice.FeatureID}
		if choice.New >= 0 {
			if id, ok := created[choice.New]; ok {
				target = placeTarget{FeatureID: id}
			} else {
				target = placeTarget{Title: reply.New[choice.New].Title, Description: reply.New[choice.New].Description}
			}
		}
		id, err := s.placePull(write, b.version, b.repository, pull, target, b.now)
		if err != nil {
			return &buildWriteError{err}
		}
		if choice.New >= 0 && created[choice.New] == "" {
			created[choice.New] = id
			b.report.Created++
		}
		b.report.Placed++
	}
	return nil
}

// switchVersion makes the building version current in one transaction: the
// current one is retired, the pull requests left out lose their
// settlement so the live placement takes them, and, when its pass was made
// (consolidated), the switch is recorded as the repository's last
// consolidation pass.
func (s *Store) switchVersion(ctx context.Context, repository string, version int64, consolidated bool, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE feature_version SET state=? WHERE repository=? AND state=?`, VersionRetired, repository, VersionCurrent); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE feature_version SET state=?, switched_at=?, stopped='' WHERE id=? AND state=?`,
			VersionCurrent, now.UnixMilli(), version, VersionBuilding)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(err, fmt.Errorf("feature version %d is not being built", version))
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM feature_settle WHERE version_id=? AND outcome=?`, version, outcomeLeftOut); err != nil {
			return err
		}
		if !consolidated {
			return nil
		}
		fingerprint, err := featuresFingerprint(ctx, tx, version, repository)
		if err != nil {
			return err
		}
		return recordConsolidation(ctx, tx, repository, fingerprint, now)
	})
}

// addVersionSpend adds a build's call's cost to its version's spend.
func (s *Store) addVersionSpend(ctx context.Context, version int64, cost float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feature_version SET spent_usd=spent_usd+? WHERE id=?`, cost, version)
	return err
}
