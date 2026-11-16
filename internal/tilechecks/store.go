package tilechecks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
)

// keptChecks is how many (digest, data) verdicts the store keeps, newest
// first.
var keptChecks = 10_000

// store keeps the verdict of each (definition digest, data) pair a check
// drew, in the application database's tile_checked table, so the page's
// first-show preflight runs once per pair.
type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := appdb.Open(path, false)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tile_checked(digest TEXT NOT NULL, data_sha TEXT NOT NULL, verdict TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(digest, data_sha));
CREATE INDEX IF NOT EXISTS tile_checked_at ON tile_checked(at);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// record keeps a pair's verdict, replacing an earlier one, and drops the
// oldest rows past keptChecks.
func (s *store) record(ctx context.Context, digest, dataSHA, verdict string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tile_checked(digest, data_sha, verdict, at) VALUES(?, ?, ?, ?)
ON CONFLICT(digest, data_sha) DO UPDATE SET verdict=excluded.verdict, at=excluded.at`, digest, dataSHA, verdict, at.UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM tile_checked WHERE rowid IN (SELECT rowid FROM tile_checked ORDER BY at DESC, rowid DESC LIMIT -1 OFFSET ?)`, keptChecks)
	return err
}

// lookup returns a pair's verdict, if one is kept.
func (s *store) lookup(ctx context.Context, digest, dataSHA string) (string, bool, error) {
	var verdict string
	err := s.db.QueryRowContext(ctx, `SELECT verdict FROM tile_checked WHERE digest=? AND data_sha=?`, digest, dataSHA).Scan(&verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return verdict, err == nil, err
}

func (s *store) close() error { return s.db.Close() }

// DataSHA names a tile's data by value: the lowercase hex SHA-256 of the
// data in a canonical form, with object members sorted, strings as JSON
// writes them without escaping HTML, and each number as the exact rational
// it spells (so 1, 1.0 and 1e0 are one value, and integers beyond a
// double's precision stay apart). The page sends data it parsed, the tools
// data an agent wrote: the same value has the same name whichever way it
// was spelled.
func DataSHA(data json.RawMessage) (string, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return "", err
	}
	if _, err := d.Token(); err != io.EOF {
		return "", errors.New("tilechecks: more than one JSON value")
	}
	h := sha256.New()
	if err := canonicalData(h, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalData(w io.Writer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		_, _ = io.WriteString(w, "{")
		for i, key := range slices.Sorted(maps.Keys(v)) {
			if i > 0 {
				_, _ = io.WriteString(w, ",")
			}
			if err := canonicalData(w, key); err != nil {
				return err
			}
			_, _ = io.WriteString(w, ":")
			if err := canonicalData(w, v[key]); err != nil {
				return err
			}
		}
		_, _ = io.WriteString(w, "}")
	case []any:
		_, _ = io.WriteString(w, "[")
		for i, item := range v {
			if i > 0 {
				_, _ = io.WriteString(w, ",")
			}
			if err := canonicalData(w, item); err != nil {
				return err
			}
		}
		_, _ = io.WriteString(w, "]")
	case json.Number:
		// An exponent this large would make the rational enormous: such a
		// number is named by its spelling.
		if _, exponent, found := strings.Cut(strings.ToLower(string(v)), "e"); found && len(strings.TrimLeft(exponent, "+-0")) > 4 {
			_, _ = io.WriteString(w, "~"+string(v))
			return nil
		}
		r, ok := new(big.Rat).SetString(string(v))
		if !ok {
			return fmt.Errorf("tilechecks: %q is not a number", string(v))
		}
		// A rational is not a JSON number, so no string can spell it.
		_, _ = io.WriteString(w, r.RatString())
	case string:
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, _ = w.Write(b)
	}
	return nil
}
