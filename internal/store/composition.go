package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
)

// A version may carry a composition (baum.composition/1, tiles.Composition)
// beside its review document: how its tiles are laid out. It sits in the
// nullable versions.composition column, written with the version's row and
// never changed after. A NULL column, a store without the column and a
// composition no reader can use all mean the same: the converted document's
// own layout.
//
// The column is added by an ALTER TABLE on a writable open, which is additive
// and needs no copy. A store that cannot be altered (read-only, held by
// another writer, or read by an older build) keeps working: reads select NULL
// in its place and writes leave the composition out. Older builds name their
// columns in every statement, so they ignore the column and write NULL.

const compositionColumn = "composition"

// columnQuerier is what a handle can query to find out whether versions has
// the composition column.
type columnQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// hasComposition reports whether versions has the composition column. It is
// asked per read and write rather than remembered, since a writable open of
// the same store, here or in another process, may add it at any time.
func hasComposition(ctx context.Context, q columnQuerier) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('versions') WHERE name=?`, compositionColumn).Scan(&n)
	return n > 0, err
}

// setupComposition adds the column to a store that lacks it.
func setupComposition(ctx context.Context, db *sql.DB) error {
	if has, err := hasComposition(ctx, db); err != nil || has {
		return err
	}
	_, err := db.ExecContext(ctx, `ALTER TABLE versions ADD COLUMN `+compositionColumn+` TEXT`)
	if err != nil {
		// Another process may have added it between the check and the ALTER.
		if has, again := hasComposition(ctx, db); again == nil && has {
			return nil
		}
	}
	return err
}

// compositionSelect is the select expression for a version's composition: the
// column, or NULL in a store that lacks it.
func compositionSelect(has bool) string {
	if has {
		return `v.` + compositionColumn
	}
	return `NULL`
}

// ErrComposition reports a composition Put cannot store because it is not a
// baum.composition/1 document.
var ErrComposition = errors.New("invalid composition")

// storableComposition returns the composition's bytes to store with a
// document, or nil to store none. Only a normalised composition is stored: it
// is normalised against the tile document converted from the document, with
// the host's own parts (the generator and the hidden list) kept as given. A
// composition that is not baum.composition/1, or whose generator is not one
// the schema accepts, is an error; one that chooses nothing, and one the
// document cannot take (it does not convert to tiles), is not stored, so a
// layout never costs a review its place in the history.
func storableComposition(document, raw []byte, generator *tiles.Generator) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	given, err := tiles.ParseComposition(raw)
	if err != nil {
		return nil, ErrComposition
	}
	if given.Empty() && generator == nil {
		return nil, nil
	}
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		workstats.Inc("store.compositions_dropped")
		return nil, nil
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		workstats.Inc("store.compositions_dropped")
		return nil, nil
	}
	n, report := tiles.NormalizeComposition(doc, given)
	if report.Counts()[tiles.FixUnusable] > 0 {
		workstats.Inc("store.compositions_dropped")
		return nil, nil
	}
	if generator != nil {
		if err := n.SetGenerator(*generator); err != nil {
			return nil, ErrComposition
		}
	}
	if n.Empty() && n.Generator == nil {
		return nil, nil
	}
	out, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseComposition parses a version's stored composition (Version.Composition)
// for a reader, such as the compose stage reading the previous version's. It
// returns nil for none (NULL or empty) and for one that cannot be read.
func ParseComposition(raw []byte) *tiles.Composition {
	if len(raw) == 0 {
		return nil
	}
	c, err := tiles.ParseComposition(raw)
	if err != nil {
		return nil
	}
	return c
}
