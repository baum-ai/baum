package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
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
	before, statErr := os.Stat(s.path())
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE runs SET expires=? WHERE id=? AND state='generating'`, deadline.UnixNano(), id)
	if err == nil && statErr == nil {
		if after, e := os.Stat(s.path()); e == nil {
			h := heartbeat{before: stampOf(before), after: stampOf(after)}
			// Renewals in a row chain: the file is still as the last one
			// left it, so what a watcher saw before that one still stands.
			if v, ok := heartbeats.Load(s.path()); ok && v.(heartbeat).after == h.before {
				h.before = v.(heartbeat).before
			}
			heartbeats.Store(s.path(), h)
		}
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}

// FileStamp is the size and modification time (nanoseconds) of a file.
type FileStamp struct{ Size, ModTime int64 }

func stampOf(info os.FileInfo) FileStamp {
	return FileStamp{info.Size(), info.ModTime().UnixNano()}
}

type heartbeat struct{ before, after FileStamp }

// heartbeats holds, per store file, the stamps around this process's latest
// HeartbeatRun write.
var heartbeats sync.Map

// SeenThroughHeartbeats maps the stamp of the store file at path to what it
// was before this process's latest lease heartbeat wrote it, when the file
// is exactly as that heartbeat left it. A lease renewal changes nothing a
// reader of the store sees, so a watcher that compares stamps can look
// through it; any other write, before or after the heartbeat, leaves a
// different stamp and is not hidden. A write by another process landing
// between the heartbeat's two stats would be folded in; the review index's
// MaxAge bounds that.
func SeenThroughHeartbeats(path string, stamp FileStamp) FileStamp {
	if v, ok := heartbeats.Load(path); ok {
		if h := v.(heartbeat); h.after == stamp {
			return h.before
		}
	}
	return stamp
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

// The projection of a latest document, shared by Snapshot's fallback, the
// write-time maintenance and the backfill so all three agree exactly.
const (
	titleSQL   = `COALESCE(json_extract({doc},'$.summary.title'),'')`
	prStateSQL = `COALESCE(json_extract({doc},'$.review.pr_state'),'')`
	riskSQL    = `json_extract({doc},'$.summary.risk_score')`
	minutesSQL = `CASE WHEN json_type({doc},'$.summary.review_minutes') IN ('integer','real') AND json_extract({doc},'$.summary.review_minutes') BETWEEN 1 AND 240 AND CAST(json_extract({doc},'$.summary.review_minutes') AS INTEGER)=json_extract({doc},'$.summary.review_minutes') THEN CAST(json_extract({doc},'$.summary.review_minutes') AS INTEGER) END`
)

func extract(doc string) string {
	return strings.NewReplacer("{doc}", doc).Replace(titleSQL + `,` + prStateSQL + `,` + riskSQL + `,` + minutesSQL)
}

// fromSummary reads a column of a matching summaries row, extracting from the
// document only for a row without one. SQLite evaluates CASE lazily.
func fromSummary(column, expr string) string {
	return `CASE WHEN s.pr IS NULL THEN ` + strings.ReplaceAll(expr, "{doc}", "v.document") + ` ELSE ` + column + ` END`
}

const summariesSchema = `CREATE TABLE IF NOT EXISTS summaries (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, document_length INTEGER NOT NULL, title, pr_state, risk_score, review_minutes INTEGER, PRIMARY KEY(repository,pr)) WITHOUT ROWID`

const summaryColumns = `INSERT OR REPLACE INTO summaries(repository,pr,version,document_length,title,pr_state,risk_score,review_minutes) `

// summaryTriggers keep the projection current for every writer of versions, in
// the writer's own transaction: older binaries too, once the triggers exist. A
// document SQLite cannot parse (nested deeper than its limit) drops the row so
// Snapshot falls back, rather than failing the write.
//
// Trigger names carry a hash of every body and of the table, so a binary that
// changes the extraction finds the old triggers missing and replaces them.
func summaryTriggers() []trigger {
	latest := `NEW.version=(SELECT MAX(version) FROM versions WHERE repository=NEW.repository AND pr=NEW.pr)`
	upsert := summaryColumns + `SELECT NEW.repository,NEW.pr,NEW.version,length(NEW.document),` + extract("NEW.document") + `;`
	drop := `DELETE FROM summaries WHERE repository=NEW.repository AND pr=NEW.pr;`
	var out []trigger
	for _, event := range []struct{ name, on string }{{"insert", "INSERT"}, {"update", "UPDATE OF document"}} {
		out = append(out,
			trigger{"summaries_" + event.name, ` AFTER ` + event.on + ` ON versions WHEN ` + latest + ` AND json_valid(NEW.document) BEGIN ` + upsert + ` END`},
			trigger{"summaries_" + event.name + "_invalid", ` AFTER ` + event.on + ` ON versions WHEN ` + latest + ` AND NOT json_valid(NEW.document) BEGIN ` + drop + ` END`})
	}
	out = append(out, trigger{"summaries_delete", ` AFTER DELETE ON versions BEGIN DELETE FROM summaries WHERE repository=OLD.repository AND pr=OLD.pr; ` +
		summaryColumns + `SELECT v.repository,v.pr,v.version,length(v.document),` + extract("v.document") + ` FROM versions v WHERE v.repository=OLD.repository AND v.pr=OLD.pr AND v.version=(SELECT MAX(version) FROM versions WHERE repository=OLD.repository AND pr=OLD.pr) AND json_valid(v.document); END`})
	h := sha256.New()
	h.Write([]byte(summariesSchema))
	for _, t := range out {
		h.Write([]byte(t.name + t.body))
	}
	suffix := "_" + hex.EncodeToString(h.Sum(nil))[:8]
	for i := range out {
		out[i].name += suffix
	}
	return out
}

type trigger struct{ name, body string }

// immediate runs fn in a transaction that takes the writer lock up front.
func immediate(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err = fn(conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func summaryTriggerNames(ctx context.Context, q querier) ([]string, error) {
	return triggerNames(ctx, q, "summaries_")
}

func triggerNames(ctx context.Context, q querier, prefix string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND substr(name,1,?)=? ORDER BY name`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// setupSummaries makes the table and triggers current, and projects stale rows.
// A store whose triggers are exactly the current ones is only read unless a row
// is stale. Otherwise one transaction creates the table if missing, replaces
// the triggers, drops every row (the extraction may have changed) and
// backfills.
func setupSummaries(ctx context.Context, db *sql.DB) error {
	triggers := summaryTriggers()
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='summaries'`).Scan(&tables); err != nil {
		return err
	}
	have, err := summaryTriggerNames(ctx, db)
	if err != nil {
		return err
	}
	current := tables == 1 && len(have) == len(triggers)
	for _, t := range triggers {
		current = current && slices.Contains(have, t.name)
	}
	if current {
		return backfill(ctx, db)
	}
	return immediate(ctx, db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, summariesSchema); err != nil {
			return err
		}
		have, err := summaryTriggerNames(ctx, conn)
		if err != nil {
			return err
		}
		for _, name := range have {
			if _, err := conn.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
				return err
			}
		}
		for _, t := range triggers {
			if _, err := conn.ExecContext(ctx, `CREATE TRIGGER "`+t.name+`"`+t.body); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM summaries`); err != nil {
			return err
		}
		return projectStale(ctx, conn)
	})
}

// staleLatest selects every latest document without a current summary: a store
// written before summaries existed, or by a binary that does not maintain
// them. SQLite parses a document only for such a row (CASE is lazy), so a store
// that is already current costs no document reads.
const staleLatest = ` FROM versions v JOIN (SELECT repository,pr,MAX(version) latest FROM versions GROUP BY repository,pr) m ON v.repository=m.repository AND v.pr=m.pr AND v.version=m.latest LEFT JOIN summaries s ON s.repository=v.repository AND s.pr=v.pr WHERE CASE WHEN s.pr IS NULL OR s.version<>v.version OR s.document_length<>length(v.document) THEN json_valid(v.document) ELSE 0 END`

var projectStaleSQL = summaryColumns + `SELECT v.repository,v.pr,v.version,length(v.document),` + extract("v.document") + staleLatest

func projectStale(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, projectStaleSQL)
	return err
}

// backfill projects the stale rows. A read-only check comes first so a store
// that is already current is not written and its writer lock not taken.
func backfill(ctx context.Context, db *sql.DB) error {
	var stale bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+staleLatest+`)`).Scan(&stale); err != nil || !stale {
		return err
	}
	return immediate(ctx, db, func(conn *sql.Conn) error { return projectStale(ctx, conn) })
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
	// Read old stores without migrating them: summaries is used where present and current.
	var projected int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='summaries'`).Scan(&projected); err != nil {
		return nil, err
	}
	table := `(SELECT NULL repository,NULL pr,NULL version,NULL document_length,NULL title,NULL pr_state,NULL risk_score,NULL review_minutes WHERE 0)`
	if projected != 0 {
		table = "summaries"
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.repository,v.pr,v.metadata,`+
		fromSummary("s.title", titleSQL)+`,`+fromSummary("s.pr_state", prStateSQL)+`,`+fromSummary("s.risk_score", riskSQL)+`,`+fromSummary("s.review_minutes", minutesSQL)+
		`,m.n FROM versions v JOIN (SELECT repository,pr,MAX(version) latest,COUNT(*) n FROM versions GROUP BY repository,pr) m ON v.repository=m.repository AND v.pr=m.pr AND v.version=m.latest LEFT JOIN `+table+` s ON s.repository=v.repository AND s.pr=v.pr AND s.version=v.version AND s.document_length=length(v.document) ORDER BY v.repository,v.pr`)
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
	// Opening for writes creates runs.
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
