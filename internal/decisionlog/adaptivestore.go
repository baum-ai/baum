package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/github"
)

const adaptiveSchema = `
CREATE TABLE IF NOT EXISTS feature_build_state(
 version_id INTEGER PRIMARY KEY, protocol TEXT NOT NULL, sequence INTEGER NOT NULL, state_json TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS feature_scope(
 feature_id TEXT PRIMARY KEY, version_id INTEGER NOT NULL, repository TEXT NOT NULL, revision TEXT NOT NULL, scope_json TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS feature_scope_version ON feature_scope(version_id, repository);
CREATE UNIQUE INDEX IF NOT EXISTS feature_version_active_building ON feature_version(repository) WHERE state IN ('building','building_adaptive');`

var errBuildConcurrent = errors.New("feature build changed concurrently; resume from its saved checkpoint")
var errWorkflowMismatch = errors.New("an existing build uses a different workflow; explicitly resume that workflow before starting another")

// FeatureScope keeps source-derived purpose and boundaries after members join.
// FeatureID is reserved even for a source-only scope; placePull materializes it
// transactionally on its first membership, so the Timeline has no empty cards.
type FeatureScope struct {
	FeatureID string `json:"feature_id"`
	Revision  string `json:"revision"`
	adaptive.Scope
}

// BuildCoverage distinguishes metadata available from evidence explicitly read.
// Inspected counts unique PRs whose descriptions or code the host supplied on
// request; it never claims the model read every available title.
type BuildCoverage = adaptive.Coverage

type adaptiveCode struct {
	Paths   []string          `json:"paths"`
	Commits []PullCommit      `json:"commits"`
	Patches map[string]string `json:"patches"`
}

type adaptiveState struct {
	Protocol          string                           `json:"protocol"`
	Source            SourceContext                    `json:"source"`
	Catalog           map[int]github.ListedPull        `json:"catalog"`
	Details           map[int]bool                     `json:"details"`
	Decisions         map[int]adaptiveDecisionEvidence `json:"decisions"`
	Code              map[int]adaptiveCode             `json:"code"`
	Selected          map[int]bool                     `json:"selected"`
	Scopes            map[string]adaptive.Scope        `json:"scopes"`
	IDs               map[string]string                `json:"scope_ids"`
	Assignments       map[int]adaptive.Assignment      `json:"assignments"`
	Windows           map[string]bool                  `json:"catalog_windows"`
	Pending           *adaptive.Reply                  `json:"pending_request,omitempty"`
	Since             string                           `json:"since"`
	Truncated         bool                             `json:"truncated"`
	Step              int                              `json:"step"`
	NoProgress        int                              `json:"no_progress"`
	Failures          int                              `json:"failures"`
	ExplorationClosed bool                             `json:"exploration_closed"`
	Complete          bool                             `json:"complete"`
	// CoverageAsked is set once a finish was answered with the coverage
	// question, so it is asked once in a build.
	CoverageAsked bool      `json:"coverage_asked,omitempty"`
	Feedback      string    `json:"feedback"`
	Sequence      int64     `json:"-"`
	RetryAt       time.Time `json:"retry_at,omitempty"`
}

func newAdaptiveState() *adaptiveState {
	return &adaptiveState{Protocol: adaptive.Version, Decisions: map[int]adaptiveDecisionEvidence{}, Catalog: map[int]github.ListedPull{}, Details: map[int]bool{}, Code: map[int]adaptiveCode{}, Selected: map[int]bool{}, Scopes: map[string]adaptive.Scope{}, IDs: map[string]string{}, Assignments: map[int]adaptive.Assignment{}, Windows: map[string]bool{}}
}

func (a *adaptiveState) coverage() *BuildCoverage {
	seen := map[int]bool{}
	for n := range a.Details {
		seen[n] = true
	}
	for n := range a.Decisions {
		seen[n] = true
	}
	for n := range a.Code {
		seen[n] = true
	}
	var retry *time.Time
	if !a.RetryAt.IsZero() {
		retry = &a.RetryAt
	}
	var source *adaptive.SourceCoverage
	if a.Source.Revision != "" {
		source = &adaptive.SourceCoverage{Revision: a.Source.Revision, Omissions: len(a.Source.Omissions), Limitations: slices.Clone(a.Source.Limitations)}
	}
	return &BuildCoverage{Source: source, RetryAt: retry, Protocol: a.Protocol, Since: a.Since, Catalogued: len(a.Catalog), Inspected: len(seen), Selected: len(a.Selected), Completed: len(a.Assignments), Remaining: len(a.Selected) - len(a.Assignments), Truncated: a.Truncated, ExplorationClosed: a.ExplorationClosed, Reason: a.Feedback}
}

func (s *Store) adaptiveState(ctx context.Context, version int64) (*adaptiveState, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='feature_build_state')`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	var protocol, data string
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT protocol, sequence, state_json FROM feature_build_state WHERE version_id=?`, version).Scan(&protocol, &seq, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if protocol != adaptive.Version {
		return nil, errWorkflowMismatch
	}
	a := newAdaptiveState()
	if err := json.Unmarshal([]byte(data), a); err != nil {
		return nil, err
	}
	a.Sequence = seq
	return a, nil
}

// FeatureScopes reads the durable definitions for one version/repository.
func (s *Store) FeatureScopes(ctx context.Context, version int64, repository string) ([]FeatureScope, error) {
	// An older read-only database may predate the adaptive workflow.
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='feature_scope')`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	return collect(func(rows *sql.Rows) (FeatureScope, error) {
		var f FeatureScope
		var data string
		err := rows.Scan(&f.FeatureID, &f.Revision, &data)
		if err == nil {
			err = json.Unmarshal([]byte(data), &f.Scope)
		}
		return f, err
	})(s.db.QueryContext(ctx, `SELECT feature_id, revision, scope_json FROM feature_scope WHERE version_id=? AND repository=? ORDER BY feature_id`, version, repository))
}

// FeatureBuildSource is the pinned context the version's feature map used.
func (s *Store) FeatureBuildSource(ctx context.Context, version int64) (*SourceContext, error) {
	a, err := s.adaptiveState(ctx, version)
	if err != nil || a == nil {
		return nil, err
	}
	return &a.Source, nil
}

func (s *Store) requireLegacyBuild(ctx context.Context, repository string) error {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM feature_version v JOIN feature_build_state b ON b.version_id=v.id WHERE v.repository=? AND v.state IN (?,?)`, repository, VersionBuilding, VersionAdaptiveBuilding).Scan(&n)
	if err == nil && n > 0 {
		return errWorkflowMismatch
	}
	return err
}

// saveAdaptive checkpoints scope, membership and progress in one transaction.
// The sequence compare-and-swap serializes independent workers/clones too.
func (s *Store) saveAdaptive(ctx context.Context, version int64, repository string, a *adaptiveState, promote bool, now time.Time) error {
	for key := range a.Scopes {
		if a.IDs[key] == "" {
			a.IDs[key] = uuid.NewString()
		}
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM feature_version WHERE id=? AND repository=?`, version, repository).Scan(&state); err != nil {
			return err
		}
		if state != VersionAdaptiveBuilding {
			return errVersionRetired
		}
		if a.Sequence == 0 {
			result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO feature_build_state(version_id,protocol,sequence,state_json,updated_at) VALUES (?,?,1,?,?)`, version, a.Protocol, string(data), now.UnixMilli())
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return errBuildConcurrent
			}
		} else {
			result, err := tx.ExecContext(ctx, `UPDATE feature_build_state SET sequence=sequence+1,state_json=?,updated_at=? WHERE version_id=? AND protocol=? AND sequence=?`, string(data), now.UnixMilli(), version, a.Protocol, a.Sequence)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return errBuildConcurrent
			}
		}
		// Catalog-only metadata stays out of the live sync queue. Only accepted
		// memberships/skips enter its shared cache, after this checkpoint wins CAS.
		pulls := make([]github.ListedPull, 0, len(a.Assignments))
		for number := range a.Assignments {
			pulls = append(pulls, a.Catalog[number])
		}
		if err := s.upsertPulls(ctx, tx, repository, pulls, true); err != nil {
			return err
		}
		for key, scope := range a.Scopes {
			encoded, err := json.Marshal(scope)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature_scope(feature_id,version_id,repository,revision,scope_json) VALUES (?,?,?,?,?) ON CONFLICT(feature_id) DO UPDATE SET scope_json=excluded.scope_json`, a.IDs[key], version, repository, a.Source.Revision, string(encoded)); err != nil {
				return err
			}
		}
		affected := map[string]bool{}
		for number, assignment := range a.Assignments {
			p := a.Catalog[number]
			var previous string
			if err := tx.QueryRowContext(ctx, `SELECT feature_id FROM feature_pull WHERE version_id=? AND repository=? AND number=?`, version, repository, number).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if assignment.Scope == "" {
				if previous != "" {
					affected[previous] = true
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM feature_pull WHERE version_id=? AND repository=? AND number=?`, version, repository, number); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO feature_settle(version_id,repository,kind,item,trigger_key,outcome) VALUES (?,?,?,?,?,'settled') ON CONFLICT(version_id,repository,kind,item) DO UPDATE SET trigger_key=excluded.trigger_key,outcome='settled'`, version, repository, kindPull, strconv.Itoa(number), p.HeadSHA); err != nil {
					return err
				}
				continue
			}
			scope := a.Scopes[assignment.Scope]
			id := a.IDs[assignment.Scope]
			if previous != id {
				affected[id] = true
				if previous != "" {
					affected[previous] = true
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature(id,version_id,repository,title,description,digest,created_at,updated_at,changed_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, version, repository, scope.Title, scope.Purpose, scope.Digest, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
				return err
			}
			paths := a.Code[number].Paths
			if paths == nil {
				paths = []string{}
			}
			encoded, _ := json.Marshal(paths)
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature_pull(version_id,repository,number,feature_id,assigned_by,assigned_at,assigned_head_sha,assigned_paths_json,assigned_merged) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(version_id,repository,number) DO UPDATE SET feature_id=excluded.feature_id,assigned_head_sha=excluded.assigned_head_sha,assigned_paths_json=excluded.assigned_paths_json,assigned_merged=excluded.assigned_merged`, version, repository, number, id, "adaptive", now.UnixMilli(), p.HeadSHA, string(encoded), p.MergedAt != nil); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM feature_settle WHERE version_id=? AND repository=? AND kind=? AND item=?`, version, repository, kindPull, strconv.Itoa(number)); err != nil {
				return err
			}
		}
		ids := make([]string, 0, len(affected))
		for id := range affected {
			ids = append(ids, id)
		}
		if err := markFeatures(ctx, tx, ids, now); err != nil {
			return err
		}
		// Every checkpoint carries concise text. This avoids a per-PR summary fanout.
		for key, scope := range a.Scopes {
			if _, err := tx.ExecContext(ctx, `UPDATE feature SET title=?,description=?,digest=?,key_decisions_json='[]',summarized_version=membership_version,dirty_since=NULL WHERE id=? AND version_id=?`, scope.Title, scope.Purpose, scope.Digest, a.IDs[key], version); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feature_version SET total=?,placed=?,stopped='' WHERE id=?`, len(a.Selected), len(a.Assignments), version); err != nil {
			return err
		}
		if !promote {
			return nil
		}
		if !a.Complete || len(a.Selected) != len(a.Assignments) {
			return errors.New("selected work is incomplete")
		}
		for n := range a.Selected {
			if _, ok := a.Assignments[n]; !ok {
				return fmt.Errorf("selected PR %d is incomplete", n)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM feature WHERE version_id=? AND NOT EXISTS(SELECT 1 FROM feature_pull fp WHERE fp.feature_id=feature.id) AND NOT EXISTS(SELECT 1 FROM feature_session fs WHERE fs.feature_id=feature.id)`, version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feature_version SET state=? WHERE repository=? AND state=?`, VersionRetired, repository, VersionCurrent); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feature_version SET state=?,switched_at=?,stopped='' WHERE id=?`, VersionCurrent, now.UnixMilli(), version); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM feature_consolidation WHERE repository=?`, repository)
		return err
	})
	if err == nil {
		a.Sequence++
	}
	return s.changed(err)
}

func adaptiveScopeNumbers(a *adaptiveState) []int {
	out := make([]int, 0, len(a.Catalog))
	for n := range a.Catalog {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// adaptivePaths records requested evidence only while the live PR still has
// the pinned head, in the same transaction as its commit/path replacement.
func (s *Store) adaptivePaths(ctx context.Context, repository string, number int, head string, paths []string, commits []PullCommit) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT head_sha FROM pull_request WHERE repository=? AND number=?`, repository, number).Scan(&current); err != nil {
			return err
		}
		if current != head {
			return nil
		}
		return s.setPaths(ctx, tx, repository, number, head, paths, commits)
	})
}

// FeatureCoverage reads coverage for either a building or current version.
func (s *Store) FeatureCoverage(ctx context.Context, repository, state string) (*BuildCoverage, error) {
	var version int64
	query := `SELECT id FROM feature_version WHERE repository=? AND state=?`
	args := []any{repository, state}
	if state == VersionBuilding {
		query = `SELECT id FROM feature_version WHERE repository=? AND state IN (?,?)`
		args = append(args, VersionAdaptiveBuilding)
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a, err := s.adaptiveState(ctx, version)
	if err != nil || a == nil {
		return nil, err
	}
	return a.coverage(), nil
}

// FeatureBuildWorkflow identifies the existing candidate so an explicit Resume
// continues its original workflow. New requested builds are adaptive.
func (s *Store) FeatureBuildWorkflow(ctx context.Context, repository string) (string, error) {
	var version int64
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT id,state FROM feature_version WHERE repository=? AND state IN (?,?)`, repository, VersionBuilding, VersionAdaptiveBuilding).Scan(&version, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return adaptive.Version, nil
	}
	if err != nil {
		return "", err
	}
	a, err := s.adaptiveState(ctx, version)
	if err != nil {
		return "", err
	}
	if a == nil {
		if state == VersionAdaptiveBuilding {
			return adaptive.Version, nil
		}
		return "legacy", nil
	}
	return a.Protocol, nil
}

// ArchiveFeatureBuild replaces only the candidate, preserving the current
// Timeline and retired links. Callers expose this as an explicit fresh-build
// action, separate from Resume. Any racing writer sees the retired version.
func (s *Store) ArchiveFeatureBuild(ctx context.Context, repository string) error {
	return s.changed(s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feature_version SET state=?,stopped='replaced' WHERE repository=? AND state IN (?,?)`, VersionRetired, repository, VersionBuilding, VersionAdaptiveBuilding)
		return err
	}))
}
