package tilelibrary

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// MaxUsedIn bounds the repositories a type remembers being used in: the
// most recent ones are kept.
const MaxUsedIn = 50

// RecordUse records that a version of a pull request in repository was
// stored with a ready fill of each named type: their used_at becomes now
// and used_in names the repository at now, keeping the MaxUsedIn most
// recent repositories. A name the library does not hold, a deleted type's
// included, is left alone.
func (s *Store) RecordUse(ctx context.Context, repository string, names []string) error {
	if len(names) == 0 || repository == "" {
		return nil
	}
	now := s.now().Format(time.RFC3339Nano)
	return s.write(ctx, func(conn querierExec) error {
		for _, name := range names {
			var usedIn sql.NullString
			err := conn.QueryRowContext(ctx, `SELECT used_in FROM tile_types WHERE name = ? AND deleted IS NULL`, name).Scan(&usedIn)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			where := map[string]string{}
			if usedIn.Valid {
				_ = json.Unmarshal([]byte(usedIn.String), &where)
			}
			where[repository] = now
			encoded, err := json.Marshal(newest(where, MaxUsedIn))
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `UPDATE tile_types SET used_at = ?, used_in = ? WHERE name = ?`, now, string(encoded), name); err != nil {
				return err
			}
		}
		return nil
	})
}

// newest keeps the n repositories used most recently.
func newest(where map[string]string, n int) map[string]string {
	if len(where) <= n {
		return where
	}
	keys := make([]string, 0, len(where))
	for k := range where {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := stamp(where[b]).Compare(stamp(where[a])); c != 0 {
			return c
		}
		if a < b {
			return -1
		}
		return 1
	})
	out := make(map[string]string, n)
	for _, k := range keys[:n] {
		out[k] = where[k]
	}
	return out
}
