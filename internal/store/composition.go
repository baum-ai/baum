package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// A version may carry a composition beside its review document: how its
// review page presents it, a review view composition (baum.composition/2,
// view.Composition). Versions stored before it may carry a tile grid layout
// (baum.composition/1), which nothing reads any more. It sits in the
// nullable versions.composition column, written with the version's row and
// never changed after. A NULL column, a store without the column and a
// composition no reader can use all mean the same: a version without one.
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

// storableComposition returns the composition's bytes to store with a
// document, or nil to store none. Only a review view composition is stored,
// checked against the document (view.Normalize), which drops what it cannot
// take item by item, never the whole composition, with the caller's
// generator (Version.CompositionGenerator) in place of any in its body.
//
// A composition problem never costs the review its place in the history: a
// composition that is too large, is not a baum.composition/2 or whose
// document cannot be read is not stored, and a generator the checks refuse
// is left off; each is counted (store.compositions_dropped,
// store.composition_generators_dropped) and logged without content. One
// that chooses nothing is stored as none.
func storableComposition(document, raw []byte, generator *tiles.Generator) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	drop := func(reason string) []byte {
		workstats.Inc("store.compositions_dropped")
		slog.Debug("a composition was not stored", "reason", reason)
		return nil
	}
	if len(raw) > tiles.MaxStoredComposition {
		return drop("too large")
	}
	if !view.IsComposition(raw) {
		return drop("not a review view composition")
	}
	out, reason := storableView(document, raw, generator)
	if reason != "" {
		return drop(reason)
	}
	return out
}

// storableView checks a review view composition against the document and
// returns the bytes to store, nil for none, or why it is not stored.
func storableView(document, raw []byte, generator *tiles.Generator) ([]byte, string) {
	given, err := view.Parse(raw)
	if err != nil {
		return nil, "not a composition"
	}
	given.Generator = nil
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return nil, "document not readable"
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		return nil, "document not readable"
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		return nil, "document does not convert"
	}
	n, _ := view.Normalize(r, doc, given)
	if generator != nil {
		if checked, err := generator.Checked(); err != nil {
			workstats.Inc("store.composition_generators_dropped")
			slog.Debug("a composition's generator was not stored")
		} else {
			n.Generator = &checked
		}
	}
	if n.Generator == nil && viewEmpty(&n) {
		return nil, ""
	}
	out, err := json.Marshal(n)
	if err != nil || len(out) > tiles.MaxStoredComposition {
		return nil, "too large after normalising"
	}
	return out, ""
}

// viewEmpty reports whether a review view composition chooses nothing.
func viewEmpty(c *view.Composition) bool {
	o := c.Overview
	return o.RiskSummary == "" && o.Verdict == nil && len(o.Screenshots) == 0 && o.Diagram == nil && len(o.Intent) == 0 &&
		len(c.Sections) == 0 && len(c.Diagrams) == 0 && len(c.Library) == 0 && len(c.Authored) == 0
}

// ParseComposition parses a version's stored review view composition
// (Version.Composition) for a reader, such as the tile analysis reading the
// previous version's library tiles. It returns nil for none (NULL or empty)
// and for one that cannot be read or is not a baum.composition/2.
func ParseComposition(raw []byte) *view.Composition {
	if len(raw) == 0 || len(raw) > tiles.MaxStoredComposition {
		return nil
	}
	c, err := view.Parse(raw)
	if err != nil {
		return nil
	}
	return c
}
