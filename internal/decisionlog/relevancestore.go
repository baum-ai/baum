package decisionlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/decisions"
)

const relevanceSchema = `
CREATE TABLE IF NOT EXISTS decision_relevance_input(decision_id TEXT PRIMARY KEY, content_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pr_decision_relevance(
  repository TEXT NOT NULL, number INTEGER NOT NULL, decision_id TEXT NOT NULL,
  context_hash TEXT NOT NULL, decision_hash TEXT NOT NULL, state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '', retry_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(repository, number, decision_id));
CREATE TABLE IF NOT EXISTS pr_relevance_patch(
  repository TEXT NOT NULL, number INTEGER NOT NULL, revision TEXT NOT NULL,
  patch TEXT NOT NULL, failure TEXT NOT NULL DEFAULT '', retry_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(repository, number));`

// RelevanceContext is the cached PR metadata that determines which changes
// a classification describes. Reading it never contacts GitHub or Git.
type RelevanceContext struct {
	Repository                       string
	Number                           int
	Title, Body, Base, Head, BaseRef string
}

func relevanceHash(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func decisionFingerprint(repository string, d decisions.Decision) string {
	// File order is not meaningful. EvidenceTurn is not a persisted field;
	// the quote and all its cited turns are covered by the fingerprint.
	d.EvidenceTurn = 0
	d.Files = slices.Compact(slices.Sorted(slices.Values(d.Files)))
	if d.Files == nil {
		d.Files = []string{}
	}
	return relevanceHash(struct {
		Repository string
		Decision   decisions.Decision
	}{strings.ToLower(repository), d})
}

// decisionHints reads only identities and precomputed fingerprints, so the
// PR endpoint does not load the text/files of hundreds of hidden decisions.
type decisionHint struct{ ID, repository, hash string }

func (s *Store) decisionHints(ctx context.Context, keys []SessionKey) (map[SessionKey][]decisionHint, error) {
	out := map[SessionKey][]decisionHint{}
	if len(keys) == 0 {
		return out, nil
	}
	set, err := sessionSet(keys)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.agent,d.session_id,d.decision_id,d.repository,COALESCE(h.content_hash,'')
FROM current_decision d LEFT JOIN decision_relevance_input h ON h.decision_id=d.decision_id
WHERE (d.agent,d.session_id)`+inSessions+` ORDER BY d.agent,d.session_id,d.ordinal`, set)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key SessionKey
		var d decisionHint
		if err := rows.Scan(&key.Agent, &key.ID, &d.ID, &d.repository, &d.hash); err != nil {
			return nil, err
		}
		out[key] = append(out[key], d)
	}
	return out, rows.Err()
}

func (s *Store) relevanceContext(ctx context.Context, repository string, number int) (RelevanceContext, error) {
	p := RelevanceContext{Repository: strings.ToLower(repository), Number: number}
	err := s.db.QueryRowContext(ctx, `SELECT title, body, base_sha, head_sha, base_ref FROM pull_request WHERE repository=? AND number=?`,
		p.Repository, number).Scan(&p.Title, &p.Body, &p.Base, &p.Head, &p.BaseRef)
	return p, err
}

type relevanceRow struct {
	ID, ContextHash, DecisionHash, State, Reason string
	RetryAt                                      time.Time
}

func (s *Store) relevanceRows(ctx context.Context, repository string, number int) (map[string]relevanceRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT decision_id, context_hash, decision_hash, state, retry_at
FROM pr_decision_relevance WHERE repository=? AND number=?`, repository, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]relevanceRow{}
	for rows.Next() {
		var r relevanceRow
		var at int64
		if err := rows.Scan(&r.ID, &r.ContextHash, &r.DecisionHash, &r.State, &at); err != nil {
			return nil, err
		}
		r.RetryAt = time.UnixMilli(at)
		out[r.ID] = r
	}
	return out, rows.Err()
}

func (s *Store) saveRelevance(ctx context.Context, p RelevanceContext, rows []relevanceRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		// Upgrade older captures lazily, in the same bounded transaction as
		// the result. A concurrent capture's newer fingerprint takes precedence.
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_relevance_input(decision_id,content_hash) VALUES(?,?) ON CONFLICT(decision_id) DO NOTHING`, r.ID, r.DecisionHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pr_decision_relevance
(repository, number, decision_id, context_hash, decision_hash, state, reason, retry_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(repository, number, decision_id) DO UPDATE SET context_hash=excluded.context_hash,
decision_hash=excluded.decision_hash, state=excluded.state, reason=excluded.reason, retry_at=excluded.retry_at`,
			p.Repository, p.Number, r.ID, r.ContextHash, r.DecisionHash, r.State, r.Reason, r.RetryAt.UnixMilli()); err != nil {
			return err
		}
	}
	// The cache keeps the latest result for a pair, not a history of every
	// model/revision. Obsolete decisions cannot be returned and need no cache.
	if _, err := tx.ExecContext(ctx, `DELETE FROM pr_decision_relevance WHERE repository=? AND number=?
AND NOT EXISTS(SELECT 1 FROM current_decision d WHERE d.decision_id=pr_decision_relevance.decision_id)`, p.Repository, p.Number); err != nil {
		return err
	}
	return tx.Commit()
}

func missingRelevanceContext(err error) bool { return errors.Is(err, sql.ErrNoRows) }
