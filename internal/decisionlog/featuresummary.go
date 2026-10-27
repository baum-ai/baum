package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/baum-ai/baum/features/summary"
)

// Feature text (README.md, "Feature text"): which features, their input,
// generated pull request summaries, reuse and the write.

// Outcomes of a feature's text besides those of placement.
const (
	FeatureSummarized = "summarized"
	FeatureReused     = "reused"
	FeatureStale      = "stale"
	FeatureEmpty      = "empty"
)

// SummaryReport says what Summarize did.
type SummaryReport struct {
	Repository string              `json:"repository"`
	Features   []SummarizedFeature `json:"features"`
	// Stopped is the reason no further call was made (a backend, price or
	// cap reason).
	Stopped string  `json:"stopped,omitempty"`
	Spent   float64 `json:"spent_usd"`
}

// SummarizedFeature is one feature Summarize took.
type SummarizedFeature struct {
	Feature string `json:"feature"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	// PullRequests are its pull requests' numbers, earliest opened first.
	PullRequests []int `json:"pull_requests"`
	// PullSummaries counts the generated pull request summaries made by a
	// call, PullSummariesReused those kept.
	PullSummaries       int      `json:"pull_request_summaries"`
	PullSummariesReused int      `json:"pull_request_summaries_reused"`
	Decisions           int      `json:"decisions"`
	KeyDecisions        int      `json:"key_decisions"`
	InputBytes          int      `json:"input_bytes"`
	Cost                *float64 `json:"cost_usd,omitempty"`
	Error               string   `json:"error,omitempty"`
}

// Summarize writes the text of the repository's features whose members
// changed and have been unchanged for FeatureQuiet (not with o.Manual),
// longest behind first, at most FeatureSummaryRunLimit (or o.Limit). A
// backend, price or cap reason stops it until the next run.
func (w *FeatureWorker) Summarize(ctx context.Context, repository, dir string, o FeatureOptions) (SummaryReport, error) {
	report := SummaryReport{Repository: repository, Features: []SummarizedFeature{}}
	o.DryRun = false
	r, err := w.newRun(ctx, repository, dir, o)
	if err != nil {
		return report, err
	}
	quiet := r.now.Add(-FeatureQuiet)
	if o.Manual {
		quiet = r.now
	}
	pending, err := w.Store.pendingSummaries(ctx, r.version, repository, quiet)
	if err != nil {
		return report, err
	}
	settled, err := w.Store.settleRows(ctx, r.version, repository)
	if err != nil {
		return report, err
	}
	pending = slices.DeleteFunc(pending, func(p [2]string) bool { return settled.waiting(kindFeature, p[0], p[1], r.now) })
	limit := FeatureSummaryRunLimit
	if o.Limit > 0 {
		limit = o.Limit
	}
	for _, p := range pending[:min(len(pending), limit)] {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		out, err := r.summarize(ctx, p[0])
		if out.Cost != nil {
			report.Spent += *out.Cost
		}
		if err != nil {
			return report, err
		}
		if out.Outcome == "" {
			report.Stopped = out.Reason
			break
		}
		report.Features = append(report.Features, out)
		w.logger().Info("feature text", "repository", repository, "feature", out.Feature, "outcome", out.Outcome,
			"decisions", out.Decisions, "pull_request_summaries", out.PullSummaries)
	}
	return report, nil
}

// pendingSummaries are the version's features whose text is behind their
// members, unchanged since quiet, longest behind first, each with its
// membership version.
func (s *Store) pendingSummaries(ctx context.Context, version int64, repository string, quiet time.Time) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, membership_version FROM feature WHERE version_id=? AND repository=? AND merged_into IS NULL
AND summarized_version < membership_version AND changed_at <= ? ORDER BY COALESCE(dirty_since, changed_at), id`, version, repository, quiet.UnixMilli())
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for rows.Next() {
		var id string
		var membership int64
		if err := rows.Scan(&id, &membership); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, [2]string{id, strconv.FormatInt(membership, 10)})
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// featureText is a feature's stored text.
type featureText struct {
	Title, Description, Digest string
	// KeyDecisions are decision IDs.
	KeyDecisions []string
	// InputHash is featureTextHash's, "" when the text may not be reused.
	InputHash string
}

// summaryPull is one of a feature's pull requests, as its text reads it.
type summaryPull struct {
	Number             int
	Title, Body, State string
	// Sessions are the captured sessions linked to its commits.
	Sessions []SessionKey
}

// summarySource is everything a feature's text is written from, read at
// its membership version.
type summarySource struct {
	Version int64
	Text    featureText
	// Pulls are earliest opened first.
	Pulls []summaryPull
	// Decisions are the current decisions attributed to the feature,
	// superseded ones included, earliest first.
	Decisions []Decision
	Paths     []string
}

// summarySource reads a feature's text input: its pull requests, the
// current decisions of its sessions (those linked to its pull requests'
// commits and those in it on their own) that features.Attributor gives it,
// never a session's prompts, and its paths (its pull requests' paths, those
// they were placed with, and those its decisions cite).
func (s *Store) summarySource(ctx context.Context, repository, featureID string) (summarySource, error) {
	var src summarySource
	var keys string
	if err := s.db.QueryRowContext(ctx, `SELECT membership_version, title, description, digest, key_decisions_json, summary_input_hash
FROM feature WHERE id=?`, featureID).Scan(&src.Version, &src.Text.Title, &src.Text.Description, &src.Text.Digest, &keys, &src.Text.InputHash); err != nil {
		return src, err
	}
	if err := json.Unmarshal([]byte(keys), &src.Text.KeyDecisions); err != nil {
		return src, err
	}
	paths := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT p.number, p.title, p.body, CASE WHEN p.merged_at IS NOT NULL THEN 'merged' ELSE p.state END,
COALESCE(p.paths_json, '[]'), fp.assigned_paths_json FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.feature_id=? ORDER BY p.created_at, p.number`, featureID)
	if err != nil {
		return src, err
	}
	for rows.Next() {
		var pull summaryPull
		var current, assigned string
		if err := rows.Scan(&pull.Number, &pull.Title, &pull.Body, &pull.State, &current, &assigned); err != nil {
			_ = rows.Close()
			return src, err
		}
		for _, list := range []string{current, assigned} {
			var held []string
			if err := json.Unmarshal([]byte(list), &held); err != nil {
				_ = rows.Close()
				return src, err
			}
			for _, path := range held {
				paths[path] = true
			}
		}
		src.Pulls = append(src.Pulls, pull)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return src, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT pc.number, sc.agent, sc.session_id FROM feature_pull fp
JOIN pull_commit pc ON pc.repository=fp.repository AND pc.number=fp.number
JOIN session_commit sc ON sc.repository=pc.repository AND sc.sha=pc.sha
JOIN capture_session cs ON cs.agent=sc.agent AND cs.session_id=sc.session_id
WHERE fp.feature_id=?1
UNION SELECT 0, fs.agent, fs.session_id FROM feature_session fs JOIN capture_session cs ON cs.agent=fs.agent AND cs.session_id=fs.session_id
WHERE fs.feature_id=?1 ORDER BY 2, 3, 1`, featureID)
	if err != nil {
		return src, err
	}
	var sessions []SessionKey
	for rows.Next() {
		var number int
		var key SessionKey
		if err := rows.Scan(&number, &key.Agent, &key.ID); err != nil {
			_ = rows.Close()
			return src, err
		}
		if !slices.Contains(sessions, key) {
			sessions = append(sessions, key)
		}
		for i := range src.Pulls {
			if src.Pulls[i].Number == number {
				src.Pulls[i].Sessions = append(src.Pulls[i].Sessions, key)
			}
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return src, err
	}
	for _, key := range sessions {
		links, err := s.SessionLinks(ctx, repository, key.Agent, key.ID)
		if err != nil {
			return src, err
		}
		attributor := links.Attributor()
		current, err := s.Current(ctx, key)
		if err != nil {
			return src, err
		}
		for _, d := range current {
			if attributor.Attribute(d.Files) == featureID {
				src.Decisions = append(src.Decisions, d)
				for _, file := range d.Files {
					paths[file] = true
				}
			}
		}
	}
	slices.SortStableFunc(src.Decisions, func(a, b Decision) int { return a.DecidedAt.Compare(b.DecidedAt) })
	for path := range paths {
		src.Paths = append(src.Paths, path)
	}
	slices.Sort(src.Paths)
	return src, nil
}

// storeSummary writes a feature's text when its members did not change
// since version was read, and reports whether it did. Complete text makes
// the feature clean; incomplete text (written without a pull request's
// summary) leaves it behind.
func (s *Store) storeSummary(ctx context.Context, featureID string, version int64, text featureText, complete bool) (bool, error) {
	if text.KeyDecisions == nil {
		text.KeyDecisions = []string{}
	}
	keys, err := json.Marshal(text.KeyDecisions)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE feature SET title=?1, description=?2, digest=?3, key_decisions_json=?4, summary_input_hash=?5,
summarized_version=CASE WHEN ?6 THEN ?7 ELSE summarized_version END, dirty_since=CASE WHEN ?6 THEN NULL ELSE dirty_since END
WHERE id=?8 AND membership_version=?7`, text.Title, text.Description, text.Digest, string(keys), text.InputHash, complete, version, featureID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// storePullSummary keeps a pull request's generated summary with the hash
// of its input.
func (s *Store) storePullSummary(ctx context.Context, repository string, number int, hash, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pr_summary(repository, number, content_hash, summary, created_at) VALUES (?,?,?,?,?)
ON CONFLICT(repository, number) DO UPDATE SET content_hash=excluded.content_hash, summary=excluded.summary, created_at=excluded.created_at`,
		repository, number, hash, text, s.now().UnixMilli())
	return err
}

// summarize writes one feature's text and settles it. A feature with no
// outcome was not called for the reason it gives, and stops the run; an
// error is the store's.
func (r *featureRun) summarize(ctx context.Context, featureID string) (SummarizedFeature, error) {
	s := r.w.Store
	out := SummarizedFeature{Feature: featureID, PullRequests: []int{}}
	src, err := s.summarySource(ctx, r.repository, featureID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	key := strconv.FormatInt(src.Version, 10)
	out.Title = src.Text.Title
	write := context.WithoutCancel(ctx)
	if len(src.Pulls) == 0 && len(src.Decisions) == 0 {
		// Nothing to write from: the text stays, without key decisions or
		// an input to reuse it for.
		kept := src.Text
		kept.KeyDecisions, kept.InputHash = nil, ""
		if _, err := s.storeSummary(write, featureID, src.Version, kept, true); err != nil {
			return out, err
		}
		out.Outcome = FeatureEmpty
		return out, s.settleItem(write, r.version, r.repository, kindFeature, featureID, key, "")
	}
	generated, err := s.pullSummaries(ctx, r.repository)
	if err != nil {
		return out, err
	}
	input := summary.FeatureInput{Paths: src.Paths}
	var pullErr error
	failed := 0
	for _, pull := range src.Pulls {
		out.PullRequests = append(out.PullRequests, pull.Number)
		info := summary.PullRequestInfo{Number: int32(pull.Number), State: pull.State, Title: pull.Title, Body: pull.Body,
			Summary: r.reviewSummary(ctx, pull.Number)}
		if info.Summary == "" {
			text, called, skip, err := r.pullSummary(ctx, src, pull, generated[pull.Number], &out)
			switch {
			case skip != "":
				out.Reason = skip
				return out, nil
			case err != nil:
				// The text is written without it and stays behind; a call
				// that may have been billed counts.
				pullErr = errors.Join(pullErr, err)
				failed += map[bool]int{true: 1}[called]
			case called:
				out.PullSummaries++
			case text != "":
				out.PullSummariesReused++
			}
			info.Summary = text
		}
		input.PullRequests = append(input.PullRequests, info)
	}
	for _, d := range src.Decisions {
		input.Decisions = append(input.Decisions, summary.Decision{
			Summary: d.Summary, Category: string(d.Category), Who: string(d.Who), Trigger: string(d.Trigger), Rationale: d.Rationale,
			RejectedAlternative: d.RejectedAlternative, Superseded: d.Superseded, Files: d.Files,
		})
	}
	// Room for the current text and the repository, added after hashing.
	rendered, err := summary.RenderFeature(input, summary.DefaultMaxInputBytes-summary.MaxCurrentTextBytes)
	if err != nil {
		return out, err
	}
	inputHash := featureInputHash(rendered, src.Decisions)
	out.Decisions, out.InputBytes = len(rendered.Rendered), len(rendered.Input)
	if hash := featureTextHash(inputHash, src.Text, src.Decisions); pullErr == nil && hash != "" && hash == src.Text.InputHash && src.Text.Title != "" {
		// Written from this input already: a call would differ only in its
		// wording.
		written, err := s.storeSummary(write, featureID, src.Version, src.Text, true)
		if err != nil {
			return out, err
		}
		out.Outcome, out.KeyDecisions = FeatureReused, len(src.Text.KeyDecisions)
		if !written {
			out.Outcome = FeatureStale
		}
		return out, s.settleItem(write, r.version, r.repository, kindFeature, featureID, key, "")
	}
	callInput, err := summary.WithRepository(rendered.Input, r.repository)
	if err == nil && src.Text.Title != "" {
		callInput, err = summary.WithCurrent(callInput, src.Text.Title, src.Text.Description)
	}
	if err != nil {
		return out, err
	}
	request, err := summary.Public.Request(SummaryVersion, summary.Feature, callInput, summary.MaxOutputTokens)
	if err != nil {
		return out, err
	}
	result, callErr := r.calls.complete(ctx, r.prefs, r.settings, r.opts.Backend, r.opts.Model,
		Attempt{Repository: r.repository, Stage: StageSummary, Item: kindFeature + ":" + featureID}, request)
	out.Cost = addCost(out.Cost, result.Cost)
	switch {
	case result.Skip != "":
		out.Reason = result.Skip
		return out, callErr
	case callErr != nil:
		return r.deferFeature(write, featureID, key, out, map[bool]int{true: 1}[result.Called]+failed, callErr)
	}
	text, err := summary.ParseFeatureReply(result.JSON, rendered)
	if err != nil {
		// Paid for, and never written.
		return r.deferFeature(write, featureID, key, out, 1+failed, err)
	}
	stored := featureText{Title: text.Title, Description: text.Description, Digest: text.Digest}
	for _, index := range text.KeyDecisions {
		stored.KeyDecisions = append(stored.KeyDecisions, src.Decisions[index].ID)
	}
	if pullErr == nil {
		stored.InputHash = featureTextHash(inputHash, stored, src.Decisions)
	}
	out.Title, out.KeyDecisions = stored.Title, len(stored.KeyDecisions)
	written, err := s.storeSummary(write, featureID, src.Version, stored, pullErr == nil)
	if err != nil {
		return out, err
	}
	if pullErr != nil {
		// Incomplete: retried after the backoff, the feature call counting
		// too.
		return r.deferFeature(write, featureID, key, out, 1+failed, pullErr)
	}
	out.Outcome = FeatureSummarized
	if !written {
		// Its members changed meanwhile: it stays behind.
		out.Outcome = FeatureStale
	}
	return out, s.settleItem(write, r.version, r.repository, kindFeature, featureID, key, "")
}

// pullSummary is the summary generated for a pull request without a saved
// review summary, from its title, description and the current decisions of
// the feature of the sessions linked to it: the stored one while that input
// is unchanged, else one call's. One with neither a description nor such a
// decision gets none. It reports whether it called, or why it could not.
func (r *featureRun) pullSummary(ctx context.Context, src summarySource, pull summaryPull, stored generatedSummary, out *SummarizedFeature) (string, bool, string, error) {
	input := summary.PullRequestInput{Title: pull.Title, Body: pull.Body}
	for _, d := range src.Decisions {
		if !d.Superseded && slices.Contains(pull.Sessions, SessionKey{Agent: d.Agent, ID: d.SessionID}) {
			input.Decisions = append(input.Decisions, summary.Decision{
				Summary: d.Summary, Category: string(d.Category), Who: string(d.Who), Trigger: string(d.Trigger), Rationale: d.Rationale,
				RejectedAlternative: d.RejectedAlternative, Files: d.Files,
			})
		}
	}
	if len(input.Decisions) == 0 && pull.Body == "" {
		return "", false, "", nil
	}
	rendered, _, err := summary.RenderPullRequest(input)
	if err != nil {
		return "", false, "", err
	}
	hash := summary.ContentHash(summary.Public.PullRequestVersion(SummaryVersion), rendered)
	if hash == stored.Hash && stored.Summary != "" {
		return stored.Summary, false, "", nil
	}
	request, err := summary.Public.Request(SummaryVersion, summary.PullRequest, rendered, summary.MaxOutputTokens)
	if err != nil {
		return "", false, "", err
	}
	result, callErr := r.calls.complete(ctx, r.prefs, r.settings, r.opts.Backend, r.opts.Model,
		Attempt{Repository: r.repository, Stage: StagePullSummary, Item: kindPull + ":" + strconv.Itoa(pull.Number)}, request)
	out.Cost = addCost(out.Cost, result.Cost)
	if result.Skip != "" || callErr != nil {
		return "", result.Called, result.Skip, callErr
	}
	text, err := summary.ParsePullRequestReply(result.JSON)
	if err != nil {
		return "", true, "", err
	}
	return text, true, "", r.w.Store.storePullSummary(context.WithoutCancel(ctx), r.repository, pull.Number, hash, text)
}

// deferFeature backs a feature off after a failure, counting failed calls
// against its membership version.
func (r *featureRun) deferFeature(ctx context.Context, featureID, key string, out SummarizedFeature, failed int, cause error) (SummarizedFeature, error) {
	count, err := r.w.Store.deferItem(ctx, r.version, r.repository, kindFeature, featureID, key, failed, r.now.Add(FeatureBackoff))
	out.Outcome, out.Error = FeatureDeferred, cause.Error()
	if count >= FeatureMaxFailures {
		out.Outcome = FeatureGivenUp
	}
	r.w.logger().Warn("feature log: text deferred", "repository", r.repository, "feature", featureID, "error", cause)
	return out, err
}

// featureInputHash identifies a feature's rendered input under the prompt
// version together with the decisions it renders, in order.
func featureInputHash(rendered summary.RenderedFeature, decisions []Decision) string {
	ids := make([]string, 0, len(rendered.Rendered))
	for _, index := range rendered.Rendered {
		ids = append(ids, decisions[index].ID)
	}
	encoded, _ := json.Marshal(ids)
	return summary.ContentHash(SummaryVersion, rendered.Input+"\n"+string(encoded))
}

// featureTextHash binds an input hash to the text written from it, so text
// stored any other way never matches; "" when a key decision is none of
// decisions, so such text is never reused.
func featureTextHash(inputHash string, text featureText, decisions []Decision) string {
	for _, id := range text.KeyDecisions {
		if !slices.ContainsFunc(decisions, func(d Decision) bool { return d.ID == id }) {
			return ""
		}
	}
	keys := text.KeyDecisions
	if keys == nil {
		keys = []string{}
	}
	encoded, _ := json.Marshal(struct {
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Digest       string   `json:"digest"`
		KeyDecisions []string `json:"key_decisions"`
	}{text.Title, text.Description, text.Digest, keys})
	return summary.ContentHash(inputHash, string(encoded))
}

// addCost adds a call's cost, when known, to a total.
func addCost(total, cost *float64) *float64 {
	switch {
	case cost == nil:
		return total
	case total == nil:
		return new(*cost)
	}
	return new(*total + *cost)
}
