package jobs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/baum-ai/baum/internal/appdb"
)

// maxJobJSON bounds one job's journal record; the history as a whole has no
// limit. A larger job is journaled reduced (encodeJob), or not at all.
const maxJobJSON = 1 << 20

// The journal keeps one row per job. seq is its creation order, which the
// queue relies on (oldest first); archived is reserved for pruning.
const journalSchema = `CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, seq INTEGER NOT NULL UNIQUE, created INTEGER NOT NULL, updated INTEGER NOT NULL, state TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, job BLOB NOT NULL)`

const upsertJob = `INSERT INTO jobs(id, seq, created, updated, state, job) VALUES(?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM jobs), ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET created=excluded.created, updated=excluded.updated, state=excluded.state, job=excluded.job`

// loadJournal creates the journal table, moves a journal an older baum kept
// as the state/jobs document into it, and returns the jobs oldest first. The
// move and the document's removal are one transaction: a failure leaves the
// document as it was.
func loadJournal(path string) (jobs []Job, err error) {
	db, err := appdb.Open(path, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(journalSchema); err != nil {
		return nil, err
	}
	var document []byte
	err = tx.QueryRow(`SELECT value FROM app_documents WHERE bucket='state' AND key='jobs'`).Scan(&document)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return nil, err
	default:
		var old []Job
		if err = json.Unmarshal(document, &old); err != nil {
			return nil, fmt.Errorf("job journal document: %w", err)
		}
		for _, j := range old {
			var raw []byte
			if raw, err = json.Marshal(j); err != nil {
				return nil, err
			}
			if err = writeJob(tx, j, raw); err != nil {
				return nil, fmt.Errorf("moving job %s to the journal table: %w", j.ID, err)
			}
		}
		if _, err = tx.Exec(`DELETE FROM app_documents WHERE bucket='state' AND key='jobs'`); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(`SELECT job FROM jobs ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var j Job
		if err = json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("job journal row: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return jobs, tx.Commit()
}

// writeJob inserts a job, encoded as raw, at the end of the journal, or
// replaces its row in place.
func writeJob(tx *sql.Tx, j Job, raw []byte) error {
	_, err := tx.Exec(upsertJob, j.ID, j.CreatedAt.UnixMilli(), j.UpdatedAt.UnixMilli(), j.State, raw)
	return err
}

// errJobTooLarge is a job whose record, even reduced, is over maxJobJSON.
var errJobTooLarge = errors.New("job too large for the journal")

// encodeJob returns the record of j, or of a reduced copy of it (reduced is
// then true) when j's own is over maxJobJSON: the reduced copy keeps the
// job's identity, state, reason, times, counts and cost and drops its
// variable-size text and lists. It is errJobTooLarge when even that is over.
func encodeJob(j Job) (raw []byte, reduced bool, err error) {
	if raw, err = json.Marshal(j); err != nil || len(raw) <= maxJobJSON {
		return raw, false, err
	}
	j.Summary, j.Detail, j.ThreadOutcomes, j.FailedSections, j.FixThreads = "", "", nil, nil, nil
	if j.Accounting != nil {
		accounting := *j.Accounting
		accounting.Attempts = nil
		j.Accounting = &accounting
	}
	if raw, err = json.Marshal(j); err != nil {
		return nil, true, err
	}
	if len(raw) > maxJobJSON {
		return nil, true, fmt.Errorf("%w: %d bytes over %d", errJobTooLarge, len(raw), maxJobJSON)
	}
	return raw, true, nil
}

// journalRow is a job and the record writeJournal stores for it.
type journalRow struct {
	job Job
	raw []byte
}

// writeJournal writes the given rows and deletes the rows of gone, which are
// jobs dropped from memory before any write kept them (a rolled-back
// submission), in one transaction.
func writeJournal(path string, rows []journalRow, gone []string) (err error) {
	db, err := appdb.Open(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, row := range rows {
		if err = writeJob(tx, row.job, row.raw); err != nil {
			return err
		}
	}
	for _, id := range gone {
		if _, err = tx.Exec(`DELETE FROM jobs WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
