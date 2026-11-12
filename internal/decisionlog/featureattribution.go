package decisionlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features"
)

const attributionVersion = "feature-attribution-repository-v1"
const StageAttribution = "feature_attribution"
const attributionSchema = `CREATE TABLE IF NOT EXISTS feature_decision(
 version_id INTEGER NOT NULL,repository TEXT NOT NULL,decision_id TEXT NOT NULL,input_hash TEXT NOT NULL,
 context_version TEXT NOT NULL,feature_id TEXT NOT NULL,pull_number INTEGER NOT NULL,key_eligible INTEGER NOT NULL,
 reason TEXT NOT NULL,source_json TEXT NOT NULL,PRIMARY KEY(version_id,repository,decision_id));
 CREATE INDEX IF NOT EXISTS feature_decision_feature ON feature_decision(version_id,repository,feature_id);`

const attributionPrompt = `Classify each supplied captured decision against the eligible feature candidates using its meaning, explicit human/agent choice, quoted evidence, cited turns and linked commit/PR evidence. Read the pinned repository source where relevant. Feature scopes describe purpose and exclusions at their recorded map revision. Commit links and shared files are supporting evidence, not proof that unrelated choices share an outcome.
Return each supplied decision_id exactly once. Choose only a supplied feature_id, or empty feature_id with a reason when irrelevant, ambiguous or unsupported. In particular, a Settings choice does not belong to packaging/AppImage merely because that feature dominates a shared session. A unique purpose may support an assignment even without a cited file; equal file overlap between independent outcomes does not decide it. Never guess the dominant, earliest or most recent feature. pull_number is 0 unless a supplied PR of the chosen feature is supported by the decision's evidence.
key_eligible is true only for a consequential choice directly relevant to that feature: product behavior, interface, architecture, data model, a substantive implementation tradeoff or deliberate repository policy. Preserve explicit human choices. Routine agent housekeeping, publishing/running tests, lint/build checks, preparing PRs, bookkeeping and completion reports are not highlights. Distinguish performing tests from deliberately choosing a new testing policy. Unresolved or superseded decisions are never key eligible. Zero eligible decisions is valid. Explain both relevance and significance in reason. Do not change decision IDs or citations.`

type decisionResolution struct {
	DecisionID  string `json:"decision_id"`
	FeatureID   string `json:"feature_id"`
	Pull        int    `json:"pull_number"`
	KeyEligible bool   `json:"key_eligible"`
	Reason      string `json:"reason"`
	Hash        string `json:"-"`
}
type attributionPull struct {
	Number            int
	Title, Body, Head string
	Commits, Paths    []string
}
type attributionCandidate struct {
	ID, Title, Description string
	Scopes                 []FeatureScope
	Pulls                  []attributionPull
}
type attributionInput struct {
	Version         string                    `json:"context_version"`
	Repository      string                    `json:"repository"`
	Source          *SourceContext            `json:"source"`
	DecisionSources map[string]*SourceContext `json:"decision_sources"`
	Candidates      []attributionCandidate    `json:"candidates"`
	Decisions       []Decision                `json:"decisions"`
}
type attributionSet struct {
	Inputs   map[SessionKey]attributionInput
	Resolved map[string]decisionResolution
}

func attributionHash(input attributionInput, d Decision) string {
	input.Decisions = []Decision{d}
	input.DecisionSources = map[string]*SourceContext{d.ID: input.DecisionSources[d.ID]}
	input.Candidates = slices.Clone(input.Candidates)
	for i := range input.Candidates {
		if len(input.Candidates[i].Scopes) == 0 {
			input.Candidates[i].Title = ""
			input.Candidates[i].Description = ""
		}
	}
	data, _ := json.Marshal(input)
	return hash(string(data))
}

// attributionSet reads all resolution inputs together. It is pure storage work;
// the Timeline and summary input reader never call an agent or inspect a clone.
func (s *Store) attributionSet(ctx context.Context, version int64, repository string, keys []SessionKey, current map[SessionKey][]Decision, links map[SessionKey]features.SessionLinks) (attributionSet, error) {
	out := attributionSet{Inputs: map[SessionKey]attributionInput{}, Resolved: map[string]decisionResolution{}}
	if len(keys) == 0 {
		return out, nil
	}
	candidates, err := s.candidateFeatures(ctx, version, repository, time.Time{}, "")
	if err != nil {
		return out, err
	}
	byID := map[string]*candidateFeature{}
	for _, c := range candidates {
		byID[c.ID] = c
	}
	type pullMeta struct{ title, body, head string }
	pulls := map[int]pullMeta{}
	rows, err := s.db.QueryContext(ctx, `SELECT p.number,p.title,p.body,p.head_sha FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number WHERE fp.version_id=? AND fp.repository=?`, version, repository)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n int
		var p pullMeta
		if err = rows.Scan(&n, &p.title, &p.body, &p.head); err != nil {
			_ = rows.Close()
			return out, err
		}
		pulls[n] = p
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	sources := map[SessionKey]*SourceContext{}
	var exists bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='capture_source')`).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		set, err := sessionSet(keys)
		if err != nil {
			return out, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT agent,session_id,source_json FROM capture_source WHERE (agent,session_id)`+inSessions, set)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var key SessionKey
			var data string
			var source SourceContext
			if err = rows.Scan(&key.Agent, &key.ID, &data); err == nil {
				err = json.Unmarshal([]byte(data), &source)
			}
			if err != nil {
				_ = rows.Close()
				return out, err
			}
			sources[key] = &source
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return out, err
		}
	}
	decisionSources := map[string]*SourceContext{}
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='decision_source')`).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		set, err := sessionSet(keys)
		if err != nil {
			return out, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT ds.decision_id,ds.source_json FROM decision_source ds JOIN current_decision d ON d.decision_id=ds.decision_id WHERE (d.agent,d.session_id)`+inSessions, set)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id, data string
			var source SourceContext
			if err = rows.Scan(&id, &data); err == nil {
				err = json.Unmarshal([]byte(data), &source)
			}
			if err != nil {
				_ = rows.Close()
				return out, err
			}
			decisionSources[id] = &source
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return out, err
		}
	}
	for _, key := range keys {
		ownSources := map[string]*SourceContext{}
		for _, d := range current[key] {
			if source := decisionSources[d.ID]; source != nil {
				ownSources[d.ID] = source
			}
		}
		input := attributionInput{DecisionSources: ownSources, Version: attributionVersion, Repository: repository, Source: sources[key], Decisions: current[key]}
		eligible := map[string]bool{}
		for _, p := range links[key].PullRequests {
			eligible[p.FeatureID] = true
		}
		if len(eligible) == 0 && links[key].OwnFeature != "" {
			eligible[links[key].OwnFeature] = true
		}
		for _, id := range mapKeys(eligible) {
			c := byID[id]
			if c == nil {
				continue
			}
			candidate := attributionCandidate{ID: id, Scopes: c.Scopes, Title: c.Title, Description: c.Description}
			// Summarized feature text is output of attribution, not a cache input.
			// Durable scopes and PR text establish purpose without a feedback loop.
			// Legacy display text is shown for context but excluded from the hash.
			if len(c.Scopes) > 0 {
				candidate.Title = c.Scopes[0].Title
				candidate.Description = c.Scopes[0].Purpose
			}
			for _, p := range links[key].PullRequests {
				if p.FeatureID == id {
					meta := pulls[int(p.Number)]
					candidate.Pulls = append(candidate.Pulls, attributionPull{Number: int(p.Number), Title: meta.title, Body: meta.body, Head: meta.head, Commits: p.Commits, Paths: p.Paths})
				}
			}
			slices.SortFunc(candidate.Pulls, func(a, b attributionPull) int { return a.Number - b.Number })
			input.Candidates = append(input.Candidates, candidate)
		}
		out.Inputs[key] = input
		for _, d := range input.Decisions {
			source := input.DecisionSources[d.ID]
			if (d.Repository != "" && d.Repository != repository) || (source != nil && source.Repository != repository) {
				out.Resolved[d.ID] = decisionResolution{DecisionID: d.ID, Reason: "Original decision belongs to a different repository; relevance is unsupported."}
			}
		}
	}
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='feature_decision')`).Scan(&exists); err != nil || !exists {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT decision_id,input_hash,feature_id,pull_number,key_eligible,reason FROM feature_decision WHERE version_id=? AND repository=? AND context_version=?`, version, repository, attributionVersion)
	if err != nil {
		return out, err
	}
	stored := map[string]decisionResolution{}
	for rows.Next() {
		var r decisionResolution
		if err = rows.Scan(&r.DecisionID, &r.Hash, &r.FeatureID, &r.Pull, &r.KeyEligible, &r.Reason); err != nil {
			_ = rows.Close()
			return out, err
		}
		stored[r.DecisionID] = r
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	for _, input := range out.Inputs {
		for _, d := range input.Decisions {
			if _, blocked := out.Resolved[d.ID]; blocked {
				continue
			}
			if r, ok := stored[d.ID]; ok && r.Hash == attributionHash(input, d) {
				out.Resolved[d.ID] = r
			}
		}
	}
	return out, nil
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// resolveDecision uses a durable semantic answer when current. Before the
// worker catches up, only unique positive file support is accepted; no commit
// dominance, earliest PR or single-feature shortcut can invent relevance.
func resolveDecision(d Decision, links features.SessionLinks, resolutions map[string]decisionResolution) decisionResolution {
	if r, ok := resolutions[d.ID]; ok {
		return r
	}
	scores := map[string]int{}
	pulls := map[string]map[int]int{}
	for _, p := range links.PullRequests {
		matched := decisions.NewCitedPaths(p.Paths)
		paths := []string{}
		for _, file := range d.Files {
			if _, ok := matched.Match(file); ok {
				paths = append(paths, file)
			}
		}
		if len(paths) == 0 {
			continue
		}
		scores[p.FeatureID] = max(scores[p.FeatureID], len(paths))
		if pulls[p.FeatureID] == nil {
			pulls[p.FeatureID] = map[int]int{}
		}
		pulls[p.FeatureID][int(p.Number)] = len(paths)
	}
	best := 0
	id := ""
	tie := false
	for feature, score := range scores {
		if score > best {
			best, id, tie = score, feature, false
		} else if score == best {
			tie = true
		}
	}
	out := decisionResolution{DecisionID: d.ID, Reason: "no unambiguous semantic resolution"}
	if tie || best == 0 || len(scores) > 1 {
		return out
	}
	out.FeatureID = id
	out.Reason = "unique positive file support; significance pending"
	best = 0
	tie = false
	for n, score := range pulls[id] {
		if score > best {
			best, out.Pull, tie = score, n, false
		} else if score == best {
			tie = true
		}
	}
	if tie {
		out.Pull = 0
	}
	return out
}

type AttributionReport struct {
	Sessions, Decisions int
	Spent               float64
	Stopped             string
}

// AttributeDecisions is a bounded worker stage and is also called by the public
// Summarize entry point. Successful answers are cached, including explicit none.
func (w *FeatureWorker) AttributeDecisions(ctx context.Context, repository, dir string, o FeatureOptions) (AttributionReport, error) {
	report := AttributionReport{}
	r, err := w.newRun(ctx, repository, dir, o)
	if err != nil {
		return report, err
	}
	keys, err := collect(scanSession)(w.Store.db.QueryContext(ctx, `SELECT cs.agent,cs.session_id FROM capture_session cs WHERE cs.repository=? AND EXISTS(SELECT 1 FROM current_decision d WHERE d.agent=cs.agent AND d.session_id=cs.session_id AND d.superseded=0) ORDER BY cs.extracted_at,cs.agent,cs.session_id`, repository))
	if err != nil {
		return report, err
	}
	current, err := w.Store.currentOf(ctx, keys)
	if err != nil {
		return report, err
	}
	links, err := w.Store.sessionLinksOf(ctx, r.version, repository, keys)
	if err != nil {
		return report, err
	}
	initial, err := w.Store.attributionSet(ctx, r.version, repository, keys, current, links)
	if err != nil {
		return report, err
	}
	limit := FeatureAttributionRunLimit
	if o.AttributionLimit > 0 {
		limit = o.AttributionLimit
	}
	settled, err := w.Store.settleRows(ctx, r.version, repository)
	if err != nil {
		return report, err
	}
	for _, key := range keys {
		if report.Sessions >= limit {
			break
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if !slices.ContainsFunc(initial.Inputs[key].Decisions, func(d Decision) bool { _, resolved := initial.Resolved[d.ID]; return !d.Superseded && !resolved }) {
			continue
		}
		guardRevision, err := w.Store.revision(ctx, repository)
		if err != nil {
			return report, err
		}
		freshCurrent, err := w.Store.currentOf(ctx, []SessionKey{key})
		if err != nil {
			return report, err
		}
		freshLinks, err := w.Store.sessionLinksOf(ctx, r.version, repository, []SessionKey{key})
		if err != nil {
			return report, err
		}
		freshSet, err := w.Store.attributionSet(ctx, r.version, repository, []SessionKey{key}, freshCurrent, freshLinks)
		if err != nil {
			return report, err
		}
		input := freshSet.Inputs[key]
		pending := []Decision{}
		for _, d := range input.Decisions {
			if !d.Superseded {
				if _, ok := freshSet.Resolved[d.ID]; !ok {
					pending = append(pending, d)
				}
			}
		}
		if len(pending) == 0 || len(input.Candidates) == 0 {
			continue
		}
		pending = pending[:min(len(pending), 40)]
		input.Decisions = pending
		data, err := json.Marshal(input)
		if err != nil {
			return report, err
		}
		trigger := hash(string(data))
		item := key.Agent + "/" + key.ID
		if settled.waiting(StageAttribution, item, trigger, r.now) {
			continue
		}
		report.Sessions++
		if len(data) > 512<<10 {
			if _, err = r.backOff(ctx, StageAttribution, item, trigger, 0, errors.New("semantic attribution input exceeds 512 KiB; decisions remain unresolved")); err != nil {
				return report, err
			}
			continue
		}
		source, err := r.itemSource(ctx, assignItem{Kind: kindSession, Session: key}, placedPull{})
		if err != nil {
			_, err = r.backOff(ctx, StageAttribution, item, trigger, 0, err)
			if err != nil {
				return report, err
			}
			continue
		}
		check := func(data []byte) error { _, err := parseAttribution(data, input); return err }
		request := decisions.ModelRequest{System: attributionPrompt, User: string(data), SchemaName: attributionVersion, Schema: attributionReplySchema(), MaxOutputTokens: 8192}
		result, callErr := r.calls.completeRepository(ctx, o.Backend, o.Model, Attempt{Repository: repository, Stage: StageAttribution, Item: item}, request, source, check)
		source.close()
		if result.Cost != nil {
			report.Spent += *result.Cost
		}
		if result.Skip != "" {
			report.Stopped = result.Skip
			return report, callErr
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if callErr != nil {
			if _, err = r.backOff(ctx, StageAttribution, item, trigger, 1, callErr); err != nil {
				return report, err
			}
			continue
		}
		resolutions, err := parseAttribution(result.JSON, input)
		if err != nil {
			return report, err
		}
		for i := range resolutions {
			resolutions[i].Hash = attributionHash(input, pending[i])
		}
		if err = w.Store.saveAttribution(ctx, r.version, repository, resolutions, input, result.Source, r.now, guardRevision); err != nil {
			return report, err
		}
		report.Decisions += len(resolutions)
	}
	return report, nil
}

func attributionReplySchema() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"decisions"}, "properties": map[string]any{"decisions": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"decision_id", "feature_id", "pull_number", "key_eligible", "reason"}, "properties": map[string]any{"decision_id": str, "feature_id": str, "reason": str, "pull_number": map[string]any{"type": "integer"}, "key_eligible": map[string]any{"type": "boolean"}}}}}}
}
func parseAttribution(data []byte, input attributionInput) ([]decisionResolution, error) {
	var reply struct {
		Decisions []decisionResolution `json:"decisions"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, err
	}
	byID := map[string]decisionResolution{}
	for _, r := range reply.Decisions {
		if _, ok := byID[r.DecisionID]; ok {
			return nil, fmt.Errorf("duplicate decision %s", r.DecisionID)
		}
		if !slices.ContainsFunc(input.Decisions, func(d Decision) bool { return d.ID == r.DecisionID }) {
			return nil, fmt.Errorf("unknown decision %s", r.DecisionID)
		}
		if strings.TrimSpace(r.Reason) == "" {
			return nil, errors.New("attribution requires an evidence reason")
		}
		if r.FeatureID == "" {
			if r.Pull != 0 || r.KeyEligible {
				return nil, errors.New("unresolved decisions cannot have a PR or key eligibility")
			}
		} else {
			index := slices.IndexFunc(input.Candidates, func(c attributionCandidate) bool { return c.ID == r.FeatureID })
			if index < 0 {
				return nil, errors.New("attribution chose an ineligible feature")
			}
			if r.Pull != 0 && !slices.ContainsFunc(input.Candidates[index].Pulls, func(p attributionPull) bool { return p.Number == r.Pull }) {
				return nil, errors.New("attribution chose an unrelated PR")
			}
		}
		byID[r.DecisionID] = r
	}
	if len(byID) != len(input.Decisions) {
		return nil, errors.New("attribution must resolve every supplied decision exactly once")
	}
	out := make([]decisionResolution, len(input.Decisions))
	for i, d := range input.Decisions {
		out[i] = byID[d.ID]
		if d.Superseded && out[i].KeyEligible {
			return nil, errors.New("superseded decision cannot be a highlight")
		}
	}
	return out, nil
}

func (s *Store) saveAttribution(ctx context.Context, version int64, repository string, resolutions []decisionResolution, fresh attributionInput, source *SourceContext, now time.Time, revision int64) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var currentRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM timeline_revision WHERE repository=?),0)`, repository).Scan(&currentRevision); err != nil {
			return err
		}
		if currentRevision != revision {
			return nil
		}
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM feature_version WHERE id=? AND repository=?`, version, repository).Scan(&state); err != nil {
			return err
		}
		if state == VersionRetired {
			return errVersionRetired
		}
		dirty := []string{}
		for _, r := range resolutions {
			i := slices.IndexFunc(fresh.Decisions, func(d Decision) bool { return d.ID == r.DecisionID })
			if i < 0 || r.Hash != attributionHash(fresh, fresh.Decisions[i]) {
				continue
			}
			var old string
			err := tx.QueryRowContext(ctx, `SELECT feature_id FROM feature_decision WHERE version_id=? AND repository=? AND decision_id=?`, version, repository, r.DecisionID).Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			dirty = append(dirty, old, r.FeatureID)
			_, err = tx.ExecContext(ctx, `INSERT INTO feature_decision(version_id,repository,decision_id,input_hash,context_version,feature_id,pull_number,key_eligible,reason,source_json) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(version_id,repository,decision_id) DO UPDATE SET input_hash=excluded.input_hash,context_version=excluded.context_version,feature_id=excluded.feature_id,pull_number=excluded.pull_number,key_eligible=excluded.key_eligible,reason=excluded.reason,source_json=excluded.source_json`, version, repository, r.DecisionID, r.Hash, attributionVersion, r.FeatureID, r.Pull, r.KeyEligible, r.Reason, string(data))
			if err != nil {
				return err
			}
		}
		dirty = slices.DeleteFunc(dirty, func(id string) bool { return id == "" })
		return markFeatures(ctx, tx, dirty, now)
	})
}

func hash(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
