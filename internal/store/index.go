package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Run records an attempt independently of immutable successful versions. IDs
// order starts, so an older attempt finishing cannot replace a newer attempt.
// Expires is renewed while the owning process is alive. A process killed
// without cleanup becomes interrupted instead of generating forever.
type Run struct {
	ID       int64     `json:"id"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Started  time.Time `json:"started_at"`
	Expires  time.Time `json:"expires_at"`
	Finished time.Time `json:"finished_at,omitempty"`
}

func validReason(reason string) bool {
	switch reason {
	case "input", "backend", "budget", "validation", "interrupted", "internal":
		return true
	}
	return false
}

func (s *Store) BeginRun(ctx context.Context, k Key, deadline time.Time) (int64, error) {
	if !k.valid() || !deadline.After(time.Now()) {
		return 0, errors.New("invalid run identity or deadline")
	}
	db, err := s.database(true)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `INSERT INTO runs(repository,pr,state,reason,started,expires,finished) VALUES (?,?,'generating','',?,?,0)`, k.repository(), k.Number, time.Now().UnixNano(), deadline.UnixNano())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// HeartbeatRun renews an unfinished attempt without changing its start order.
func (s *Store) HeartbeatRun(ctx context.Context, id int64, deadline time.Time) error {
	if id <= 0 || !deadline.After(time.Now()) {
		return errors.New("invalid run lease")
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE runs SET expires=? WHERE id=? AND state='generating'`, deadline.UnixNano(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}

// FinishRun completes exactly one matching unfinished attempt. Empty reason
// means successful publication; arbitrary provider errors are never persisted.
func (s *Store) FinishRun(ctx context.Context, id int64, reason string) error {
	if id <= 0 || (reason != "" && !validReason(reason)) {
		return errors.New("invalid run result")
	}
	state := "completed"
	if reason != "" {
		state = "failed"
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE runs SET state=?,reason=?,finished=? WHERE id=? AND state='generating'`, state, reason, time.Now().UnixNano(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}

// Summary is the latest document's small index projection and version count.
// All entries and run records in Snapshot come from one SQLite read snapshot.
type Summary struct {
	Key           Key             `json:"key"`
	Latest        *Metadata       `json:"latest"`
	VersionCount  int             `json:"version_count"`
	Title         string          `json:"title"`
	PRState       string          `json:"pr_state"`
	ReviewMinutes *int            `json:"review_minutes"`
	RiskScore     json.RawMessage `json:"risk_score"`
	Run           *Run            `json:"run,omitempty"`
}

func (s *Store) Snapshot(ctx context.Context) ([]Summary, error) {
	db, err := s.database(false)
	if errors.Is(err, ErrNotFound) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT v.repository,v.pr,v.metadata,COALESCE(json_extract(v.document,'$.summary.title'),''),COALESCE(json_extract(v.document,'$.review.pr_state'),''),json_extract(v.document,'$.summary.risk_score'),CASE WHEN json_type(v.document,'$.summary.review_minutes') IN ('integer','real') AND json_extract(v.document,'$.summary.review_minutes') BETWEEN 1 AND 240 AND CAST(json_extract(v.document,'$.summary.review_minutes') AS INTEGER)=json_extract(v.document,'$.summary.review_minutes') THEN CAST(json_extract(v.document,'$.summary.review_minutes') AS INTEGER) END,summary.n FROM versions v JOIN (SELECT repository,pr,MAX(version) latest,COUNT(*) n FROM versions GROUP BY repository,pr) summary ON v.repository=summary.repository AND v.pr=summary.pr AND v.version=summary.latest ORDER BY v.repository,v.pr`)
	if err != nil {
		return nil, err
	}
	entries := map[Key]*Summary{}
	out := []Summary{}
	for rows.Next() {
		var item Summary
		var raw []byte
		var risk sql.NullString
		var minutes sql.NullInt64
		if err = rows.Scan(&item.Key.Repository, &item.Key.Number, &raw, &item.Title, &item.PRState, &risk, &minutes, &item.VersionCount); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if minutes.Valid {
			value := int(minutes.Int64)
			item.ReviewMinutes = &value
		}
		item.Latest = &Metadata{}
		if err = json.Unmarshal(raw, item.Latest); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if risk.Valid {
			if !json.Valid([]byte(risk.String)) {
				_ = rows.Close()
				return nil, errors.New("invalid stored risk assessment")
			}
			item.RiskScore = json.RawMessage(risk.String)
		}
		out = append(out, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		entries[out[i].Key] = &out[i]
	}
	// Read old stores without migrating them. Opening for writes creates runs.
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		rows, err = tx.QueryContext(ctx, `SELECT r.repository,r.pr,r.id,r.state,r.reason,r.started,r.expires,r.finished FROM runs r JOIN (SELECT repository,pr,MAX(id) latest FROM runs GROUP BY repository,pr) summary ON r.id=summary.latest ORDER BY r.repository,r.pr`)
		if err != nil {
			return nil, err
		}
		extra := []Summary{}
		for rows.Next() {
			var k Key
			var run Run
			var started, expires, finished int64
			if err = rows.Scan(&k.Repository, &k.Number, &run.ID, &run.State, &run.Reason, &started, &expires, &finished); err != nil {
				_ = rows.Close()
				return nil, err
			}
			run.Started = time.Unix(0, started).UTC()
			run.Expires = time.Unix(0, expires).UTC()
			if finished != 0 {
				run.Finished = time.Unix(0, finished).UTC()
			}
			if run.State == "generating" && !time.Now().Before(run.Expires) {
				run.State = "failed"
				run.Reason = "interrupted"
			}
			if run.State == "failed" && !validReason(run.Reason) {
				_ = rows.Close()
				return nil, errors.New("invalid stored failure reason")
			}
			if run.State != "generating" && run.State != "failed" && run.State != "completed" {
				_ = rows.Close()
				return nil, fmt.Errorf("invalid stored run state")
			}
			if item := entries[k]; item != nil {
				item.Run = &run
			} else {
				extra = append(extra, Summary{Key: k, Run: &run})
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, extra...)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
