package decisionlog

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/baum-ai/baum/features/consolidate"
)

// Consolidation (README.md, "Consolidation"): the weekly merge of duplicate
// features and split of oversized ones.

// StageConsolidate is the ledger stage of consolidation's calls.
const StageConsolidate = "feature_consolidate"

// assignedByConsolidation marks a pull request or session a merge or split
// moved.
const assignedByConsolidation = "consolidation"

// kindConsolidation is a repository's pass in feature_settle, keyed by the
// features' fingerprint.
const (
	kindConsolidation = "consolidation"
	consolidationItem = "repository"
)

// Outcomes of a consolidation pass besides those of placement.
const (
	ConsolidationDone      = "consolidated"
	ConsolidationNotDue    = "not_due"
	ConsolidationUnchanged = "unchanged"
	ConsolidationWaiting   = "waiting"
)

// ConsolidateReport says what a consolidation pass did; no feature or reply
// text.
type ConsolidateReport struct {
	Repository string `json:"repository"`
	Outcome    string `json:"outcome"`
	// Stopped is the reason a call was not made (a backend, price or cap
	// reason); what was called is still written.
	Stopped  string `json:"stopped,omitempty"`
	Features int    `json:"features"`
	// Merged counts the features merged into another, HeldMerges those the
	// split hold kept apart and InvalidMerges the reply's groups dropped or
	// cut.
	Merged        int `json:"merged"`
	HeldMerges    int `json:"held_merges"`
	InvalidMerges int `json:"invalid_merges"`
	// Split counts the features split, NewFeatures the parts they made,
	// KeptWhole those the reply kept whole or too large to offer and
	// InvalidSplits those whose reply was invalid or whose call failed.
	Split         int `json:"split"`
	NewFeatures   int `json:"new_features"`
	KeptWhole     int `json:"kept_whole"`
	InvalidSplits int `json:"invalid_splits"`
	// DiscardedMerges counts the merge reply's groups not written because a
	// split call failed or was refused.
	DiscardedMerges int `json:"discarded_merges,omitempty"`
	// Changed counts the merges and splits not written because their
	// features changed after they were read.
	Changed int     `json:"changed"`
	Spent   float64 `json:"spent_usd"`
	Error   string  `json:"error,omitempty"`
}

// Consolidate makes a consolidation pass over the repository's features
// when one is due: at most every FeatureConsolidateEvery, and only when the
// features changed since the last pass (any time with o.Manual).
func (w *FeatureWorker) Consolidate(ctx context.Context, repository, dir string, o FeatureOptions) (ConsolidateReport, error) {
	o.DryRun = false
	r, err := w.newRun(ctx, repository, dir, o)
	if err != nil {
		return ConsolidateReport{Repository: repository}, err
	}
	return r.consolidate(ctx, !o.Manual)
}

// consolidation is a repository's last pass.
func (s *Store) consolidation(ctx context.Context, repository string) (time.Time, string, error) {
	var at sql.NullInt64
	var fingerprint string
	err := s.db.QueryRowContext(ctx, `SELECT last_pass_at, fingerprint FROM feature_consolidation WHERE repository=?`, repository).Scan(&at, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", nil
	}
	if !at.Valid {
		return time.Time{}, fingerprint, err
	}
	return time.UnixMilli(at.Int64), fingerprint, err
}

// recordConsolidation records a pass over version started at with the
// features' fingerprint then.
func recordConsolidation(ctx context.Context, q querier, version int64, repository, fingerprint string, at time.Time) error {
	// Never for a version a build's switch retired.
	_, err := q.ExecContext(ctx, `INSERT INTO feature_consolidation(repository, last_pass_at, fingerprint)
SELECT ?1, ?2, ?3 WHERE EXISTS (SELECT 1 FROM feature_version WHERE id=?4 AND state != 'retired')
ON CONFLICT(repository) DO UPDATE SET last_pass_at=excluded.last_pass_at, fingerprint=excluded.fingerprint`, repository, at.UnixMilli(), fingerprint, version)
	return err
}

// featuresFingerprint identifies the version's features as consolidation
// reads them: every feature's id, membership and summarized versions and
// merged_into, merged ones included.
func featuresFingerprint(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, version int64, repository string) (string, error) {
	rows, err := collect(func(rows *sql.Rows) (string, error) {
		var id, merged string
		var membership, summarized int64
		err := rows.Scan(&id, &membership, &summarized, &merged)
		return fmt.Sprintf("%s:%d:%d:%s", id, membership, summarized, merged), err
	})(q.QueryContext(ctx, `SELECT id, membership_version, summarized_version, COALESCE(merged_into, '') FROM feature
WHERE version_id=? AND repository=? ORDER BY id`, version, repository))
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	for _, row := range rows {
		sum.Write([]byte(row + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// consolidationFeature is a feature a pass reads.
type consolidationFeature struct {
	*candidateFeature
	membership, checked int64
	splitFrom           string
	splitAt             time.Time
	sessions            int
	pulls               []consolidationPull
}

// consolidationPull is one of a feature's pull requests as a split reads it.
type consolidationPull struct {
	Number      int
	Title, Body string
	State       string
	Opened      time.Time
	Paths       []string
}

// consolidationFeatures reads the version's features not merged into
// another and active within FeatureCandidateWindow, most recently active
// first, with their pull requests and the sessions in them on their own
// that follow no feature pull request.
func (r *featureRun) consolidationFeatures(ctx context.Context) ([]*consolidationFeature, error) {
	s := r.w.Store
	found, err := s.candidateFeatures(ctx, r.version, r.repository, r.now.Add(-FeatureCandidateWindow), "")
	if err != nil || len(found) == 0 {
		return nil, err
	}
	byID := map[string]*consolidationFeature{}
	out := make([]*consolidationFeature, 0, len(found))
	for _, f := range found {
		c := &consolidationFeature{candidateFeature: f}
		byID[f.ID] = c
		out = append(out, c)
	}
	type meta struct {
		id, splitFrom       string
		membership, checked int64
		splitAt             int64
		sessions            int
	}
	metas, err := collect(func(rows *sql.Rows) (meta, error) {
		var m meta
		return m, rows.Scan(&m.id, &m.membership, &m.checked, &m.splitFrom, &m.splitAt, &m.sessions)
	})(s.db.QueryContext(ctx, `SELECT f.id, f.membership_version, f.split_checked_version, COALESCE(f.split_from, ''), COALESCE(f.split_at, 0),
(SELECT COUNT(*) FROM feature_session fs WHERE fs.feature_id=f.id AND NOT EXISTS (SELECT 1 FROM session_commit sc
  JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
  JOIN feature_pull fp ON fp.version_id=f.version_id AND fp.repository=pc.repository AND fp.number=pc.number
  WHERE sc.repository=fs.repository AND sc.agent=fs.agent AND sc.session_id=fs.session_id))
FROM feature f WHERE f.version_id=? AND f.repository=? AND f.merged_into IS NULL`, r.version, r.repository))
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if c := byID[m.id]; c != nil {
			c.membership, c.checked, c.splitFrom, c.sessions = m.membership, m.checked, m.splitFrom, m.sessions
			if m.splitAt > 0 {
				c.splitAt = time.UnixMilli(m.splitAt)
			}
		}
	}
	type pullRow struct {
		feature string
		pull    consolidationPull
		paths   string
		opened  int64
	}
	pulls, err := collect(func(rows *sql.Rows) (pullRow, error) {
		var p pullRow
		return p, rows.Scan(&p.feature, &p.pull.Number, &p.pull.Title, &p.pull.Body, &p.pull.State, &p.opened, &p.paths)
	})(s.db.QueryContext(ctx, `SELECT fp.feature_id, p.number, p.title, p.body, CASE WHEN p.merged_at IS NOT NULL THEN 'merged' ELSE p.state END,
p.created_at, fp.assigned_paths_json FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.version_id=? AND fp.repository=? ORDER BY p.created_at, p.number`, r.version, r.repository))
	if err != nil {
		return nil, err
	}
	for _, p := range pulls {
		c := byID[p.feature]
		if c == nil {
			continue
		}
		if err := json.Unmarshal([]byte(p.paths), &p.pull.Paths); err != nil {
			return nil, err
		}
		p.pull.Opened = time.UnixMilli(p.opened)
		c.pulls = append(c.pulls, p.pull)
	}
	slices.SortFunc(out, func(a, b *consolidationFeature) int {
		return cmp.Or(cmp.Compare(b.last, a.last), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// pullSummary is the summary a consolidation call shows for a pull
// request: its saved review's, else its generated one.
func (r *featureRun) shownSummary(ctx context.Context, number int, generated map[int]generatedSummary) string {
	if summary := r.reviewSummary(ctx, number); summary != "" {
		return summary
	}
	return generated[number].Summary
}

// heldApart reports whether a merge of a and b would undo a split of the
// last FeatureSplitHold: one is a part of the other, or both are parts of
// one split.
func heldApart(a, b *consolidationFeature, now time.Time) bool {
	recent := func(f *consolidationFeature) bool { return f.splitFrom != "" && now.Sub(f.splitAt) < FeatureSplitHold }
	return (recent(a) && a.splitFrom == b.ID) || (recent(b) && b.splitFrom == a.ID) ||
		((recent(a) || recent(b)) && a.splitFrom != "" && a.splitFrom == b.splitFrom)
}

// plannedSplit is a split call's feature and its validated reply.
type plannedSplit struct {
	feature *consolidationFeature
	pulls   []consolidationPull
	parts   *consolidate.Partition
	// checked records the feature as checked: kept whole or too large.
	checked bool
}

// consolidate makes one pass over the run's version (README.md,
// "Consolidation"); gated, only when one is due. Every call is made before
// anything is written. The pass is recorded unless the run builds a version,
// whose switch records it.
func (r *featureRun) consolidate(ctx context.Context, gated bool) (ConsolidateReport, error) {
	s := r.w.Store
	report := ConsolidateReport{Repository: r.repository}
	fingerprint, err := featuresFingerprint(ctx, s.db, r.version, r.repository)
	if err != nil {
		return report, err
	}
	if gated {
		last, previous, err := s.consolidation(ctx, r.repository)
		switch {
		case err != nil:
			return report, err
		case !last.IsZero() && r.now.Sub(last) < FeatureConsolidateEvery:
			report.Outcome = ConsolidationNotDue
			return report, nil
		case fingerprint == previous:
			report.Outcome = ConsolidationUnchanged
			return report, nil
		}
		settled, err := s.settleRows(ctx, r.version, r.repository)
		if err != nil {
			return report, err
		}
		if settled.waiting(kindConsolidation, consolidationItem, fingerprint, r.now) {
			report.Outcome = ConsolidationWaiting
			return report, nil
		}
	}
	found, err := r.consolidationFeatures(ctx)
	if err != nil {
		return report, err
	}
	report.Features = len(found)
	var numbers []int
	for _, f := range found {
		for _, p := range f.pulls {
			numbers = append(numbers, p.Number)
		}
	}
	generated, err := s.pullSummaries(ctx, r.repository, numbers)
	if err != nil {
		return report, err
	}
	addCall := func(result callResult) {
		if result.Cost != nil {
			report.Spent += *result.Cost
		}
	}

	// The merge call.
	var groups []consolidate.Group
	if len(found) >= 2 {
		inputs := make([]consolidate.Feature, len(found))
		index := map[string]int{}
		for i, f := range found {
			index[f.ID] = i
		}
		for i, f := range found {
			inputs[i] = consolidate.Feature{Title: f.Title, Description: f.Description, Digest: f.Digest, Paths: f.Paths, Sessions: f.sessions}
			for _, p := range f.pulls {
				inputs[i].PullRequests = append(inputs[i].PullRequests, consolidate.PullRequestTitle{
					Number: int32(p.Number), Title: p.Title, Summary: r.shownSummary(ctx, p.Number, generated)})
			}
			if j, ok := index[f.splitFrom]; ok && r.now.Sub(f.splitAt) < FeatureSplitHold {
				inputs[i].SplitFrom = &j
			}
		}
		rendered, err := consolidate.RenderMerge(inputs, consolidate.DefaultMaxInputBytes)
		if err != nil {
			return report, err
		}
		request, err := consolidate.Public.Request(ConsolidateVersion, consolidate.Merge, rendered.Input, FeatureConsolidateOutputTokens)
		if err != nil {
			return report, err
		}
		result, callErr := r.calls.complete(ctx, r.opts.Backend, r.opts.Model,
			Attempt{Repository: r.repository, Stage: StageConsolidate, Item: "merge"}, request)
		addCall(result)
		switch {
		case result.Skip != "":
			report.Stopped = result.Skip
			return report, callErr
		case callErr != nil && ctx.Err() != nil:
			return report, ctx.Err()
		case callErr != nil:
			return r.consolidationFailed(ctx, report, fingerprint, map[bool]int{true: 1}[result.Called], callErr)
		}
		var dropped int
		if groups, dropped, err = consolidate.ParseMergeReply(result.JSON, rendered); err != nil {
			// Paid for, and never written.
			return r.consolidationFailed(ctx, report, fingerprint, 1, err)
		}
		report.InvalidMerges = dropped
		// The split hold: a duplicate that would undo a recent split, of the
		// survivor or of a duplicate kept before it, is dropped.
		kept := groups[:0]
		for _, g := range groups {
			members := []int{g.Survivor}
			var duplicates []int
			for _, d := range g.Duplicates {
				if slices.ContainsFunc(members, func(m int) bool { return heldApart(found[m], found[d], r.now) }) {
					report.HeldMerges++
					continue
				}
				members, duplicates = append(members, d), append(duplicates, d)
			}
			if len(duplicates) > 0 {
				kept = append(kept, consolidate.Group{Survivor: g.Survivor, Duplicates: duplicates})
			}
		}
		groups = kept
	}

	// The split calls, on the features as they will be after the merges.
	after := map[int][]consolidationPull{}
	merged := map[int]bool{}
	for i, f := range found {
		after[i] = f.pulls
	}
	for _, g := range groups {
		merged[g.Survivor] = true
		for _, d := range g.Duplicates {
			after[g.Survivor] = append(after[g.Survivor], found[d].pulls...)
			delete(after, d)
		}
		slices.SortStableFunc(after[g.Survivor], func(a, b consolidationPull) int {
			return cmp.Or(a.Opened.Compare(b.Opened), cmp.Compare(a.Number, b.Number))
		})
	}
	var offered []int
	for i, pulls := range after {
		f := found[i]
		if len(pulls) == 0 || (!merged[i] && f.checked > 0 && f.checked == f.membership) {
			continue
		}
		if len(pulls) > FeatureSplitPulls || pulls[len(pulls)-1].Opened.Sub(pulls[0].Opened) > FeatureSplitAge {
			offered = append(offered, i)
		}
	}
	slices.SortFunc(offered, func(a, b int) int {
		return cmp.Or(cmp.Compare(len(after[b]), len(after[a])), cmp.Compare(found[a].ID, found[b].ID))
	})
	var splits []plannedSplit
	slots := 0
	for _, i := range offered {
		if slots >= FeatureMaxSplits {
			break
		}
		f, pulls := found[i], after[i]
		input := consolidate.SplitInput{Title: f.Title, Description: f.Description}
		for _, p := range pulls {
			input.PullRequests = append(input.PullRequests, consolidate.PullRequest{Number: int32(p.Number), Title: p.Title, Body: p.Body,
				Summary: r.shownSummary(ctx, p.Number, generated), State: p.State, OpenedAt: p.Opened, Paths: p.Paths})
		}
		rendered, err := consolidate.RenderSplit(input, consolidate.DefaultMaxInputBytes)
		if errors.Is(err, consolidate.ErrTooLarge) {
			// Takes no slot, and is not offered again until it changes.
			if !merged[i] {
				splits = append(splits, plannedSplit{feature: f, checked: true})
			}
			report.KeptWhole++
			continue
		}
		if err != nil {
			return report, err
		}
		request, err := consolidate.Public.Request(ConsolidateVersion, consolidate.Split, rendered.Input, FeatureConsolidateOutputTokens)
		if err != nil {
			return report, err
		}
		// A call takes a slot whatever its outcome.
		slots++
		result, callErr := r.calls.complete(ctx, r.opts.Backend, r.opts.Model,
			Attempt{Repository: r.repository, Stage: StageConsolidate, Item: kindFeature + ":" + f.ID}, request)
		addCall(result)
		switch {
		case result.Skip != "":
			// A call refused defers the pass: nothing is written, the merges
			// paid for included.
			report.Stopped, report.DiscardedMerges = result.Skip, len(groups)
			if len(groups) > 0 {
				r.w.logger().Warn("feature log: merges discarded, a split call was refused", "repository", r.repository,
					"groups", len(groups), "reason", result.Skip)
			}
			return report, callErr
		case callErr != nil && ctx.Err() != nil:
			return report, ctx.Err()
		case callErr != nil:
			// A failed call defers the pass too.
			report.DiscardedMerges = len(groups)
			return r.consolidationFailed(ctx, report, fingerprint, map[bool]int{true: 1}[result.Called], callErr)
		}
		partition, err := consolidate.ParseSplitReply(result.JSON, rendered)
		if err != nil {
			// Left whole and offered again once it changes.
			report.InvalidSplits++
			r.w.logger().Warn("feature log: split not made", "repository", r.repository, "error", err)
			continue
		}
		planned := plannedSplit{feature: f, pulls: pulls, checked: len(partition.Parts) == 1}
		if planned.checked {
			report.KeptWhole++
		} else {
			planned.parts = &partition
		}
		splits = append(splits, planned)
	}

	// The writes, each only while its features are as they were read, and
	// none when a build's switch retired the version meanwhile.
	write := context.WithoutCancel(ctx)
	if retired, err := r.retired(write); err != nil {
		return report, err
	} else if retired {
		report.Stopped = ReasonVersionRetired
		return report, nil
	}
	expected := map[string]int64{}
	for _, f := range found {
		expected[f.ID] = f.membership
	}
	for _, g := range groups {
		for _, d := range g.Duplicates {
			done, err := s.mergeFeature(write, r.version, r.repository, found[g.Survivor].ID, found[d].ID, expected, r.now)
			if err != nil {
				return r.consolidationFailed(write, report, fingerprint, 0, err)
			}
			if done {
				report.Merged++
			} else {
				report.Changed++
			}
		}
	}
	for _, planned := range splits {
		f := planned.feature
		if planned.checked {
			if _, err := s.db.ExecContext(write, `UPDATE feature SET split_checked_version=membership_version WHERE id=? AND membership_version=?
AND version_id IN (SELECT id FROM feature_version WHERE state != 'retired')`,
				f.ID, expected[f.ID]); err != nil {
				return r.consolidationFailed(write, report, fingerprint, 0, err)
			}
			continue
		}
		created, err := s.splitFeature(write, r.version, r.repository, f.ID, planned.pulls, *planned.parts, expected, r.now)
		if err != nil {
			return r.consolidationFailed(write, report, fingerprint, 0, err)
		}
		if created < 0 {
			report.Changed++
			continue
		}
		report.Split++
		report.NewFeatures += created
	}
	report.Outcome = ConsolidationDone
	if err := recordConsolidation(write, s.db, r.version, r.repository, fingerprint, r.now); err != nil {
		return report, err
	}
	r.w.logger().Info("feature consolidation", "repository", r.repository, "outcome", report.Outcome, "features", report.Features,
		"merged", report.Merged, "held_merges", report.HeldMerges, "invalid_merges", report.InvalidMerges, "split", report.Split,
		"invalid_splits", report.InvalidSplits, "changed", report.Changed)
	return report, s.settleItem(write, r.version, r.repository, kindConsolidation, consolidationItem, fingerprint, "")
}

// consolidationFailed backs the pass off, counting failed calls against the
// fingerprint.
func (r *featureRun) consolidationFailed(ctx context.Context, report ConsolidateReport, fingerprint string, failed int, cause error) (ConsolidateReport, error) {
	var err error
	report.Error = cause.Error()
	report.Outcome, err = r.backOff(ctx, kindConsolidation, consolidationItem, fingerprint, failed, cause)
	return report, err
}

// markExpected marks the features (markFeatures) and moves on the
// membership version a later write of the pass expects of each one still at
// the version expected.
func markExpected(ctx context.Context, tx *sql.Tx, ids []string, expected map[string]int64, now time.Time) error {
	before := map[string]int64{}
	for _, id := range ids {
		var v int64
		if err := tx.QueryRowContext(ctx, `SELECT membership_version FROM feature WHERE id=?`, id).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		before[id] = v
	}
	if err := markFeatures(ctx, tx, ids, now); err != nil {
		return err
	}
	for id, v := range before {
		if want, ok := expected[id]; ok && want == v {
			expected[id] = v + 1
		}
	}
	return nil
}

// movedSessionFeatures are the features of the sessions linked to the pull
// requests numbers' commits.
func movedSessionFeatures(ctx context.Context, tx *sql.Tx, version int64, repository string, numbers []int) ([]string, error) {
	var sessions []SessionKey
	for _, n := range numbers {
		linked, err := collect(scanSession)(tx.QueryContext(ctx, `SELECT DISTINCT sc.agent, sc.session_id FROM pull_commit pc
JOIN session_commit sc ON sc.repository=pc.repository AND sc.sha=pc.sha WHERE pc.repository=? AND pc.number=?`, repository, n))
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, linked...)
	}
	return sessionFeatures(ctx, tx, version, repository, sessions)
}

// writable reports whether version may still be written: a build's switch
// has not retired it.
func writable(ctx context.Context, tx *sql.Tx, version int64) (bool, error) {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM feature_version WHERE id=?`, version).Scan(&state); err != nil {
		return false, err
	}
	return state != VersionRetired, nil
}

// mergeFeature merges duplicate into survivor while both are at the
// membership versions expected and neither is merged: its pull requests and
// sessions move to the survivor as consolidation's, it points at the
// survivor (as do the features merged into it), and a more recent split
// origin passes to the survivor. It reports whether it merged.
func (s *Store) mergeFeature(ctx context.Context, version int64, repository, survivor, duplicate string, expected map[string]int64, now time.Time) (bool, error) {
	done := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if live, err := writable(ctx, tx, version); err != nil || !live {
			return err
		}
		for _, id := range []string{survivor, duplicate} {
			var membership int64
			var into sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT membership_version, merged_into FROM feature WHERE id=? AND version_id=?`, id, version).Scan(&membership, &into)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (into.Valid || membership != expected[id]) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		moved, err := collect(scanInt)(tx.QueryContext(ctx, `SELECT number FROM feature_pull WHERE version_id=? AND feature_id=?`, version, duplicate))
		if err != nil {
			return err
		}
		linked, err := movedSessionFeatures(ctx, tx, version, repository, moved)
		if err != nil {
			return err
		}
		for _, query := range []string{
			`UPDATE feature_pull SET feature_id=?1, assigned_by=?3, assigned_at=?4 WHERE version_id=?5 AND feature_id=?2`,
			`UPDATE feature_session SET feature_id=?1, assigned_at=?4 WHERE version_id=?5 AND feature_id=?2`,
			`UPDATE feature SET merged_into=?1 WHERE id=?2 OR merged_into=?2`,
			`UPDATE feature SET split_from=d.split_from, split_at=d.split_at FROM feature d
WHERE feature.id=?1 AND d.id=?2 AND d.split_at > COALESCE(feature.split_at, 0) AND d.split_from != ?1`,
		} {
			if _, err := tx.ExecContext(ctx, query, survivor, duplicate, assignedByConsolidation, now.UnixMilli(), version); err != nil {
				return err
			}
		}
		done = true
		return markExpected(ctx, tx, append([]string{survivor, duplicate}, linked...), expected, now)
	})
	return done, err
}

// splitFeature splits the feature into the partition's parts while it is
// at the membership version expected and holds exactly the pull requests
// pulls the partition places: the largest part keeps its id, and every
// other part becomes a new feature with the part's text, recording the
// feature split from and when, its pull requests moved as consolidation's
// at the head and paths they were placed with. Sessions in the feature on
// their own stay with it. Each part is recorded as checked, so it is not
// offered again until it changes. It returns the new features, -1 when the
// feature changed.
func (s *Store) splitFeature(ctx context.Context, version int64, repository, id string, pulls []consolidationPull, partition consolidate.Partition,
	expected map[string]int64, now time.Time) (int, error) {
	created := -1
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if live, err := writable(ctx, tx, version); err != nil || !live {
			return err
		}
		var membership int64
		var into sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT membership_version, merged_into FROM feature WHERE id=? AND version_id=?`, id, version).Scan(&membership, &into)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (into.Valid || membership != expected[id]) {
			return nil
		}
		if err != nil {
			return err
		}
		held, err := collect(scanInt)(tx.QueryContext(ctx, `SELECT number FROM feature_pull WHERE version_id=? AND feature_id=? ORDER BY number`, version, id))
		if err != nil {
			return err
		}
		want := make([]int, len(pulls))
		for i, p := range pulls {
			want[i] = p.Number
		}
		if slices.Sort(want); !slices.Equal(held, want) {
			return nil
		}
		ids := []string{id}
		var moved []int
		for i, part := range partition.Parts {
			if i == partition.Keeper {
				continue
			}
			part := part
			newID := uuid.NewString()
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature(id, version_id, repository, title, description, split_from, split_at, changed_at, created_at, updated_at)
VALUES (?1,?2,?3,?4,?5,?6,?7,?7,?7,?7)`, newID, version, repository, part.Title, part.Description, id, now.UnixMilli()); err != nil {
				return err
			}
			ids = append(ids, newID)
			for _, index := range part.PullRequests {
				number := pulls[index].Number
				moved = append(moved, number)
				if _, err := tx.ExecContext(ctx, `UPDATE feature_pull SET feature_id=?, assigned_by=?, assigned_at=? WHERE version_id=? AND repository=? AND number=?`,
					newID, assignedByConsolidation, now.UnixMilli(), version, repository, number); err != nil {
					return err
				}
			}
		}
		linked, err := movedSessionFeatures(ctx, tx, version, repository, moved)
		if err != nil {
			return err
		}
		if err := markExpected(ctx, tx, append(slices.Clone(ids), linked...), expected, now); err != nil {
			return err
		}
		for _, part := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE feature SET split_checked_version=membership_version WHERE id=?`, part); err != nil {
				return err
			}
		}
		created = len(ids) - 1
		return nil
	})
	return created, err
}

// ResolvedFeature is what a link to a feature shows.
type ResolvedFeature struct {
	// ID is the feature, followed to the one it was merged into.
	ID         string `json:"id"`
	Repository string `json:"repository"`
	// Archived is a feature of a retired version.
	Archived bool `json:"archived,omitempty"`
}

// ResolveFeature is the feature a link to id shows: followed to the feature
// it was merged into, archived when its version was retired.
// sql.ErrNoRows for an unknown feature or one of a version being built.
func (s *Store) ResolveFeature(ctx context.Context, id string) (ResolvedFeature, error) {
	var out ResolvedFeature
	for range 16 {
		var into sql.NullString
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT f.repository, f.merged_into, v.state FROM feature f JOIN feature_version v ON v.id=f.version_id
WHERE f.id=?`, id).Scan(&out.Repository, &into, &state); err != nil {
			return out, err
		}
		if state == VersionBuilding {
			return out, sql.ErrNoRows
		}
		out.ID, out.Archived = id, state == VersionRetired
		if !into.Valid {
			return out, nil
		}
		id = into.String
	}
	return out, errors.New("feature " + strconv.Quote(id) + " is merged too many times over")
}
