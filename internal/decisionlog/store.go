// Package decisionlog captures the decisions of the coding-agent sessions on
// this machine and keeps them in a private local store. See README.md.
package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Coverage of a session.
const (
	CoverageExtracted = "extracted"
	CoverageSkipped   = "skipped"
)

// Attempt states.
const (
	AttemptPending   = "pending"
	AttemptSucceeded = "succeeded"
	AttemptFailed    = "failed"
	AttemptAbandoned = "abandoned"
)

// StageCapture is the ledger stage of a decision capture's call.
const StageCapture = "capture"

// Retention: settled attempts and obsolete decisions older than this are
// pruned.
const Retention = 90 * 24 * time.Hour

const schema = `
CREATE TABLE IF NOT EXISTS capture_session(
  agent TEXT NOT NULL, session_id TEXT NOT NULL,
  repository TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', transcript_path TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL DEFAULT '', content_hash TEXT NOT NULL DEFAULT '', prefix_hash TEXT NOT NULL DEFAULT '',
  prompt_version TEXT NOT NULL DEFAULT '', backend TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  decisions_json TEXT, dropped_json TEXT,
  input_tokens INTEGER NOT NULL DEFAULT 0, cached_input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL, failures INTEGER NOT NULL DEFAULT 0,
  coverage TEXT NOT NULL, skip_reason TEXT NOT NULL DEFAULT '',
  extracted_at INTEGER, updated_at INTEGER NOT NULL,
  PRIMARY KEY(agent, session_id));
CREATE TABLE IF NOT EXISTS decision(
  decision_id TEXT PRIMARY KEY, agent TEXT NOT NULL, session_id TEXT NOT NULL, repository TEXT NOT NULL DEFAULT '',
  ordinal INTEGER NOT NULL, title TEXT NOT NULL, summary TEXT NOT NULL, category TEXT NOT NULL, who TEXT NOT NULL,
  trigger TEXT NOT NULL, rationale TEXT NOT NULL, rejected_alternative TEXT NOT NULL, evidence TEXT NOT NULL,
  superseded INTEGER NOT NULL, turns_json TEXT NOT NULL, first_turn INTEGER NOT NULL, decided_turn INTEGER NOT NULL,
  decided_at INTEGER NOT NULL, branch TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, obsolete_at INTEGER);
CREATE INDEX IF NOT EXISTS decision_session ON decision(agent, session_id);
CREATE VIEW IF NOT EXISTS current_decision AS SELECT * FROM decision WHERE obsolete_at IS NULL;
CREATE TABLE IF NOT EXISTS decision_file(decision_id TEXT NOT NULL, path TEXT NOT NULL, PRIMARY KEY(decision_id, path));
CREATE INDEX IF NOT EXISTS decision_file_path ON decision_file(path);
CREATE TABLE IF NOT EXISTS capture_attempt(
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT NOT NULL, session_id TEXT NOT NULL, repository TEXT NOT NULL DEFAULT '',
  backend TEXT NOT NULL, model TEXT NOT NULL, mode TEXT NOT NULL, started_at INTEGER NOT NULL, settled_at INTEGER,
  state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', reserved_usd REAL NOT NULL, cost_usd REAL,
  input_tokens INTEGER NOT NULL DEFAULT 0, cached_input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  rejections INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS capture_attempt_session ON capture_attempt(agent, session_id, started_at);
CREATE INDEX IF NOT EXISTS capture_attempt_started ON capture_attempt(started_at);`

// Store is decisions.sqlite: the sessions' coverage and head extraction, the
// decision log and the ledger of model calls.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// OpenOptions choose how Open opens the store.
type OpenOptions struct {
	// AbandonPending settles the attempts a previous run left pending as
	// abandoned: their reservation counts as spent. Only the daemon, the one
	// writer that makes calls unattended, sets it.
	AbandonPending bool
	// ReadOnly opens an existing store without writing to it, and an empty
	// in-memory one when there is none, for a dry run.
	ReadOnly bool
}

// Open creates or opens the private store at path, in a 0700 directory.
func Open(path string, o OpenOptions) (*Store, error) {
	if o.ReadOnly {
		return openReadOnly(path)
	}
	if err := privatefs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = file.Chmod(0o600)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	db, err := openDB(path, false)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	_, err = db.Exec(schema + featureSchema)
	if err == nil {
		err = migrate(context.Background(), db)
	}
	if err == nil && o.AbandonPending {
		_, err = db.Exec(`UPDATE capture_attempt SET state=?, settled_at=?, reason='daemon_stopped' WHERE state=?`, AttemptAbandoned, s.now().UnixMilli(), AttemptPending)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// openDB opens the database file at path, read-only when asked, waiting for
// a writer's lock rather than failing.
func openDB(path string, readOnly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := "_pragma=busy_timeout(10000)"
	if readOnly {
		query += "&mode=ro"
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: uriPath}).String()+"?"+query)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// openReadOnly opens an existing store read-only, or an empty one in memory
// when there is none.
func openReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err == nil {
		db, err := openDB(path, true)
		if err != nil {
			return nil, err
		}
		return &Store{db: db, now: time.Now}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(schema + featureSchema)
	if err == nil {
		err = migrate(context.Background(), db)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Session is a captured session's row: its coverage and head extraction.
type Session struct {
	Agent, ID                string
	Repository, Branch, Path string
	Fingerprint              string
	ContentHash, PrefixHash  string
	PromptVersion            string
	Backend, Model           string
	// Decisions are the head extraction's, with IDs; nil when none was made.
	Decisions  []decisions.Decision
	Coverage   string
	SkipReason string
	Failures   int
	UpdatedAt  time.Time
}

// Session reads the session's row; false when it has none.
func (s *Store) Session(ctx context.Context, agent, id string) (Session, bool, error) {
	row := Session{Agent: agent, ID: id}
	var head sql.NullString
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT repository, branch, transcript_path, fingerprint, content_hash, prefix_hash, prompt_version,
backend, model, decisions_json, coverage, skip_reason, failures, updated_at FROM capture_session WHERE agent=? AND session_id=?`, agent, id).
		Scan(&row.Repository, &row.Branch, &row.Path, &row.Fingerprint, &row.ContentHash, &row.PrefixHash, &row.PromptVersion,
			&row.Backend, &row.Model, &head, &row.Coverage, &row.SkipReason, &row.Failures, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	row.UpdatedAt = time.UnixMilli(updated)
	if head.Valid {
		if err := json.Unmarshal([]byte(head.String), &row.Decisions); err != nil {
			return Session{}, false, fmt.Errorf("decoding the session's head decisions: %w", err)
		}
	}
	return row, true, nil
}

// Meta is where a session's transcript is and what it belongs to.
type Meta struct {
	Agent, ID          string
	Repository, Branch string
	Path               string
	Fingerprint        string
}

// upsertMeta makes sure the session has a row and records its meta.
func upsertMeta(ctx context.Context, tx *sql.Tx, m Meta, coverage string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO capture_session(agent, session_id, repository, branch, transcript_path, fingerprint, coverage, updated_at)
VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(agent, session_id) DO UPDATE SET repository=excluded.repository, branch=excluded.branch,
transcript_path=excluded.transcript_path, fingerprint=excluded.fingerprint, coverage=excluded.coverage, updated_at=excluded.updated_at`,
		m.Agent, m.ID, m.Repository, m.Branch, m.Path, m.Fingerprint, coverage, now.UnixMilli())
	return err
}

func (s *Store) inTx(ctx context.Context, work func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := work(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Skip records that the session, as fingerprinted, was not extracted and why.
// Its head extraction and decisions stay as they were.
func (s *Store) Skip(ctx context.Context, m Meta, reason string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertMeta(ctx, tx, m, CoverageSkipped, s.now()); err != nil {
			return err
		}
		// A session marked failed gets its tries again once it changes.
		_, err := tx.ExecContext(ctx, `UPDATE capture_session SET skip_reason=?1, failures=CASE WHEN ?1=?4 THEN 0 ELSE failures END
WHERE agent=?2 AND session_id=?3`, reason, m.Agent, m.ID, ReasonFailed)
		return err
	})
}

// Covered records that the head extraction still covers the session as
// fingerprinted (a cache hit): its rendered transcript did not change.
func (s *Store) Covered(ctx context.Context, m Meta) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertMeta(ctx, tx, m, CoverageExtracted, s.now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE capture_session SET skip_reason='', failures=0 WHERE agent=? AND session_id=?`, m.Agent, m.ID)
		return err
	})
}

// Failed counts a failed extraction of the session and returns how many
// followed its last success.
func (s *Store) Failed(ctx context.Context, m Meta) (int, error) {
	failures := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO capture_session(agent, session_id, repository, branch, transcript_path, coverage, updated_at)
VALUES (?,?,?,?,?,?,?) ON CONFLICT(agent, session_id) DO NOTHING`, m.Agent, m.ID, m.Repository, m.Branch, m.Path, CoverageSkipped, s.now().UnixMilli())
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `UPDATE capture_session SET failures=failures+1 WHERE agent=? AND session_id=? RETURNING failures`, m.Agent, m.ID).Scan(&failures)
	})
	return failures, err
}

// Extraction is one extraction to save as the session's head.
type Extraction struct {
	Meta
	ContentHash, PrefixHash string
	PromptVersion           string
	Backend, Model          string
	// Decisions all have IDs (decisions.Identified).
	Decisions []decisions.Decision
	Dropped   []decisions.Dropped
	Usage     decisions.Usage
	// Cost is nil when unknown.
	Cost *float64
	// TurnTimes are the times of the root session's turns, by ordinal.
	TurnTimes map[int]time.Time
}

// Save makes the extraction the session's head and its decisions the
// session's current ones, as the hosted decision log does: a decision is
// upserted by its ID, so it keeps the row and creation time it had; the
// session's current decisions missing from the new set become obsolete and
// are never deleted. A decision is dated by its first cited turn's time,
// else the date its ID had, else the time of the extraction.
func (s *Store) Save(ctx context.Context, e Extraction) error {
	now := s.now()
	if e.Decisions == nil {
		e.Decisions = []decisions.Decision{}
	}
	head, err := json.Marshal(e.Decisions)
	if err != nil {
		return err
	}
	dropped, err := json.Marshal(e.Dropped)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertMeta(ctx, tx, e.Meta, CoverageExtracted, now); err != nil {
			return err
		}
		var cost any
		if e.Cost != nil {
			cost = *e.Cost
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capture_session SET content_hash=?, prefix_hash=?, prompt_version=?, backend=?, model=?,
decisions_json=?, dropped_json=?, input_tokens=?, cached_input_tokens=?, output_tokens=?, cost_usd=?, failures=0, skip_reason='', extracted_at=?
WHERE agent=? AND session_id=?`, e.ContentHash, e.PrefixHash, e.PromptVersion, e.Backend, e.Model, string(head), string(dropped),
			e.Usage.InputTokens, e.Usage.CachedInputTokens, e.Usage.OutputTokens, cost, now.UnixMilli(), e.Agent, e.ID); err != nil {
			return err
		}
		previous := map[string]int64{}
		rows, err := tx.QueryContext(ctx, `SELECT decision_id, decided_at FROM decision WHERE agent=? AND session_id=?`, e.Agent, e.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var at int64
			if err := rows.Scan(&id, &at); err != nil {
				_ = rows.Close()
				return err
			}
			previous[id] = at
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		ids := make([]any, 0, len(e.Decisions)+3)
		ids = append(ids, now.UnixMilli(), e.Agent, e.ID)
		for _, d := range e.Decisions {
			ids = append(ids, d.ID)
		}
		obsolete := `UPDATE decision SET obsolete_at=? WHERE agent=? AND session_id=? AND obsolete_at IS NULL`
		if len(e.Decisions) > 0 {
			obsolete += ` AND decision_id NOT IN (?` + strings.Repeat(",?", len(e.Decisions)-1) + `)`
		}
		if _, err := tx.ExecContext(ctx, obsolete, ids...); err != nil {
			return err
		}
		for i, d := range e.Decisions {
			if d.ID == "" {
				return errors.New("a decision to save has no ID")
			}
			turns, err := json.Marshal(d.Turns)
			if err != nil {
				return err
			}
			first := 0
			if len(d.Turns) > 0 {
				first = d.Turns[0]
			}
			decided := now.UnixMilli()
			if at, ok := e.TurnTimes[first]; ok {
				decided = at.UnixMilli()
			} else if at, ok := previous[d.ID]; ok {
				decided = at
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO decision(decision_id, agent, session_id, repository, ordinal, title, summary, category, who,
trigger, rationale, rejected_alternative, evidence, superseded, turns_json, first_turn, decided_turn, decided_at, branch, created_at, obsolete_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL) ON CONFLICT(decision_id) DO UPDATE SET agent=excluded.agent, session_id=excluded.session_id,
repository=excluded.repository, ordinal=excluded.ordinal, title=excluded.title, summary=excluded.summary, category=excluded.category, who=excluded.who,
trigger=excluded.trigger, rationale=excluded.rationale, rejected_alternative=excluded.rejected_alternative, evidence=excluded.evidence,
superseded=excluded.superseded, turns_json=excluded.turns_json, first_turn=excluded.first_turn, decided_turn=excluded.decided_turn,
decided_at=excluded.decided_at, branch=excluded.branch, obsolete_at=NULL`,
				d.ID, e.Agent, e.ID, e.Repository, i, d.Title, d.Summary, string(d.Category), string(d.Who), string(d.Trigger), d.Rationale,
				d.RejectedAlternative, d.Evidence, d.Superseded, string(turns), first, d.DecidedTurn, decided, e.Branch, now.UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM decision_file WHERE decision_id=?`, d.ID); err != nil {
				return err
			}
			for _, file := range slices.Compact(slices.Sorted(slices.Values(d.Files))) {
				if _, err := tx.ExecContext(ctx, `INSERT INTO decision_file(decision_id, path) VALUES (?,?)`, d.ID, file); err != nil {
					return err
				}
			}
		}
		// The session's features read its decisions.
		version, err := liveVersion(ctx, tx, e.Repository, false, now)
		if err != nil || version == 0 {
			return err
		}
		marked, err := sessionFeatures(ctx, tx, version, e.Repository, []SessionKey{{Agent: e.Agent, ID: e.ID}})
		if err != nil {
			return err
		}
		return markFeatures(ctx, tx, marked, now)
	})
}

// Attempt is one model call in the ledger.
type Attempt struct {
	Agent, ID, Repository string
	Backend, Model        string
	// Mode is "full" or "extend".
	Mode     string
	Reserved float64
	// Stage is the call's stage, "" for capture; Item names what a feature
	// stage's call was about.
	Stage, Item string
}

// Begin records an attempt as pending before its call, with its reservation.
func (s *Store) Begin(ctx context.Context, a Attempt) (int64, error) {
	id, _, err := s.beginWithin(ctx, a, nil, nil, time.Time{})
	return id, err
}

// beginWithin is Begin when every stage's spend since day, the reservation
// included, stays within limit, and the session's within sessionLimit (each
// none when nil); false, recording nothing, when it would not. The checks
// and the insert are one statement, so two workers reserving at once cannot
// both pass a cap.
func (s *Store) beginWithin(ctx context.Context, a Attempt, limit, sessionLimit *float64, day time.Time) (int64, bool, error) {
	var id int64
	stage := a.Stage
	if stage == "" {
		stage = StageCapture
	}
	var item any
	if a.Item != "" {
		item = a.Item
	}
	var bound, sessionBound any
	if limit != nil {
		bound = *limit
	}
	if sessionLimit != nil {
		sessionBound = *sessionLimit
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO capture_attempt(agent, session_id, repository, backend, model, mode, started_at, state, reserved_usd, stage, item)
SELECT ?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11 WHERE (?12 IS NULL OR
  (SELECT COALESCE(SUM(COALESCE(cost_usd, reserved_usd)), 0) FROM capture_attempt WHERE started_at >= ?13) + ?9 <= ?12)
AND (?14 IS NULL OR (SELECT COALESCE(SUM(COALESCE(cost_usd, reserved_usd)), 0) FROM capture_attempt
  WHERE agent=?1 AND session_id=?2 AND started_at >= ?13) + ?9 <= ?14) RETURNING id`,
		a.Agent, a.ID, a.Repository, a.Backend, a.Model, a.Mode, s.now().UnixMilli(), AttemptPending, a.Reserved, stage, item, bound, day.UnixMilli(),
		sessionBound).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Settle records how an attempt ended. A nil cost is unknown, and the
// attempt's reservation counts as its spend.
func (s *Store) Settle(ctx context.Context, id int64, state, reason string, cost *float64, usage decisions.Usage) error {
	return s.settle(ctx, id, state, reason, cost, usage, 0)
}

// settle is Settle recording, too, how many replies the harness rejected
// against the schema.
func (s *Store) settle(ctx context.Context, id int64, state, reason string, cost *float64, usage decisions.Usage, rejections int) error {
	var value any
	if cost != nil {
		value = *cost
	}
	_, err := s.db.ExecContext(ctx, `UPDATE capture_attempt SET state=?, reason=?, cost_usd=?, settled_at=?, input_tokens=?, cached_input_tokens=?, output_tokens=?,
rejections=? WHERE id=?`, state, reason, value, s.now().UnixMilli(), usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, rejections, id)
	return err
}

// Stats are what the throttle and the caps read from the ledger.
type Stats struct {
	// Count and Spent are the session's attempts and spend since the day's
	// start; LastStart is its latest attempt and LastFail its latest failed
	// one, zero for none.
	Count     int
	Spent     float64
	LastStart time.Time
	LastFail  time.Time
	// AllSpent is every session's spend since the day's start.
	AllSpent float64
}

// Stats reads the ledger for the session since day. An attempt's spend is
// its cost, or its reservation when the cost is unknown.
func (s *Store) Stats(ctx context.Context, agent, id string, day time.Time) (Stats, error) {
	var st Stats
	var last, failed sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
  COUNT(*) FILTER (WHERE agent=?1 AND session_id=?2),
  COALESCE(SUM(COALESCE(cost_usd, reserved_usd)) FILTER (WHERE agent=?1 AND session_id=?2), 0),
  COALESCE(SUM(COALESCE(cost_usd, reserved_usd)), 0)
FROM capture_attempt WHERE started_at >= ?3`, agent, id, day.UnixMilli()).Scan(&st.Count, &st.Spent, &st.AllSpent)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT MAX(started_at), MAX(started_at) FILTER (WHERE state = ?3)
FROM capture_attempt WHERE agent=?1 AND session_id=?2`, agent, id, AttemptFailed).Scan(&last, &failed)
	if last.Valid {
		st.LastStart = time.UnixMilli(last.Int64)
	}
	if failed.Valid {
		st.LastFail = time.UnixMilli(failed.Int64)
	}
	return st, err
}

// Prune deletes settled attempts and obsolete decisions older than Retention.
func (s *Store) Prune(ctx context.Context) error {
	before := s.now().Add(-Retention).UnixMilli()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM capture_attempt WHERE state != ? AND started_at < ?`, AttemptPending, before); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM decision_file WHERE decision_id IN (SELECT decision_id FROM decision WHERE obsolete_at < ?)`, before); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM decision WHERE obsolete_at < ?`, before)
		return err
	})
}

// Decision is a current decision as the log keeps it.
type Decision struct {
	decisions.Decision
	Agent      string    `json:"agent"`
	SessionID  string    `json:"session_id"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch,omitempty"`
	DecidedAt  time.Time `json:"decided_at"`
}

// SessionKey names a session.
type SessionKey struct{ Agent, ID string }

// Current reads the current decisions of the sessions, in session and
// extraction order, with their files.
func (s *Store) Current(ctx context.Context, sessions ...SessionKey) ([]Decision, error) {
	var out []Decision
	for _, key := range sessions {
		rows, err := s.db.QueryContext(ctx, `SELECT decision_id, repository, title, summary, category, who, trigger, rationale,
rejected_alternative, evidence, superseded, turns_json, decided_turn, decided_at, branch FROM current_decision
WHERE agent=? AND session_id=? ORDER BY ordinal`, key.Agent, key.ID)
		if err != nil {
			return nil, err
		}
		start := len(out)
		for rows.Next() {
			d := Decision{Agent: key.Agent, SessionID: key.ID}
			var turns string
			var decided int64
			if err := rows.Scan(&d.ID, &d.Repository, &d.Title, &d.Summary, &d.Category, &d.Who, &d.Trigger, &d.Rationale,
				&d.RejectedAlternative, &d.Evidence, &d.Superseded, &turns, &d.DecidedTurn, &decided, &d.Branch); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if err := json.Unmarshal([]byte(turns), &d.Turns); err != nil {
				_ = rows.Close()
				return nil, err
			}
			d.DecidedAt = time.UnixMilli(decided)
			d.Files = []string{}
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for i := start; i < len(out); i++ {
			files, err := s.db.QueryContext(ctx, `SELECT path FROM decision_file WHERE decision_id=? ORDER BY path`, out[i].ID)
			if err != nil {
				return nil, err
			}
			for files.Next() {
				var path string
				if err := files.Scan(&path); err != nil {
					_ = files.Close()
					return nil, err
				}
				out[i].Files = append(out[i].Files, path)
			}
			if err := files.Err(); err != nil {
				_ = files.Close()
				return nil, err
			}
			if err := files.Close(); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Coverage reads each session's coverage and skip reason; a session without
// a row is left out.
func (s *Store) Coverage(ctx context.Context, sessions ...SessionKey) (map[SessionKey][2]string, error) {
	out := map[SessionKey][2]string{}
	for _, key := range sessions {
		var coverage, reason string
		err := s.db.QueryRowContext(ctx, `SELECT coverage, skip_reason FROM capture_session WHERE agent=? AND session_id=?`, key.Agent, key.ID).Scan(&coverage, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[key] = [2]string{coverage, reason}
	}
	return out, nil
}
