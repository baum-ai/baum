package decisionlog

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/baum-ai/baum/features"
	"github.com/baum-ai/baum/features/assign"
)

// Placement (README.md, "Placement"): the items to place, their candidate
// features, the call and the writes.

// Item kinds of feature_settle and the ledger.
const (
	kindPull    = "pull"
	kindSession = "session"
	kindFeature = "feature"
)

// Triggers of a placement.
const (
	TriggerOpened  = "opened"
	TriggerMerged  = "merged"
	TriggerUpdated = "updated"
	TriggerQuiet   = "quiet"
)

// Outcomes of an item placed or a feature's text.
const (
	FeatureAssigned = "assigned"
	FeatureCreated  = "created"
	FeatureSkipped  = "skipped"
	FeatureDeferred = "deferred"
	FeatureGivenUp  = "given_up"
	FeatureDryRun   = "dry_run"
)

// Ledger stages of the feature log's calls.
const (
	StageAssign      = "feature_assign"
	StageSummary     = "feature_summary"
	StagePullSummary = "pr_summary"
)

// assignedByAssigner marks a placement the assigner made.
const assignedByAssigner = "assigner"

// assignItem is a pull request or session to place, and why.
type assignItem struct {
	Kind, Trigger string
	// Key is the trigger's: a pull request's head, "merged", or a session's
	// extraction.
	Key     string
	Number  int
	Session SessionKey
	at      int64
}

func (i assignItem) name() string {
	if i.Kind == kindPull {
		return strconv.Itoa(i.Number)
	}
	return i.Session.Agent + "/" + i.Session.ID
}

// AssignReport says what Assign did.
type AssignReport struct {
	Repository string `json:"repository"`
	// SmallUpdates are the heads judged too small a change to place again.
	SmallUpdates int          `json:"small_updates"`
	Items        []PlacedItem `json:"items"`
	// Stopped is the reason no further call was made (a backend, price or
	// cap reason).
	Stopped string  `json:"stopped,omitempty"`
	Spent   float64 `json:"spent_usd"`
}

// PlacedItem is one item Assign took.
type PlacedItem struct {
	Kind    string `json:"kind"`
	Trigger string `json:"trigger"`
	Number  int    `json:"number,omitempty"`
	Title   string `json:"title,omitempty"`
	Session string `json:"session,omitempty"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Feature string `json:"feature,omitempty"`
	// FeatureTitle is a new feature's provisional title.
	FeatureTitle string `json:"feature_title,omitempty"`
	// Candidates are the candidate features' titles, in the order offered.
	Candidates []string `json:"candidates"`
	InputBytes int      `json:"input_bytes"`
	Cost       *float64 `json:"cost_usd,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Assign places the repository's pending pull requests and sessions, oldest
// trigger first, at most FeatureAssignRunLimit (or o.Limit) of them. A
// backend, price or cap reason stops it until the next run.
func (w *FeatureWorker) Assign(ctx context.Context, repository, dir string, o FeatureOptions) (AssignReport, error) {
	report := AssignReport{Repository: repository, Items: []PlacedItem{}}
	r, err := w.newRun(ctx, repository, dir, o)
	if err != nil {
		return report, err
	}
	activated, err := r.activation(ctx)
	if err != nil {
		return report, err
	}
	items, small, err := r.pendingPlacements(ctx, activated)
	report.SmallUpdates = small
	if err != nil {
		return report, err
	}
	limit := FeatureAssignRunLimit
	if o.Limit > 0 {
		limit = o.Limit
	}
	for _, item := range items[:min(len(items), limit)] {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if retired, err := r.retired(ctx); err != nil {
			return report, err
		} else if retired {
			report.Stopped = ReasonVersionRetired
			return report, nil
		}
		if on, err := r.refresh(ctx); err != nil {
			report.Stopped = ReasonPreferences
			return report, err
		} else if !on {
			report.Stopped = ReasonDisabled
			return report, nil
		}
		placed, err := r.place(ctx, item)
		if placed.Cost != nil {
			report.Spent += *placed.Cost
		}
		if placed.Outcome == "" && placed.Reason != "" {
			report.Stopped = placed.Reason
		}
		if err != nil {
			return report, err
		}
		if placed.Outcome == "" {
			break
		}
		report.Items = append(report.Items, placed)
		w.logger().Info("feature placement", "repository", repository, "kind", item.Kind, "trigger", item.Trigger,
			"outcome", placed.Outcome, "reason", placed.Reason, "feature", placed.Feature, "candidates", len(placed.Candidates))
	}
	return report, nil
}

// pendingPlacements are the items to place, oldest trigger first, without
// those settled for their trigger, backing off or given up on. An open pull
// request's head that changed too little is recorded as checked (once,
// before the items are chosen) and counted.
func (r *featureRun) pendingPlacements(ctx context.Context, activated time.Time) ([]assignItem, int, error) {
	s, version := r.w.Store, r.version
	opened := later(activated, r.now.Add(-FeatureLookback))
	// A pull request in no feature, opened since the activation and within
	// the lookback, once its paths are known or it waited for them.
	items, err := collect(func(rows *sql.Rows) (assignItem, error) {
		item := assignItem{Kind: kindPull, Trigger: TriggerOpened}
		return item, rows.Scan(&item.Number, &item.Key, &item.at)
	})(s.db.QueryContext(ctx, `SELECT number, head_sha, created_at FROM pull_request p WHERE repository=?1 AND bot=0
AND created_at >= ?2 AND ((paths_head_sha = head_sha AND paths_json IS NOT NULL) OR created_at <= ?3)
AND NOT EXISTS (SELECT 1 FROM feature_pull fp WHERE fp.version_id=?4 AND fp.repository=p.repository AND fp.number=p.number)`,
		r.repository, opened.UnixMilli(), r.now.Add(-FeatureQuiet).UnixMilli(), version))
	if err != nil {
		return nil, 0, err
	}
	// A pull request the assigner placed that merged since, once.
	merged, err := collect(func(rows *sql.Rows) (assignItem, error) {
		item := assignItem{Kind: kindPull, Trigger: TriggerMerged, Key: TriggerMerged}
		return item, rows.Scan(&item.Number, &item.at)
	})(s.db.QueryContext(ctx, `SELECT p.number, p.merged_at FROM feature_pull fp
JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.version_id=?1 AND fp.repository=?2 AND fp.assigned_by=?3 AND fp.assigned_merged=0 AND p.merged_at IS NOT NULL`,
		version, r.repository, assignedByAssigner))
	if err != nil {
		return nil, 0, err
	}
	items = append(items, merged...)
	// An open pull request whose new head, its paths known, changed them
	// enough; one that did not is checked once.
	type moved struct {
		number          int
		head            string
		paths, assigned string
		at              int64
	}
	heads, err := collect(func(rows *sql.Rows) (moved, error) {
		var m moved
		return m, rows.Scan(&m.number, &m.head, &m.paths, &m.assigned, &m.at)
	})(s.db.QueryContext(ctx, `SELECT p.number, p.head_sha, p.paths_json, fp.assigned_paths_json, p.updated_at FROM feature_pull fp
JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.version_id=?1 AND fp.repository=?2 AND p.state='open' AND p.merged_at IS NULL AND p.head_sha != fp.assigned_head_sha
AND p.head_sha != fp.checked_head_sha AND p.paths_head_sha = p.head_sha AND p.paths_json IS NOT NULL`, version, r.repository))
	if err != nil {
		return nil, 0, err
	}
	small := 0
	for _, m := range heads {
		var current, assigned []string
		if err := errors.Join(json.Unmarshal([]byte(m.paths), &current), json.Unmarshal([]byte(m.assigned), &assigned)); err != nil {
			return nil, 0, err
		}
		if features.LargeUpdate(assigned, current, FeatureReassignShare, FeatureReassignPaths) {
			items = append(items, assignItem{Kind: kindPull, Trigger: TriggerUpdated, Key: m.head, Number: m.number, at: m.at})
			continue
		}
		small++
		if !r.opts.DryRun {
			if _, err := s.db.ExecContext(ctx, `UPDATE feature_pull SET checked_head_sha=? WHERE version_id=? AND repository=? AND number=?`,
				m.head, version, r.repository, m.number); err != nil {
				return nil, 0, err
			}
		}
	}
	// A captured session with current decisions, quiet since its last
	// extraction, in no feature and linked to no pull request's commit.
	sessions, err := collect(func(rows *sql.Rows) (assignItem, error) {
		item := assignItem{Kind: kindSession, Trigger: TriggerQuiet}
		return item, rows.Scan(&item.Session.Agent, &item.Session.ID, &item.Key, &item.at)
	})(s.db.QueryContext(ctx, `SELECT cs.agent, cs.session_id, cs.content_hash, cs.extracted_at FROM capture_session cs
WHERE cs.repository=?1 AND cs.extracted_at >= ?2 AND cs.extracted_at <= ?3
AND EXISTS (SELECT 1 FROM current_decision d WHERE d.agent=cs.agent AND d.session_id=cs.session_id AND d.superseded=0)
AND NOT EXISTS (SELECT 1 FROM feature_session fs WHERE fs.version_id=?4 AND fs.repository=cs.repository AND fs.agent=cs.agent AND fs.session_id=cs.session_id)
AND NOT EXISTS (SELECT 1 FROM session_commit sc JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
  WHERE sc.repository=cs.repository AND sc.agent=cs.agent AND sc.session_id=cs.session_id)`,
		r.repository, later(activated.Add(-FeatureQuiet), r.now.Add(-FeatureLookback)).UnixMilli(), r.now.Add(-FeatureQuiet).UnixMilli(), version))
	if err != nil {
		return nil, 0, err
	}
	items = append(items, sessions...)

	settled, err := s.settleRows(ctx, version, r.repository)
	if err != nil {
		return nil, 0, err
	}
	items = slices.DeleteFunc(items, func(item assignItem) bool {
		return settled.waiting(item.Kind, item.name(), item.Key, r.now)
	})
	slices.SortStableFunc(items, func(a, b assignItem) int { return cmp.Compare(a.at, b.at) })
	return items, small, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// placedPull is what placing a pull request reads.
type placedPull struct {
	Number                        int
	Title, Body, HeadRef, HeadSHA string
	Paths                         []string
	Merged                        bool
	// Feature is the feature it is in, "" for none.
	Feature string
}

func (s *Store) placedPull(ctx context.Context, version int64, repository string, number int) (placedPull, error) {
	p := placedPull{Number: number}
	var paths sql.NullString
	var merged sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT p.title, p.body, p.head_ref, p.head_sha, p.paths_json, p.merged_at, COALESCE(fp.feature_id, '')
FROM pull_request p LEFT JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=p.repository AND fp.number=p.number
WHERE p.repository=?2 AND p.number=?3`, version, repository, number).
		Scan(&p.Title, &p.Body, &p.HeadRef, &p.HeadSHA, &paths, &merged, &p.Feature)
	if err != nil {
		return p, err
	}
	p.Merged, p.Paths = merged.Valid, []string{}
	if paths.Valid {
		err = json.Unmarshal([]byte(paths.String), &p.Paths)
	}
	return p, err
}

// place places one item and settles it: placed, settled for its trigger,
// or backing off. An item with no outcome was not called for the reason it
// gives, and stops the run; an error is the store's.
func (r *featureRun) place(ctx context.Context, item assignItem) (PlacedItem, error) {
	out := PlacedItem{Kind: item.Kind, Trigger: item.Trigger, Number: item.Number, Candidates: []string{}}
	var input assign.Item
	var pull placedPull
	switch item.Kind {
	case kindPull:
		var err error
		if pull, err = r.w.Store.placedPull(ctx, r.version, r.repository, item.Number); err != nil {
			return out, err
		}
		out.Title = pull.Title
		input.PullRequest = &assign.PullRequest{Title: pull.Title, Body: pull.Body, Branch: pull.HeadRef, Paths: pull.Paths}
	default:
		out.Session = item.name()
		current, err := r.w.Store.Current(ctx, item.Session)
		if err != nil {
			return out, err
		}
		session := &assign.Session{}
		for _, d := range current {
			if !d.Superseded {
				session.Decisions = append(session.Decisions, assign.Decision{Category: string(d.Category), Summary: d.Summary, Files: d.Files})
			}
		}
		if len(session.Decisions) == 0 {
			return r.settle(ctx, item, out, "no_decisions")
		}
		input.Session = session
	}
	candidates, err := r.candidates(ctx, input, pull)
	if err != nil {
		return out, err
	}
	for _, c := range candidates {
		out.Candidates = append(out.Candidates, c.Title)
	}
	if item.Kind == kindSession && len(candidates) == 0 {
		// A session only joins a feature: with none to join, no call.
		return r.settle(ctx, item, out, "no_candidates")
	}
	rendered, err := assign.Public.Render(AssignVersion, input, candidates)
	if err != nil {
		return out, err
	}
	out.InputBytes = len(rendered)
	if r.opts.DryRun {
		out.Outcome = FeatureDryRun
		return out, nil
	}
	request, err := assign.Public.Request(AssignVersion, rendered, assign.MaxOutputTokens)
	if err != nil {
		return out, err
	}
	result, callErr := r.calls.complete(ctx, r.opts.Backend, r.opts.Model,
		Attempt{Repository: r.repository, Stage: StageAssign, Item: item.Kind + ":" + item.name()}, request)
	out.Cost = result.Cost
	switch {
	case result.Skip != "":
		out.Reason = result.Skip
		return out, callErr
	case callErr != nil && ctx.Err() != nil:
		// Cut short by the daemon stopping: not the item's failure.
		return out, ctx.Err()
	case callErr != nil:
		out.Outcome, err = r.backOff(ctx, item.Kind, item.name(), item.Key, map[bool]int{true: 1}[result.Called], callErr)
		out.Error = callErr.Error()
		return out, err
	}
	choice, err := assign.Public.ParseReply(AssignVersion, result.JSON, candidates)
	if err != nil {
		// Paid for, and never written.
		out.Error = err.Error()
		out.Outcome, err = r.backOff(ctx, item.Kind, item.name(), item.Key, 1, err)
		return out, err
	}
	if item.Kind == kindSession && (choice.New != nil || choice.Skip) {
		// A session never starts a feature.
		return r.settle(ctx, item, out, "no_fit")
	}
	if choice.Skip && pull.Feature == "" {
		// A small change continuing no feature stays in none until its head
		// changes.
		return r.settle(ctx, item, out, "small_change")
	}
	target := placeTarget{FeatureID: choice.FeatureID}
	out.Outcome = FeatureAssigned
	switch {
	case choice.Skip:
		// A pull request already in a feature stays in it.
		target.FeatureID = pull.Feature
	case choice.New != nil:
		target = placeTarget{Title: choice.New.Title, Description: choice.New.Description}
		out.Outcome, out.FeatureTitle = FeatureCreated, choice.New.Title
	}
	write := context.WithoutCancel(ctx)
	if item.Kind == kindPull {
		out.Feature, err = r.w.Store.placePull(write, r.version, r.repository, pull, target, r.now)
	} else {
		out.Feature, err = r.w.Store.placeSession(write, r.version, r.repository, item.Session, target.FeatureID, r.now)
	}
	if errors.Is(err, errVersionRetired) {
		// A build's switch retired the version: the run stops.
		out.Reason = ReasonVersionRetired
		return out, nil
	}
	if err != nil {
		// Paid for but not kept.
		out.Error = err.Error()
		out.Outcome, err = r.backOff(write, item.Kind, item.name(), item.Key, 1, err)
		return out, err
	}
	return out, r.w.Store.settleItem(write, r.version, r.repository, item.Kind, item.name(), item.Key, "")
}

// settle leaves the item as it is until its next trigger.
func (r *featureRun) settle(ctx context.Context, item assignItem, out PlacedItem, reason string) (PlacedItem, error) {
	out.Outcome, out.Reason = FeatureSkipped, reason
	if r.opts.DryRun {
		return out, nil
	}
	return out, r.w.Store.settleItem(context.WithoutCancel(ctx), r.version, r.repository, item.Kind, item.name(), item.Key, "settled")
}

// backOff backs an item or feature off after a failure, counting failed
// calls against its trigger key; at FeatureMaxFailures it waits for the next
// trigger. It returns the outcome.
func (r *featureRun) backOff(ctx context.Context, kind, item, key string, failed int, cause error) (string, error) {
	count, err := r.w.Store.deferItem(context.WithoutCancel(ctx), r.version, r.repository, kind, item, key, failed, r.now.Add(FeatureBackoff))
	r.w.logger().Warn("feature log: backing off", "repository", r.repository, "kind", kind, "error", cause)
	if count >= FeatureMaxFailures {
		return FeatureGivenUp, err
	}
	return FeatureDeferred, err
}

// candidateFeature is a feature an item may join, as ranked.
type candidateFeature struct {
	ID, Title, Description, Digest string
	last                           int64
	Paths                          []string
	// PullRequests are its pull requests, earliest opened first.
	PullRequests []candidatePull
	Links        assign.Links

	score                   float64
	current, linked, byPath bool
}

type candidatePull struct {
	Number int
	Title  string
}

// candidates are the item's candidate features as the call shows them
// (rankCandidates), each with its digest and its pull requests' summaries.
func (r *featureRun) candidates(ctx context.Context, input assign.Item, pull placedPull) ([]assign.Candidate, error) {
	s := r.w.Store
	found, err := s.candidateFeatures(ctx, r.version, r.repository, r.now.Add(-FeatureCandidateWindow), pull.Feature)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	if input.PullRequest != nil {
		links, err := s.pullLinks(ctx, r.repository, pull.Number, assign.MentionedPullRequests(pull.Title, pull.Body))
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			for kind, list := range map[string]*[]int32{"stacked": &f.Links.Stacked, "sessions": &f.Links.SharedSessions, "mentioned": &f.Links.Mentioned} {
				for _, p := range f.PullRequests {
					if links[kind][p.Number] {
						*list = append(*list, int32(p.Number))
					}
				}
				// Newest first.
				slices.Reverse(*list)
			}
		}
	}
	ranked := rankCandidates(found, input.Paths(), pull.Feature, FeatureCandidates, FeaturePathSlots)
	out := make([]assign.Candidate, len(ranked))
	// Only the sample the input shows needs its summaries.
	sampled := map[int]bool{}
	for i, f := range ranked {
		out[i] = assign.Candidate{ID: f.ID, Title: f.Title, Description: f.Description, Paths: f.Paths, Current: f.current,
			Digest: f.Digest, Links: f.Links}
		out[i].PullRequests = make([]assign.CandidatePullRequest, len(f.PullRequests))
		for j, p := range f.PullRequests {
			out[i].PullRequests[j].Title = p.Title
		}
		for _, j := range assign.SampledPullRequests(len(f.PullRequests)) {
			sampled[f.PullRequests[j].Number] = true
		}
	}
	generated, err := s.pullSummaries(ctx, r.repository, slices.Collect(maps.Keys(sampled)))
	if err != nil {
		return nil, err
	}
	for i, f := range ranked {
		for _, j := range assign.SampledPullRequests(len(f.PullRequests)) {
			number := f.PullRequests[j].Number
			if out[i].PullRequests[j].Summary = r.reviewSummary(ctx, number); out[i].PullRequests[j].Summary == "" {
				out[i].PullRequests[j].Summary = generated[number].Summary
			}
		}
	}
	return out, nil
}

// rankCandidates ranks features for an item touching paths, as the hosted
// weighted ranking does: each of the item's paths a feature holds counts one
// over the number of features holding it. The item's current feature comes
// first, then the features linked to it, best path score first, then the
// best path scores of the rest (at most slots of them), then the most
// recently active; at most k in all.
func rankCandidates(found []*candidateFeature, paths []string, current string, k, slots int) []*candidateFeature {
	holders := map[string]int{}
	for _, f := range found {
		for _, path := range f.Paths {
			if slices.Contains(paths, path) {
				holders[path]++
			}
		}
	}
	var rest []*candidateFeature
	for _, f := range found {
		f.score = 0
		for _, path := range f.Paths {
			if n := holders[path]; n > 0 {
				f.score += 1 / float64(n)
			}
		}
		f.current = f.ID == current && current != ""
		f.linked = len(f.Links.Stacked)+len(f.Links.SharedSessions)+len(f.Links.Mentioned) > 0
		f.byPath = false
		if !f.current && !f.linked && f.score > 0 {
			rest = append(rest, f)
		}
	}
	byRecency := func(a, b *candidateFeature) int { return cmp.Or(cmp.Compare(b.last, a.last), cmp.Compare(a.ID, b.ID)) }
	slices.SortFunc(rest, func(a, b *candidateFeature) int { return cmp.Or(cmp.Compare(b.score, a.score), byRecency(a, b)) })
	for _, f := range rest[:min(len(rest), slots)] {
		f.byPath = true
	}
	ranked := slices.Clone(found)
	flag := func(b bool) int { return map[bool]int{true: 1}[b] }
	slices.SortFunc(ranked, func(a, b *candidateFeature) int {
		scored := func(f *candidateFeature) float64 {
			if f.byPath || f.linked {
				return f.score
			}
			return 0
		}
		return cmp.Or(cmp.Compare(flag(b.current), flag(a.current)), cmp.Compare(flag(b.linked), flag(a.linked)),
			cmp.Compare(flag(b.byPath), flag(a.byPath)), cmp.Compare(scored(b), scored(a)), byRecency(a, b))
	})
	return ranked[:min(len(ranked), k)]
}

// candidateFeatures reads the version's features not merged into another
// and active since (or current): their text, latest activity (a change to
// the feature or to one of its pull requests or their placements), paths
// (those its pull requests were placed with and those cited by the
// decisions of its sessions in it on their own that follow no feature pull
// request) and pull requests.
func (s *Store) candidateFeatures(ctx context.Context, version int64, repository string, since time.Time, current string) ([]*candidateFeature, error) {
	found, err := collect(func(rows *sql.Rows) (*candidateFeature, error) {
		f := &candidateFeature{}
		return f, rows.Scan(&f.ID, &f.Title, &f.Description, &f.Digest, &f.last)
	})(s.db.QueryContext(ctx, `SELECT f.id, f.title, f.description, f.digest,
MAX(f.updated_at, COALESCE(MAX(MAX(p.created_at, p.updated_at, COALESCE(p.merged_at, 0), COALESCE(p.closed_at, 0), fp.assigned_at)), 0)) AS last
FROM feature f LEFT JOIN feature_pull fp ON fp.version_id=f.version_id AND fp.feature_id=f.id
LEFT JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE f.version_id=?1 AND f.repository=?2 AND f.merged_into IS NULL GROUP BY f.id HAVING last >= ?3 OR f.id = ?4`,
		version, repository, since.UnixMilli(), current))
	if err != nil || len(found) == 0 {
		return nil, err
	}
	byID := map[string]*candidateFeature{}
	for _, f := range found {
		byID[f.ID] = f
	}
	type member struct {
		feature string
		pull    candidatePull
		paths   string
	}
	members, err := collect(func(rows *sql.Rows) (member, error) {
		var m member
		return m, rows.Scan(&m.feature, &m.pull.Number, &m.pull.Title, &m.paths)
	})(s.db.QueryContext(ctx, `SELECT fp.feature_id, p.number, p.title, fp.assigned_paths_json FROM feature_pull fp
JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.version_id=? AND fp.repository=? ORDER BY p.created_at, p.number`, version, repository))
	if err != nil {
		return nil, err
	}
	paths := map[string]map[string]bool{}
	hold := func(id string, held ...string) {
		if paths[id] == nil {
			paths[id] = map[string]bool{}
		}
		for _, path := range held {
			paths[id][path] = true
		}
	}
	for _, m := range members {
		f := byID[m.feature]
		if f == nil {
			continue
		}
		f.PullRequests = append(f.PullRequests, m.pull)
		var held []string
		if err := json.Unmarshal([]byte(m.paths), &held); err != nil {
			return nil, err
		}
		hold(m.feature, held...)
	}
	cited, err := collect(func(rows *sql.Rows) ([2]string, error) {
		var pair [2]string
		return pair, rows.Scan(&pair[0], &pair[1])
	})(s.db.QueryContext(ctx, `SELECT DISTINCT fs.feature_id, df.path FROM feature_session fs
JOIN capture_session cs ON cs.agent=fs.agent AND cs.session_id=fs.session_id
JOIN current_decision d ON d.agent=fs.agent AND d.session_id=fs.session_id
JOIN decision_file df ON df.decision_id=d.decision_id
WHERE fs.version_id=?1 AND fs.repository=?2 AND NOT EXISTS (SELECT 1 FROM session_commit sc
  JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
  JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=pc.repository AND fp.number=pc.number
  WHERE sc.repository=fs.repository AND sc.agent=fs.agent AND sc.session_id=fs.session_id)`, version, repository))
	if err != nil {
		return nil, err
	}
	for _, pair := range cited {
		hold(pair[0], pair[1])
	}
	for _, f := range found {
		f.Paths = slices.Sorted(maps.Keys(paths[f.ID]))
	}
	return found, nil
}

// pullLinks are the repository's pull requests linked to pull request
// number, by kind ("stacked", "sessions", "mentioned"): those it is stacked
// on or that are stacked on it (one's base branch is the other's head
// branch, opened from by one pull request only), those sharing a session
// with its commits, and mentioned.
func (s *Store) pullLinks(ctx context.Context, repository string, number int, mentioned []int32) (map[string]map[int]bool, error) {
	links := map[string]map[int]bool{"stacked": {}, "sessions": {}, "mentioned": {}}
	for _, n := range mentioned {
		if int(n) != number {
			links["mentioned"][int(n)] = true
		}
	}
	for kind, query := range map[string]string{
		"stacked": `SELECT o.number FROM pull_request i JOIN pull_request o ON o.repository=i.repository AND o.number != i.number
WHERE i.repository=?1 AND i.number=?2 AND i.cross_repository=0 AND o.cross_repository=0 AND i.default_branch != ''
AND ((o.head_ref = i.base_ref AND i.base_ref NOT IN ('', i.default_branch))
  OR (o.base_ref = i.head_ref AND i.head_ref NOT IN ('', i.default_branch)))
AND (SELECT COUNT(*) FROM pull_request same WHERE same.repository=i.repository
  AND same.head_ref = CASE WHEN o.head_ref = i.base_ref THEN i.base_ref ELSE i.head_ref END) = 1`,
		"sessions": `SELECT DISTINCT other.number FROM pull_commit item
JOIN session_commit linked ON linked.repository=item.repository AND linked.sha=item.sha
JOIN session_commit shared ON shared.repository=linked.repository AND shared.agent=linked.agent AND shared.session_id=linked.session_id
JOIN pull_commit other ON other.repository=shared.repository AND other.sha=shared.sha
WHERE item.repository=?1 AND item.number=?2 AND other.number != item.number`,
	} {
		numbers, err := collect(scanInt)(s.db.QueryContext(ctx, query, repository, number))
		if err != nil {
			return nil, err
		}
		for _, n := range numbers {
			links[kind][n] = true
		}
	}
	return links, nil
}

// errVersionRetired is a write to a feature version a build's switch
// retired.
var errVersionRetired = errors.New("the feature version was retired")

// ReasonVersionRetired stops a live run whose version a build's switch
// retired; the next run uses the new current version.
const ReasonVersionRetired = "version_retired"

// retired reports whether a build's switch retired the run's version.
func (r *featureRun) retired(ctx context.Context) (bool, error) {
	var state string
	err := r.w.Store.db.QueryRowContext(ctx, `SELECT state FROM feature_version WHERE id=?`, r.version).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == VersionRetired, err
}

// placeTarget is an existing feature, or a new one's provisional text.
type placeTarget struct {
	FeatureID          string
	Title, Description string
}

// target resolves an existing feature, followed to the feature it was
// merged into, or creates a new one; errVersionRetired for a retired
// version.
func target(ctx context.Context, tx *sql.Tx, version int64, repository string, t placeTarget, now time.Time) (string, error) {
	// A run that raced a build's switch never writes the retired version.
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM feature_version WHERE id=?`, version).Scan(&state); err != nil {
		return "", err
	}
	if state == VersionRetired {
		return "", errVersionRetired
	}
	if t.FeatureID == "" {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx, `INSERT INTO feature(id, version_id, repository, title, description, changed_at, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?)`, id, version, repository, t.Title, t.Description, now.UnixMilli(), now.UnixMilli(), now.UnixMilli())
		return id, err
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(merged_into, id) FROM feature WHERE id=? AND version_id=?`, t.FeatureID, version).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("feature %s is not in the repository's feature version", t.FeatureID)
	}
	return id, err
}

// placePull puts the pull request in the target at its head and paths,
// marking every feature that changed: the target, the one it left and the
// features of its linked sessions. An identical placement marks nothing.
func (s *Store) placePull(ctx context.Context, version int64, repository string, pull placedPull, t placeTarget, now time.Time) (string, error) {
	paths, err := json.Marshal(pull.Paths)
	if err != nil {
		return "", err
	}
	var id string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if id, err = target(ctx, tx, version, repository, t, now); err != nil {
			return err
		}
		var previous, head, held string
		err := tx.QueryRowContext(ctx, `SELECT feature_id, assigned_head_sha, assigned_paths_json FROM feature_pull
WHERE version_id=? AND repository=? AND number=?`, version, repository, pull.Number).Scan(&previous, &head, &held)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at,
assigned_head_sha, assigned_paths_json, assigned_merged) VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(version_id, repository, number) DO UPDATE SET feature_id=excluded.feature_id, assigned_by=excluded.assigned_by,
assigned_at=excluded.assigned_at, assigned_head_sha=excluded.assigned_head_sha, assigned_paths_json=excluded.assigned_paths_json,
assigned_merged=excluded.assigned_merged, checked_head_sha=''`,
			version, repository, pull.Number, id, assignedByAssigner, now.UnixMilli(), pull.HeadSHA, string(paths), pull.Merged); err != nil {
			return err
		}
		if previous == id && head == pull.HeadSHA && held == string(paths) {
			return nil
		}
		marked := []string{id, previous}
		sessions, err := collect(scanSession)(tx.QueryContext(ctx, `SELECT DISTINCT sc.agent, sc.session_id FROM pull_commit pc
JOIN session_commit sc ON sc.repository=pc.repository AND sc.sha=pc.sha WHERE pc.repository=? AND pc.number=?`, repository, pull.Number))
		if err != nil {
			return err
		}
		linked, err := sessionFeatures(ctx, tx, version, repository, sessions)
		if err != nil {
			return err
		}
		return markFeatures(ctx, tx, append(marked, linked...), now)
	})
	return id, err
}

// placeSession puts a session in no feature's pull request in an existing
// feature, and marks it.
func (s *Store) placeSession(ctx context.Context, version int64, repository string, session SessionKey, featureID string, now time.Time) (string, error) {
	var id string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = target(ctx, tx, version, repository, placeTarget{FeatureID: featureID}, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at)
VALUES (?,?,?,?,?,?) ON CONFLICT(version_id, repository, agent, session_id) DO UPDATE SET feature_id=excluded.feature_id,
assigned_at=excluded.assigned_at`, version, repository, session.Agent, session.ID, id, now.UnixMilli()); err != nil {
			return err
		}
		return markFeatures(ctx, tx, []string{id}, now)
	})
	return id, err
}

// sessionFeatures are the features the sessions' decisions may be
// attributed to: those of the pull requests their commits are linked to,
// and those they are in on their own.
func sessionFeatures(ctx context.Context, tx *sql.Tx, version int64, repository string, sessions []SessionKey) ([]string, error) {
	sessions = slices.Compact(slices.SortedFunc(slices.Values(sessions), func(a, b SessionKey) int {
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.ID, b.ID))
	}))
	var ids []string
	for chunk := range slices.Chunk(sessions, maxParameters/2) {
		found, err := chunkFeatures(ctx, tx, version, repository, chunk)
		if err != nil {
			return nil, err
		}
		ids = append(ids, found...)
	}
	return ids, nil
}

// maxParameters bounds the parameters of one IN or VALUES list.
const maxParameters = 500

func chunkFeatures(ctx context.Context, tx *sql.Tx, version int64, repository string, sessions []SessionKey) ([]string, error) {
	args := []any{version, repository}
	values := make([]string, len(sessions))
	for i, key := range sessions {
		values[i] = "(" + placeholders(len(args)+1, 2) + ")"
		args = append(args, key.Agent, key.ID)
	}
	return collect(scanString)(tx.QueryContext(ctx, `WITH linked(agent, session_id) AS (VALUES `+strings.Join(values, ",")+`)
SELECT fp.feature_id FROM linked JOIN session_commit sc ON sc.repository=?2 AND sc.agent=linked.agent AND sc.session_id=linked.session_id
JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=pc.repository AND fp.number=pc.number
UNION SELECT fs.feature_id FROM linked JOIN feature_session fs ON fs.version_id=?1 AND fs.repository=?2
AND fs.agent=linked.agent AND fs.session_id=linked.session_id`, args...))
}

// commitSessions are the sessions linked to the commits shas.
func commitSessions(ctx context.Context, tx *sql.Tx, repository string, shas []string) ([]SessionKey, error) {
	var out []SessionKey
	for chunk := range slices.Chunk(shas, maxParameters) {
		args := []any{repository}
		for _, sha := range chunk {
			args = append(args, sha)
		}
		found, err := collect(scanSession)(tx.QueryContext(ctx, `SELECT DISTINCT agent, session_id FROM session_commit
WHERE repository=?1 AND sha IN (`+placeholders(2, len(chunk))+`)`, args...))
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// placeholders are n numbered parameters from first: "?3,?4".
func placeholders(first, n int) string {
	list := make([]string, n)
	for i := range list {
		list[i] = "?" + strconv.Itoa(first+i)
	}
	return strings.Join(list, ",")
}

// collect scans every row with scan and closes the rows; it takes a query's
// results as they are returned.
func collect[T any](scan func(*sql.Rows) (T, error)) func(*sql.Rows, error) ([]T, error) {
	return func(rows *sql.Rows, err error) ([]T, error) {
		if err != nil {
			return nil, err
		}
		var out []T
		for rows.Next() {
			value, err := scan(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, value)
		}
		return out, errors.Join(rows.Err(), rows.Close())
	}
}

func scanString(rows *sql.Rows) (string, error) {
	var value string
	return value, rows.Scan(&value)
}

func scanInt(rows *sql.Rows) (int, error) {
	var value int
	return value, rows.Scan(&value)
}

func scanSession(rows *sql.Rows) (SessionKey, error) {
	var key SessionKey
	return key, rows.Scan(&key.Agent, &key.ID)
}

// markFeatures records a change to the features' inputs: their membership
// version goes up, and their text falls behind.
func markFeatures(ctx context.Context, tx *sql.Tx, ids []string, now time.Time) error {
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feature SET membership_version=membership_version+1, dirty_since=COALESCE(dirty_since, ?1),
changed_at=?1, updated_at=?1 WHERE id=?2`, now.UnixMilli(), id); err != nil {
			return err
		}
	}
	return nil
}

// settleState is an item's row of feature_settle.
type settleState struct {
	key, outcome string
	failures     int
	next         int64
}

type settleStates map[[2]string]settleState

// waiting reports whether the item waits for its next trigger or its
// backoff: settled for key, given up on for key, or backing off.
func (s settleStates) waiting(kind, item, key string, now time.Time) bool {
	row, ok := s[[2]string{kind, item}]
	return ok && row.key == key && (row.outcome != "" || row.failures >= FeatureMaxFailures || row.next > now.UnixMilli())
}

func (s *Store) settleRows(ctx context.Context, version int64, repository string) (settleStates, error) {
	type row struct {
		kind, item string
		state      settleState
	}
	rows, err := collect(func(rows *sql.Rows) (row, error) {
		var r row
		return r, rows.Scan(&r.kind, &r.item, &r.state.key, &r.state.outcome, &r.state.failures, &r.state.next)
	})(s.db.QueryContext(ctx, `SELECT kind, item, trigger_key, outcome, failures, COALESCE(next_at, 0) FROM feature_settle
WHERE version_id=? AND repository=?`, version, repository))
	out := settleStates{}
	for _, r := range rows {
		out[[2]string{r.kind, r.item}] = r.state
	}
	return out, err
}

// settleItem records an item settled for its trigger key with outcome, or,
// with none, done: its row goes.
func (s *Store) settleItem(ctx context.Context, version int64, repository, kind, item, key, outcome string) error {
	if outcome == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM feature_settle WHERE version_id=? AND repository=? AND kind=? AND item=?`, version, repository, kind, item)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO feature_settle(version_id, repository, kind, item, trigger_key, outcome) VALUES (?,?,?,?,?,?)
ON CONFLICT(version_id, repository, kind, item) DO UPDATE SET trigger_key=excluded.trigger_key, outcome=excluded.outcome, failures=0, next_at=NULL`,
		version, repository, kind, item, key, outcome)
	return err
}

// deferItem backs an item off until until, counting failed calls against
// its trigger key (a new key starts the count again), and returns the count.
func (s *Store) deferItem(ctx context.Context, version int64, repository, kind, item, key string, failed int, until time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `INSERT INTO feature_settle(version_id, repository, kind, item, trigger_key, failures, next_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(version_id, repository, kind, item) DO UPDATE SET
failures=CASE WHEN trigger_key=excluded.trigger_key THEN failures+excluded.failures ELSE excluded.failures END,
trigger_key=excluded.trigger_key, outcome='', next_at=excluded.next_at RETURNING failures`,
		version, repository, kind, item, key, failed, until.UnixMilli()).Scan(&count)
	return count, err
}

// generatedSummary is a pull request's generated summary.
type generatedSummary struct {
	Hash, Summary string
}

// pullSummaries reads the generated summaries of the repository's pull
// requests numbers.
func (s *Store) pullSummaries(ctx context.Context, repository string, numbers []int) (map[int]generatedSummary, error) {
	out := map[int]generatedSummary{}
	if len(numbers) == 0 {
		return out, nil
	}
	args := []any{repository}
	for _, n := range numbers {
		args = append(args, n)
	}
	type row struct {
		number  int
		summary generatedSummary
	}
	rows, err := collect(func(rows *sql.Rows) (row, error) {
		var r row
		return r, rows.Scan(&r.number, &r.summary.Hash, &r.summary.Summary)
	})(s.db.QueryContext(ctx, `SELECT number, content_hash, summary FROM pr_summary WHERE repository=?1 AND number IN (`+
		placeholders(2, len(numbers))+`)`, args...))
	for _, r := range rows {
		out[r.number] = r.summary
	}
	return out, err
}
