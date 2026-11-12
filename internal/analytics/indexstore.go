package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/baum-ai/baum/internal/appdb"
)

// The index keeps one row per session file, so a save writes only the
// entries that changed. analytics_index is its one meta row: the index
// version and the price list thresholds the entries were summed with. Rows
// count only while it matches the running build's.
const indexSchema = `CREATE TABLE IF NOT EXISTS analytics_sessions(path TEXT PRIMARY KEY, entry BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS analytics_index(id INTEGER PRIMARY KEY CHECK(id = 1), version INTEGER NOT NULL, bands TEXT NOT NULL)`

// legacyBucket and legacyKey name the document an older baum kept the whole
// index in, which loadIndex moves into the tables.
const (
	legacyBucket = "cache"
	legacyKey    = "analytics-index"
)

// indexFile is the legacy document.
type indexFile struct {
	Version int              `json:"version"`
	Bands   []int64          `json:"bands"`
	Entries map[string]Entry `json:"entries"`
}

// storedIndex is what loadIndex found.
type storedIndex struct {
	// valid is false when no meta row matches the running build's version
	// and thresholds; entries is then empty and the rows must be cleared.
	valid   bool
	entries map[string]Entry
	// unreadable lists rows that did not decode, for the next save to delete.
	unreadable []string
}

// indexBatch is one save: the rows of upsert written, those of remove
// deleted, and with reset every other row deleted and the meta row written
// first, all in one transaction.
type indexBatch struct {
	reset  bool
	bands  []int64
	upsert map[string][]byte
	remove []string
}

// loadIndex creates the index tables, moves a legacy document into them and
// reads the rows. The move and the document's removal are one transaction:
// a failure leaves the document as it was. A legacy document of another
// version or thresholds is deleted unread, as it would have been ignored.
func loadIndex(path string, bands []int64) (storedIndex, error) {
	db, release, err := appdb.Acquire(path, false)
	if err != nil {
		return storedIndex{}, err
	}
	err = appdb.Transact(context.Background(), db, func(conn *sql.Conn) error {
		return migrateIndex(conn, bands)
	})
	release()
	if err != nil {
		return storedIndex{}, err
	}
	return readIndex(path, bands)
}

func migrateIndex(conn *sql.Conn, bands []int64) error {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, indexSchema); err != nil {
		return err
	}
	var raw []byte
	err := conn.QueryRowContext(ctx, `SELECT value FROM app_documents WHERE bucket=? AND key=?`, legacyBucket, legacyKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var old indexFile
	if json.Unmarshal(raw, &old) == nil && old.Version == indexVersion && slices.Equal(old.Bands, bands) {
		// The document is the latest the index was written, so it replaces
		// rows a newer baum may have left before a downgrade.
		if err := resetIndex(conn, bands); err != nil {
			return err
		}
		for p, e := range old.Entries {
			encoded, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if err := upsertEntry(conn, p, encoded); err != nil {
				return fmt.Errorf("moving analytics entry to its row: %w", err)
			}
		}
	}
	_, err = conn.ExecContext(ctx, `DELETE FROM app_documents WHERE bucket=? AND key=?`, legacyBucket, legacyKey)
	return err
}

// readIndex reads the meta row and every entry in one read transaction.
func readIndex(path string, bands []int64) (storedIndex, error) {
	stored := storedIndex{entries: map[string]Entry{}}
	db, release, err := appdb.Acquire(path, true)
	if err != nil {
		return stored, err
	}
	defer release()
	tx, err := db.Begin()
	if err != nil {
		return stored, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var rawBands string
	err = tx.QueryRow(`SELECT version, bands FROM analytics_index WHERE id = 1`).Scan(&version, &rawBands)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, nil
	}
	if err != nil {
		return stored, err
	}
	var storedBands []int64
	if json.Unmarshal([]byte(rawBands), &storedBands) != nil || version != indexVersion || !slices.Equal(storedBands, bands) {
		return stored, nil
	}
	rows, err := tx.Query(`SELECT path, entry FROM analytics_sessions`)
	if err != nil {
		return stored, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p string
		var raw []byte
		if err := rows.Scan(&p, &raw); err != nil {
			return storedIndex{entries: map[string]Entry{}}, err
		}
		var e Entry
		if json.Unmarshal(raw, &e) != nil {
			stored.unreadable = append(stored.unreadable, p)
			continue
		}
		stored.entries[p] = e
	}
	if err := rows.Err(); err != nil {
		return storedIndex{entries: map[string]Entry{}}, err
	}
	stored.valid = true
	return stored, nil
}

// writeIndex applies one save in one transaction.
func writeIndex(path string, b indexBatch) error {
	db, release, err := appdb.Acquire(path, false)
	if err != nil {
		return err
	}
	defer release()
	ctx := context.Background()
	return appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if b.reset {
			if _, err := conn.ExecContext(ctx, indexSchema); err != nil {
				return err
			}
			if err := resetIndex(conn, b.bands); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM app_documents WHERE bucket=? AND key=?`, legacyBucket, legacyKey); err != nil {
				return err
			}
		}
		for p, raw := range b.upsert {
			if err := upsertEntry(conn, p, raw); err != nil {
				return err
			}
		}
		for _, p := range b.remove {
			if _, err := conn.ExecContext(ctx, `DELETE FROM analytics_sessions WHERE path = ?`, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// resetIndex deletes every row and writes the meta row for bands.
func resetIndex(conn *sql.Conn, bands []int64) error {
	ctx := context.Background()
	encoded, err := json.Marshal(bands)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM analytics_sessions`); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO analytics_index(id, version, bands) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET version=excluded.version, bands=excluded.bands`, indexVersion, string(encoded))
	return err
}

func upsertEntry(conn *sql.Conn, path string, raw []byte) error {
	_, err := conn.ExecContext(context.Background(), `INSERT INTO analytics_sessions(path, entry) VALUES(?, ?) ON CONFLICT(path) DO UPDATE SET entry=excluded.entry`, path, raw)
	return err
}
