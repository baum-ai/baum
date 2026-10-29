package decisionlog

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
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
	// Stopped is the reason no further call was made (a backend or price
	// reason).
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
// backend reason stops it until the next run.
func (w *FeatureWorker) Summarize(ctx context.Context, repository, dir string, o FeatureOptions) (SummaryReport, error) {
	o.DryRun = false
	r, err := w.newRun(ctx, repository, dir, o)
	if err != nil {
		return SummaryReport{Repository: repository, Features: []SummarizedFeature{}}, err
	}
	quiet := r.now.Add(-FeatureQuiet)
	if o.Manual {
		quiet = r.now
	}
	limit := FeatureSummaryRunLimit
	if o.Limit > 0 {
		limit = o.Limit
	}
	return r.summarizePending(ctx, quiet, limit)
}

// summarizePending writes the text of at most limit of the run's version's
// features behind their members and unchanged since quiet,
// FeatureSummaryConcurrency at once. A stop or an error starts no further
// feature; those being written finish.
func (r *featureRun) summarizePending(ctx context.Context, quiet time.Time, limit int) (SummaryReport, error) {
	w, repository := r.w, r.repository
	report := SummaryReport{Repository: repository, Features: []SummarizedFeature{}}
	pending, err := w.Store.pendingSummaries(ctx, r.version, repository, quiet)
	if err != nil {
		return report, err
	}
	settled, err := w.Store.settleRows(ctx, r.version, repository)
	if err != nil {
		return report, err
	}
	pending = slices.DeleteFunc(pending, func(p [2]string) bool { return settled.waiting(kindFeature, p[0], p[1], r.now) })
	pending = pending[:min(len(pending), limit)]
	// written keeps the features in pending's order.
	written := make([]*SummarizedFeature, len(pending))
	var (
		mu     sync.Mutex
		halted bool
		runErr error
		wg     sync.WaitGroup
	)
	// stop records why no further feature starts, the first reason kept.
	stop := func(reason string, err error) {
		mu.Lock()
		defer mu.Unlock()
		halted = true
		report.Stopped = cmp.Or(report.Stopped, reason)
		if runErr == nil {
			runErr = err
		}
	}
	slots := make(chan struct{}, FeatureSummaryConcurrency)
	for i, p := range pending {
		// Checked once a slot is free, so a stop meanwhile applies.
		slots <- struct{}{}
		mu.Lock()
		done := halted
		mu.Unlock()
		if done {
			break
		}
		if ctx.Err() != nil {
			stop("", ctx.Err())
			break
		}
		if retired, err := r.retired(ctx); err != nil {
			stop("", err)
			break
		} else if retired {
			stop(ReasonVersionRetired, nil)
			break
		}
		if on, err := r.refresh(ctx); err != nil {
			stop(ReasonPreferences, err)
			break
		} else if !on {
			stop(ReasonDisabled, nil)
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			out, err := r.summarize(ctx, p[0])
			mu.Lock()
			if out.Cost != nil {
				report.Spent += *out.Cost
			}
			mu.Unlock()
			switch {
			case errors.Is(err, errVersionRetired):
				stop(ReasonVersionRetired, nil)
				return
			case out.Outcome == "":
				stop(out.Reason, err)
				return
			case err != nil:
				stop("", err)
				return
			}
			written[i] = &out
			w.logger().Info("feature text", "repository", repository, "feature", out.Feature, "outcome", out.Outcome,
				"decisions", out.Decisions, "pull_request_summaries", out.PullSummaries)
		})
	}
	wg.Wait()
	for _, out := range written {
		if out != nil {
			report.Features = append(report.Features, *out)
		}
	}
	return report, runErr
}

// pendingSummaries are the version's features whose text is behind their
// members, unchanged since quiet, longest behind first, each with its
// membership version.
func (s *Store) pendingSummaries(ctx context.Context, version int64, repository string, quiet time.Time) ([][2]string, error) {
	return collect(func(rows *sql.Rows) ([2]string, error) {
		var id string
		var membership int64
		err := rows.Scan(&id, &membership)
		return [2]string{id, strconv.FormatInt(membership, 10)}, err
	})(s.db.QueryContext(ctx, `SELECT id, membership_version FROM feature WHERE version_id=? AND repository=? AND merged_into IS NULL
AND summarized_version < membership_version AND changed_at <= ? ORDER BY COALESCE(dirty_since, changed_at), id`, version, repository, quiet.UnixMilli()))
}

// featureText is a feature's stored text.
type featureText struct {
	Title, Description, Digest string
	// KeyDecisions are the decisions the text names, each by ID and address.
	KeyDecisions []keyDecision
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
func (s *Store) summarySource(ctx context.Context, version int64, repository, featureID string) (summarySource, error) {
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
	type pullRow struct {
		pull              summaryPull
		current, assigned string
	}
	pulls, err := collect(func(rows *sql.Rows) (pullRow, error) {
		var p pullRow
		return p, rows.Scan(&p.pull.Number, &p.pull.Title, &p.pull.Body, &p.pull.State, &p.current, &p.assigned)
	})(s.db.QueryContext(ctx, `SELECT p.number, p.title, p.body, CASE WHEN p.merged_at IS NOT NULL THEN 'merged' ELSE p.state END,
COALESCE(p.paths_json, '[]'), fp.assigned_paths_json FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.feature_id=? ORDER BY p.created_at, p.number`, featureID))
	if err != nil {
		return src, err
	}
	for _, p := range pulls {
		for _, list := range []string{p.current, p.assigned} {
			var held []string
			if err := json.Unmarshal([]byte(list), &held); err != nil {
				return src, err
			}
			for _, path := range held {
				paths[path] = true
			}
		}
		src.Pulls = append(src.Pulls, p.pull)
	}
	type linkRow struct {
		number int
		key    SessionKey
	}
	links, err := collect(func(rows *sql.Rows) (linkRow, error) {
		var l linkRow
		return l, rows.Scan(&l.number, &l.key.Agent, &l.key.ID)
	})(s.db.QueryContext(ctx, `SELECT DISTINCT pc.number, sc.agent, sc.session_id FROM feature_pull fp
JOIN pull_commit pc ON pc.repository=fp.repository AND pc.number=fp.number
JOIN session_commit sc ON sc.repository=pc.repository AND sc.sha=pc.sha
JOIN capture_session cs ON cs.agent=sc.agent AND cs.session_id=sc.session_id
WHERE fp.feature_id=?1
UNION SELECT 0, fs.agent, fs.session_id FROM feature_session fs JOIN capture_session cs ON cs.agent=fs.agent AND cs.session_id=fs.session_id
WHERE fs.feature_id=?1 ORDER BY 2, 3, 1`, featureID))
	if err != nil {
		return src, err
	}
	var sessions []SessionKey
	for _, l := range links {
		if !slices.Contains(sessions, l.key) {
			sessions = append(sessions, l.key)
		}
		for i := range src.Pulls {
			if src.Pulls[i].Number == l.number {
				src.Pulls[i].Sessions = append(src.Pulls[i].Sessions, l.key)
			}
		}
	}
	for _, key := range sessions {
		links, err := s.sessionLinks(ctx, version, repository, key.Agent, key.ID)
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
// since version was read, and reports whether it did; errVersionRetired
// when a build's switch retired its version. Complete text makes
// the feature clean; incomplete text (written without a pull request's
// summary) leaves it behind.
func (s *Store) storeSummary(ctx context.Context, featureID string, version int64, text featureText, complete bool) (bool, error) {
	if text.KeyDecisions == nil {
		text.KeyDecisions = []keyDecision{}
	}
	keys, err := json.Marshal(text.KeyDecisions)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE feature SET title=?1, description=?2, digest=?3, key_decisions_json=?4, summary_input_hash=?5,
summarized_version=CASE WHEN ?6 THEN ?7 ELSE summarized_version END, dirty_since=CASE WHEN ?6 THEN NULL ELSE dirty_since END
WHERE id=?8 AND membership_version=?7 AND version_id IN (SELECT id FROM feature_version WHERE state != 'retired')`, text.Title, text.Description, text.Digest, string(keys), text.InputHash, complete, version, featureID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 1 {
		return n == 1, err
	}
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT v.state FROM feature f JOIN feature_version v ON v.id=f.version_id WHERE f.id=?`, featureID).
		Scan(&state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if state == VersionRetired {
		return false, errVersionRetired
	}
	return false, nil
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
	src, err := s.summarySource(ctx, r.version, r.repository, featureID)
	if errors.Is(err, sql.ErrNoRows) {
		out.Outcome, out.Reason = FeatureSkipped, "gone"
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
	numbers := make([]int, len(src.Pulls))
	for i, pull := range src.Pulls {
		numbers[i] = pull.Number
	}
	generated, err := s.pullSummaries(ctx, r.repository, numbers)
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
				return out, err
			case err != nil && ctx.Err() != nil:
				return out, ctx.Err()
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
		// wording. Key decisions extracted again under new IDs take them.
		kept := src.Text
		kept.KeyDecisions, _ = currentKeys(src.Text.KeyDecisions, src.Decisions)
		written, err := s.storeSummary(write, featureID, src.Version, kept, true)
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
	result, callErr := r.calls.complete(ctx, r.opts.Backend, r.opts.Model,
		Attempt{Repository: r.repository, Stage: StageSummary, Item: kindFeature + ":" + featureID}, request)
	out.Cost = addCost(out.Cost, result.Cost)
	switch {
	case result.Skip != "":
		out.Reason = result.Skip
		return out, callErr
	case callErr != nil && ctx.Err() != nil:
		// Cut short by the daemon stopping: not the feature's failure.
		return out, ctx.Err()
	case callErr != nil:
		return r.featureFailed(write, featureID, key, out, map[bool]int{true: 1}[result.Called]+failed, callErr)
	}
	text, err := summary.ParseFeatureReply(result.JSON, rendered)
	if err != nil {
		// Paid for, and never written.
		return r.featureFailed(write, featureID, key, out, 1+failed, err)
	}
	stored := featureText{Title: text.Title, Description: text.Description, Digest: text.Digest}
	keyed := map[decisionAddress]bool{}
	for _, index := range text.KeyDecisions {
		// One key decision per address, the first named.
		if address := addressOf(src.Decisions[index]); !keyed[address] {
			keyed[address] = true
			stored.KeyDecisions = append(stored.KeyDecisions, keyDecision{ID: src.Decisions[index].ID, decisionAddress: address,
				Ordinal: ordinalAt(src.Decisions, index)})
		}
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
		return r.featureFailed(write, featureID, key, out, 1+failed, pullErr)
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
	result, callErr := r.calls.complete(ctx, r.opts.Backend, r.opts.Model,
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

// featureFailed backs a feature off (backOff), counting failed calls
// against its membership version.
func (r *featureRun) featureFailed(ctx context.Context, featureID, key string, out SummarizedFeature, failed int, cause error) (SummarizedFeature, error) {
	var err error
	out.Error = cause.Error()
	out.Outcome, err = r.backOff(ctx, kindFeature, featureID, key, failed, cause)
	return out, err
}

// decisionAddress names a decision in the text hashes as hosted does: by its
// session and first cited turn, so a re-extraction holding the same
// decisions under new IDs keeps the text.
type decisionAddress struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
	Turn    int    `json:"turn"`
}

func addressOf(d Decision) decisionAddress {
	turn := 0
	if len(d.Turns) > 0 {
		turn = d.Turns[0]
	}
	return decisionAddress{Agent: d.Agent, Session: d.SessionID, Turn: turn}
}

// featureInputHash identifies a feature's rendered input under the prompt
// version together with the decisions it renders, in order, by address.
func featureInputHash(rendered summary.RenderedFeature, decisions []Decision) string {
	addresses := make([]decisionAddress, 0, len(rendered.Rendered))
	for _, index := range rendered.Rendered {
		addresses = append(addresses, addressOf(decisions[index]))
	}
	encoded, _ := json.Marshal(addresses)
	return summary.ContentHash(SummaryVersion, rendered.Input+"\n"+string(encoded))
}

// featureTextHash binds an input hash to the text written from it, its key
// decisions by address, so text stored any other way never matches; "" when
// a key decision is none of decisions, so such text is never reused.
func featureTextHash(inputHash string, text featureText, decisions []Decision) string {
	current, ok := currentKeys(text.KeyDecisions, decisions)
	if !ok {
		return ""
	}
	type hashed struct {
		decisionAddress
		Ordinal int `json:"ordinal"`
	}
	keys := []hashed{}
	for _, key := range current {
		keys = append(keys, hashed{key.decisionAddress, key.Ordinal})
	}
	encoded, _ := json.Marshal(struct {
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Digest       string   `json:"digest"`
		KeyDecisions []hashed `json:"key_decisions"`
	}{text.Title, text.Description, text.Digest, keys})
	return summary.ContentHash(inputHash, string(encoded))
}

// keyDecision is a key decision as stored: its ID, its address and its
// place among the decisions at that address (Ordinal).
type keyDecision struct {
	ID string `json:"id"`
	decisionAddress
	Ordinal int `json:"ordinal"`
}

// UnmarshalJSON also reads a bare ID, an earlier form with no address.
func (k *keyDecision) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		*k = keyDecision{}
		return json.Unmarshal(data, &k.ID)
	}
	type plain keyDecision
	return json.Unmarshal(data, (*plain)(k))
}

// ordinalAt is decision index's place among the decisions at its address.
func ordinalAt(decisions []Decision, index int) int {
	n := 0
	for _, d := range decisions[:index] {
		if addressOf(d) == addressOf(decisions[index]) {
			n++
		}
	}
	return n
}

// currentKeys maps key decisions to current decisions: the one with the
// stored ID while it is current, else, at its address, the one at the same
// place, else the first not superseded, else the first;
// false when one has none.
func currentKeys(keys []keyDecision, decisions []Decision) ([]keyDecision, bool) {
	out := make([]keyDecision, 0, len(keys))
	for _, key := range keys {
		i := slices.IndexFunc(decisions, func(d Decision) bool { return d.ID == key.ID })
		if i < 0 {
			var at []int
			for j, d := range decisions {
				if key.Session != "" && addressOf(d) == key.decisionAddress {
					at = append(at, j)
				}
			}
			switch {
			case len(at) == 0:
				return nil, false
			case key.Ordinal < len(at):
				// The matching hash shows it is the same decision.
				i = at[key.Ordinal]
			default:
				i = at[0]
				for _, j := range at {
					if !decisions[j].Superseded {
						i = j
						break
					}
				}
			}
		}
		out = append(out, keyDecision{ID: decisions[i].ID, decisionAddress: addressOf(decisions[i]), Ordinal: ordinalAt(decisions, i)})
	}
	return out, true
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
