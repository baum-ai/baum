package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
)

const placementContextVersion = "repository-placement-v1"
const judgmentSchema = `CREATE TABLE IF NOT EXISTS feature_judgment(
 version_id INTEGER NOT NULL, repository TEXT NOT NULL, kind TEXT NOT NULL, item TEXT NOT NULL,
 context_version TEXT NOT NULL, reason TEXT NOT NULL, context_json TEXT NOT NULL,
 PRIMARY KEY(version_id,repository,kind,item));
 CREATE TABLE IF NOT EXISTS feature_scope_target(scope_id TEXT PRIMARY KEY,version_id INTEGER NOT NULL,repository TEXT NOT NULL,target_id TEXT,state TEXT NOT NULL,reason TEXT NOT NULL,source_json TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS feature_reconcile(feature_id TEXT PRIMARY KEY,version_id INTEGER NOT NULL,prompt_version TEXT NOT NULL,checked_version INTEGER NOT NULL);`

type placementContext struct {
	Version string         `json:"version"`
	Reason  string         `json:"reason"`
	Source  *SourceContext `json:"source"`
	Scopes  []FeatureScope `json:"scopes"`
}

func saveJudgment(ctx context.Context, tx *sql.Tx, version int64, repository, kind, item string, judgment *placementContext) error {
	if judgment == nil {
		return nil
	}
	data, err := json.Marshal(judgment)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feature_judgment(version_id,repository,kind,item,context_version,reason,context_json) VALUES (?,?,?,?,?,?,?)
 ON CONFLICT(version_id,repository,kind,item) DO UPDATE SET context_version=excluded.context_version,reason=excluded.reason,context_json=excluded.context_json`, version, repository, kind, item, judgment.Version, judgment.Reason, string(data))
	return err
}

// resolvedScopes preserves the original map revision while following merge
// aliases. Source-only identities remain valid targets before their first join.
func (s *Store) resolvedScopes(ctx context.Context, version int64, repository string) ([]FeatureScope, error) {
	records, err := resolvedScopeRecords(ctx, s.db, version, repository)
	if err != nil {
		return nil, err
	}
	out := make([]FeatureScope, 0, len(records))
	for _, record := range records {
		out = append(out, record.FeatureScope)
	}
	return out, nil
}

func (r *featureRun) itemSource(ctx context.Context, item assignItem, pull placedPull) (repositorySource, error) {
	revision, branch := pull.HeadSHA, pull.HeadRef
	resolution, limitation := "", ""
	if item.Kind == kindSession {
		session, _, err := r.w.Store.Session(ctx, item.Session.Agent, item.Session.ID)
		if err != nil {
			return repositorySource{}, err
		}
		branch = session.Branch
		if session.Source != nil && session.Source.Repository == r.repository {
			revision = session.Source.Revision
		}
		if revision == "" {
			err = r.w.Store.db.QueryRowContext(ctx, `SELECT sc.sha FROM session_commit sc LEFT JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha WHERE sc.repository=? AND sc.agent=? AND sc.session_id=? GROUP BY sc.sha ORDER BY MAX(COALESCE(pc.committed_at,0)) DESC,sc.sha LIMIT 1`, r.repository, item.Session.Agent, item.Session.ID).Scan(&revision)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return repositorySource{}, err
			}
			if revision != "" {
				resolution = "linked_session_commit"
				limitation = "This source is a recorded commit linked to the session, not a reconstruction of every session-time working tree."
			}
		}
		if revision == "" {
			source, err := r.w.Store.FeatureBuildSource(ctx, r.version)
			if err != nil {
				return repositorySource{}, err
			}
			if source != nil {
				revision, branch = source.Revision, source.Branch
				resolution = "feature_map_context"
			} else {
				branch = ""
				resolution = "current_repository_context"
			}
			limitation = "Historical session source was not recorded. This explicitly contextual committed snapshot is not claimed to be session-time code."
		}
	}
	r.mu.Lock()
	cfg := r.prefs.Resolve(r.w.Config)
	r.mu.Unlock()
	source, err := r.w.repositorySource(ctx, r.dir, r.repository, revision, branch, cfg)
	if err != nil {
		return source, err
	}
	if resolution != "" {
		source.Context.Resolution = resolution
		source.Context.Limitations = append(source.Context.Limitations, limitation)
		data, err := json.MarshalIndent(source.Context, "", "  ")
		if err == nil {
			err = os.Remove(filepath.Join(source.Directory, "source.json"))
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(source.Directory, "source.json"), data, 0400)
		}
		if err != nil {
			source.close()
			return repositorySource{}, err
		}
	}
	return source, nil
}

// featureSource uses an actual member revision, not an old map revision relabeled
// as incoming source. The map keeps its separate pinned context in the input.
func (r *featureRun) featureSource(ctx context.Context, featureID string) (repositorySource, error) {
	var revision, branch string
	query := `SELECT p.head_sha,p.head_ref FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number WHERE fp.version_id=? AND fp.repository=?`
	args := []any{r.version, r.repository}
	if featureID != "" {
		query += ` AND fp.feature_id=?`
		args = append(args, featureID)
	}
	err := r.w.Store.db.QueryRowContext(ctx, query+` ORDER BY p.updated_at DESC,p.number DESC LIMIT 1`, args...).Scan(&revision, &branch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return repositorySource{}, err
	}
	if revision == "" {
		source, err := r.w.Store.FeatureBuildSource(ctx, r.version)
		if err != nil {
			return repositorySource{}, err
		}
		if source != nil {
			revision, branch = source.Revision, source.Branch
		}
	}
	r.mu.Lock()
	cfg := r.prefs.Resolve(r.w.Config)
	r.mu.Unlock()
	return r.w.repositorySource(ctx, r.dir, r.repository, revision, branch, cfg)
}

func (r *featureRun) contextInput(ctx context.Context, input string, ids []string) (string, []FeatureScope, error) {
	scopes, err := r.w.Store.resolvedScopes(ctx, r.version, r.repository)
	if err != nil {
		return "", nil, err
	}
	selected := slices.DeleteFunc(slices.Clone(scopes), func(s FeatureScope) bool { return !slices.Contains(ids, s.FeatureID) })
	used := slices.Clone(selected)
	// Inputs use candidate references, never host IDs, just like the assigner.
	for i := range selected {
		for j, id := range ids {
			if selected[i].FeatureID == id {
				selected[i].FeatureID = "F" + strconv.Itoa(j+1)
				break
			}
		}
	}
	var data map[string]any
	if err = json.Unmarshal([]byte(input), &data); err != nil {
		return "", nil, err
	}
	data["source_scopes"] = selected
	source, err := r.w.Store.FeatureBuildSource(ctx, r.version)
	if err != nil {
		return "", nil, err
	}
	data["feature_map_source"] = source
	encoded, err := json.Marshal(data)
	return string(encoded), used, err
}

func scopeRelevance(text string, scopes []FeatureScope) int {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r < 'a' || r > 'z' })
	seen := map[string]bool{}
	score := 0
	for _, scope := range scopes {
		hay := strings.ToLower(scope.Title + " " + scope.Purpose + " " + scope.Key)
		for _, word := range words {
			if len(word) > 3 && !seen[word] && strings.Contains(hay, word) {
				score++
				seen[word] = true
			}
		}
	}
	return score
}

func (r *featureRun) semanticCall(ctx context.Context, feature string, a Attempt, req decisions.ModelRequest, check func([]byte) error) (callResult, error) {
	source, err := r.featureSource(ctx, feature)
	if err != nil {
		return callResult{}, err
	}
	defer source.close()
	return r.calls.completeRepository(ctx, r.opts.Backend, r.opts.Model, a, req, source, check)
}

func (w *FeatureWorker) repositorySource(ctx context.Context, dir, repository, revision, branch string, cfg config.Config) (repositorySource, error) {
	if w.source != nil {
		return w.source(ctx, dir, repository, revision, branch, cfg)
	}
	return prepareRepositorySource(ctx, dir, repository, revision, branch, cfg)
}
