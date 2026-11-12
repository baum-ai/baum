package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/features/consolidate"
)

type scopeQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type resolvedScopeRecord struct {
	ScopeID string
	FeatureScope
}

// Original map definitions are immutable. A separate mapping can rehome or
// retire their live context after a split while preserving source provenance.
func resolvedScopeRecords(ctx context.Context, q scopeQuerier, version int64, repository string) ([]resolvedScopeRecord, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='feature_scope')`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	records, err := collect(func(rows *sql.Rows) (resolvedScopeRecord, error) {
		var r resolvedScopeRecord
		var data string
		err := rows.Scan(&r.ScopeID, &r.Revision, &data)
		if err == nil {
			err = json.Unmarshal([]byte(data), &r.Scope)
		}
		return r, err
	})(q.QueryContext(ctx, `SELECT feature_id,revision,scope_json FROM feature_scope WHERE version_id=? AND repository=? ORDER BY feature_id`, version, repository))
	if err != nil {
		return nil, err
	}
	type mapping struct{ scope, target, state string }
	mappings := map[string]mapping{}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='feature_scope_target')`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		rows, err := collect(func(rows *sql.Rows) (mapping, error) {
			var m mapping
			err := rows.Scan(&m.scope, &m.target, &m.state)
			return m, err
		})(q.QueryContext(ctx, `SELECT scope_id,COALESCE(target_id,''),state FROM feature_scope_target WHERE version_id=? AND repository=?`, version, repository))
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			mappings[m.scope] = m
		}
	}
	aliases := map[string]string{}
	rows, err := collect(func(rows *sql.Rows) ([2]string, error) {
		var r [2]string
		err := rows.Scan(&r[0], &r[1])
		return r, err
	})(q.QueryContext(ctx, `SELECT id,COALESCE(merged_into,id) FROM feature WHERE version_id=? AND repository=?`, version, repository))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		aliases[r[0]] = r[1]
	}
	out := []resolvedScopeRecord{}
	for _, r := range records {
		target := r.ScopeID
		if m, ok := mappings[r.ScopeID]; ok {
			if m.state != "active" || m.target == "" {
				continue
			}
			target = m.target
			if _, ok = aliases[target]; !ok {
				continue
			}
		}
		_, materialized := aliases[target]
		visited := map[string]bool{}
		valid := true
		for next, ok := aliases[target]; ok && next != target; next, ok = aliases[target] {
			if visited[target] {
				valid = false
				break
			}
			visited[target] = true
			target = next
		}
		if materialized {
			if _, ok := aliases[target]; !ok {
				valid = false
			}
		}
		if valid {
			r.FeatureID = target
			out = append(out, r)
		}
	}
	return out, nil
}

func splitScopeTarget(scope FeatureScope, pulls []consolidationPull, partition consolidate.Partition) (int, string) {
	examples, paths := map[int]bool{}, map[int]bool{}
	for i, part := range partition.Parts {
		for _, index := range part.PullRequests {
			p := pulls[index]
			for _, n := range scope.Examples {
				if n == p.Number {
					examples[i] = true
				}
			}
			for _, file := range p.Paths {
				for _, prefix := range scope.Paths {
					if file == prefix || strings.HasPrefix(file, strings.TrimSuffix(prefix, "/")+"/") {
						paths[i] = true
					}
				}
			}
		}
	}
	supported := map[int]bool{}
	for i := range examples {
		supported[i] = true
	}
	for i := range paths {
		supported[i] = true
	}
	if len(supported) != 1 {
		return -1, "Split evidence is absent, ambiguous or conflicting; original scope context is inactive."
	}
	for i := range supported {
		return i, "Original scope uniquely follows this split part through supplied example/path evidence."
	}
	return -1, "No supported split target."
}

func rehomeSplitScopes(ctx context.Context, tx *sql.Tx, version int64, repository, id string, pulls []consolidationPull, partition consolidate.Partition, partIDs []string, source *SourceContext) error {
	records, err := resolvedScopeRecords(ctx, tx, version, repository)
	if err != nil {
		return err
	}
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.FeatureID != id {
			continue
		}
		index, reason := splitScopeTarget(record.FeatureScope, pulls, partition)
		var target any
		state := "unresolved"
		if index >= 0 {
			target = partIDs[index]
			state = "active"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO feature_scope_target(scope_id,version_id,repository,target_id,state,reason,source_json)VALUES(?,?,?,?,?,?,?) ON CONFLICT(scope_id)DO UPDATE SET target_id=excluded.target_id,state=excluded.state,reason=excluded.reason,source_json=excluded.source_json`, record.ScopeID, version, repository, target, state, fmt.Sprintf("%s %s", ConsolidateVersion, reason), string(data)); err != nil {
			return err
		}
	}
	return nil
}
