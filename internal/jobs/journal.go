package jobs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/baum-ai/baum/internal/appdb"
)

// maxJobJSON bounds one job's journal record. A save that would write a
// larger one is refused; the history as a whole has no limit.
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

// writeJournal writes the given jobs and deletes the rows of gone, which are
// jobs dropped from memory before any write kept them (a rolled-back
// submission), in one transaction.
func writeJournal(path string, jobs []Job, gone []string) (err error) {
	encoded := make([][]byte, len(jobs))
	for i, j := range jobs {
		if encoded[i], err = json.Marshal(j); err != nil {
			return err
		}
		if len(encoded[i]) > maxJobJSON {
			return fmt.Errorf("job %s is %d bytes, more than the journal keeps for one job (%d)", j.ID, len(encoded[i]), maxJobJSON)
		}
	}
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
	for i, j := range jobs {
		if err = writeJob(tx, j, encoded[i]); err != nil {
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
