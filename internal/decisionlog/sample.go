package decisionlog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// SampleFeature is a feature of the tour's sample project as the feature
// log would leave it: its text written, its pull requests and the sessions
// placed on it.
type SampleFeature struct {
	ID, Title, Description, Digest string
	// KeyDecisions name the decisions the text cites, which Save stored.
	KeyDecisions []SampleDecisionRef
	Pulls        []int
	// Sessions are Claude Code sessions placed on the feature on their own.
	Sessions []string
	Updated  time.Time
}

// SampleDecisionRef addresses one saved decision of a session's turn.
type SampleDecisionRef struct {
	ID, Agent, Session string
	Turn               int
}

// SeedSample writes the sample project's pull requests, their summaries and
// its features into a store no feature build has run on. Nothing else
// writes features without a model; this is for the sample project alone.
func (s *Store) SeedSample(ctx context.Context, repository string, pulls []github.ListedPull, summaries map[int]string, features []SampleFeature) error {
	if err := s.SaveHistoryPulls(ctx, repository, pulls); err != nil {
		return err
	}
	version, err := s.LiveVersion(ctx, repository)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UnixMilli()
	for number, summary := range summaries {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO pr_summary(repository, number, content_hash, summary, created_at) VALUES (?,?,?,?,?)`,
			repository, number, "sample", summary, now); err != nil {
			return err
		}
	}
	for _, f := range features {
		keys := []keyDecision{}
		for i, k := range f.KeyDecisions {
			keys = append(keys, keyDecision{ID: k.ID, decisionAddress: decisionAddress{Agent: k.Agent, Session: k.Session, Turn: k.Turn}, Ordinal: i + 1})
		}
		raw, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		at := f.Updated.UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO feature(id, version_id, repository, title, description, digest, key_decisions_json,
membership_version, summarized_version, changed_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,1,?,?,?)`,
			f.ID, version, repository, f.Title, f.Description, f.Digest, string(raw), at, at, at); err != nil {
			return err
		}
		for _, number := range f.Pulls {
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?,?,?,?,'sample',?)`,
				version, repository, number, f.ID, at); err != nil {
				return err
			}
		}
		for _, session := range f.Sessions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at) VALUES (?,?,'claude-code',?,?,?)`,
				version, repository, session, f.ID, at); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
