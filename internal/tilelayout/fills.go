package tilelayout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A tile the person added (an add op of their overlay) that the version's
// generation did not fill shows the data the host holds for it: a fill,
// written for one version of the pull request by the session panel's agent
// (add_tile with data) or by a fill run, kept in tile_fills. The definition
// a fill was written against is kept in tile_definitions while an overlay or
// a fill names its digest.
const (
	fillsSchema = `CREATE TABLE IF NOT EXISTS tile_fills(
  pr TEXT NOT NULL,
  tile TEXT NOT NULL,
  version INTEGER NOT NULL,
  digest TEXT NOT NULL,
  data BLOB NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(pr, tile, version))`
	definitionsSchema = `CREATE TABLE IF NOT EXISTS tile_definitions(
  digest TEXT PRIMARY KEY,
  definition BLOB NOT NULL,
  at TEXT NOT NULL)`
)

// KeepFills is how many versions' fills are kept per added tile, the newest
// first: this version's and the one before, which a new version shows
// while it is filled.
const KeepFills = 2

// MaxFillBytes bounds a fill's data, as a library tile's.
const MaxFillBytes = 64 << 10

// maxDefinitionBytes bounds a kept definition.
const maxDefinitionBytes = 512 << 10

// Fill is the data held for an added tile on one version: the tile's ID,
// the version, the digest of the definition it was written against and the
// data, masked and checked when it was stored.
type Fill struct {
	Tile    string
	Version int
	Digest  string
	Data    json.RawMessage
}

// PutFill stores data for an added tile on one version of the pull request,
// with the definition it was written against (kept by its digest), keeps
// the KeepFills newest versions' fills of that tile, and moves the layout's
// revision, so the page reads its tiles again. The caller has checked the
// data (library.CheckAddedFill) and masked it.
func (s *Service) PutFill(ctx context.Context, pr, tile string, version int, definition, data json.RawMessage) error {
	if !ValidKey(pr) || version < 1 || !tileIDPattern(tile) {
		return errors.New("invalid fill")
	}
	if len(data) > MaxFillBytes || len(definition) > maxDefinitionBytes || !json.Valid(data) {
		return fmt.Errorf("a tile's data is at most %d KiB of JSON", MaxFillBytes>>10)
	}
	var def struct {
		Digest string `json:"digest"`
	}
	if json.Unmarshal(definition, &def) != nil || def.Digest == "" {
		return errors.New("the fill's definition has no digest")
	}
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	defer release()
	now := s.now().UTC().Format(time.RFC3339Nano)
	var revision int64
	err = appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		for _, statement := range []string{schema, fillsSchema, definitionsSchema} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_definitions(digest, definition, at) VALUES (?, ?, ?)
ON CONFLICT(digest) DO UPDATE SET at = excluded.at`, def.Digest, []byte(definition), now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_fills(pr, tile, version, digest, data, at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(pr, tile, version) DO UPDATE SET digest = excluded.digest, data = excluded.data, at = excluded.at`, pr, tile, version, def.Digest, []byte(data), now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM tile_fills WHERE pr = ? AND tile = ? AND version NOT IN
  (SELECT version FROM tile_fills WHERE pr = ? AND tile = ? ORDER BY version DESC LIMIT ?)`, pr, tile, pr, tile, KeepFills); err != nil {
			return err
		}
		// The layout's revision moves, so the page reads its tiles again.
		err := conn.QueryRowContext(ctx, `UPDATE tile_overlays SET revision = revision + 1, updated = ? WHERE pr = ? RETURNING revision`, now, pr).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	if s.Changed != nil {
		s.Changed(pr, revision)
	}
	return nil
}

func tileIDPattern(id string) bool {
	return id != "" && len(id) <= 128 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// Fills returns the fills held for a pull request's added tiles, newest
// version first. A database or table that does not exist yet holds none.
func (s *Service) Fills(ctx context.Context, pr string) ([]Fill, error) {
	if !ValidKey(pr) {
		return nil, errors.New("invalid pull request key")
	}
	db, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.QueryContext(ctx, `SELECT tile, version, digest, data FROM tile_fills WHERE pr = ? ORDER BY version DESC, tile`, pr)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Fill
	for rows.Next() {
		var f Fill
		var data []byte
		if err := rows.Scan(&f.Tile, &f.Version, &f.Digest, &data); err != nil {
			return nil, err
		}
		f.Data = data
		out = append(out, f)
	}
	return out, rows.Err()
}

// Definition returns a kept definition by its digest.
func (s *Service) Definition(ctx context.Context, digest string) (json.RawMessage, bool) {
	db, release, err := appdb.Acquire(s.Path, true)
	if err != nil {
		return nil, false
	}
	defer release()
	var definition []byte
	if db.QueryRowContext(ctx, `SELECT definition FROM tile_definitions WHERE digest = ?`, digest).Scan(&definition) != nil {
		return nil, false
	}
	return definition, true
}

// Sweep removes what no overlay needs any more: the fills of pull requests
// whose overlay is gone, and the definitions no overlay's add (an undone one
// included) or fill names. The daemon runs it when it starts.
func (s *Service) Sweep(ctx context.Context) error {
	db, release, err := appdb.Acquire(s.Path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	return appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		for _, statement := range []string{schema, fillsSchema, definitionsSchema} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM tile_fills WHERE pr NOT IN (SELECT pr FROM tile_overlays)`); err != nil {
			return err
		}
		named := map[string]bool{}
		rows, err := conn.QueryContext(ctx, `SELECT value FROM tile_overlays`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var value []byte
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return err
			}
			if o, err := tiles.ParseOverlay(value); err == nil {
				for _, b := range append(slices.Clone(o.Batches), o.Undone...) {
					for _, op := range b.Ops {
						if op.Add != nil && op.Add.Digest != "" {
							named[op.Add.Digest] = true
						}
					}
				}
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		rows, err = conn.QueryContext(ctx, `SELECT DISTINCT digest FROM tile_fills`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var digest string
			if err := rows.Scan(&digest); err != nil {
				_ = rows.Close()
				return err
			}
			named[digest] = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		rows, err = conn.QueryContext(ctx, `SELECT digest FROM tile_definitions`)
		if err != nil {
			return err
		}
		var unnamed []string
		for rows.Next() {
			var digest string
			if err := rows.Scan(&digest); err != nil {
				_ = rows.Close()
				return err
			}
			if !named[digest] {
				unnamed = append(unnamed, digest)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, digest := range unnamed {
			if _, err := conn.ExecContext(ctx, `DELETE FROM tile_definitions WHERE digest = ?`, digest); err != nil {
				return err
			}
		}
		return nil
	})
}

// FillRequest asks for a fill of an added library type for one version of
// a pull request, in a registered clone of its repository.
type FillRequest struct {
	Clone      string
	Repository string
	Number     int
	Version    int
	Type       string
}

// AddedTypes are the library types the layout's active batches add, each
// once, in the order first added.
func (s State) AddedTypes() []tiles.Added {
	var out []tiles.Added
	for _, b := range s.Overlay.Active() {
		for _, op := range b.Ops {
			if op.Op != tiles.OpAdd || op.Add == nil {
				continue
			}
			if i := slices.IndexFunc(out, func(a tiles.Added) bool { return a.Name == op.Add.Name }); i >= 0 {
				out[i] = *op.Add
				continue
			}
			out = append(out, *op.Add)
		}
	}
	return out
}

// Version is what AddedFor needs of the version served: its number, whether
// it is the pull request's latest, the library types its generation filled
// (tiles the composition already holds) and its hunks; nil Diff (a draft's
// layout) checks no earlier fill's citations.
type Version struct {
	Number    int
	Latest    bool
	Generated map[string]bool
	Diff      []bundle.Hunk
}

// AddedFor is what the host holds for the added types on a version, as the
// tile views apply it (tileview.Layout's Added and Notes), and the types to
// fill for it. A type the version's generation filled needs nothing: the
// replay only places it. Otherwise:
//
//   - a fill for this version is shown as it is;
//   - else on the latest version, the newest earlier fill is shown while its
//     citations still resolve to this version's hunks, marked as from that
//     version and filling; with none, the tile is pending; either way the
//     type is to be filled;
//   - on an earlier version, the newest earlier fill is shown, marked as
//     from its version, and with none the type is not shown.
//
// An unknown type, and a fill whose definition is gone, give nothing.
// definition returns a kept definition by its digest.
func AddedFor(added []tiles.Added, fills []Fill, v Version, definition func(digest string) (json.RawMessage, bool)) (layout tileview.Layout, missing []string) {
	validator := fillValidator()
	for _, a := range added {
		entry, ok := library.Get(a.Name)
		if !ok || v.Generated[a.Name] {
			continue
		}
		id := tiles.LibraryTileID(a.Name)
		defOf := func(f Fill) (library.Entry, bool) {
			if f.Digest == entry.Definition.Digest {
				return entry, true
			}
			stored, ok := definition(f.Digest)
			if !ok {
				return library.Entry{}, false
			}
			return library.Entry{Encoded: stored}, true
		}
		var shown *tiles.LibraryTile
		note := tileview.AddedNote{Tile: id}
		for _, f := range fills {
			if f.Tile != id || f.Version > v.Number {
				continue
			}
			e, ok := defOf(f)
			if !ok {
				continue
			}
			if f.Version == v.Number {
				shown = &tiles.LibraryTile{Definition: e.Encoded, Data: f.Data}
				break
			}
			if v.Diff == nil || len(library.CheckAddedFill(validator, e, f.Data, v.Diff)) == 0 {
				shown, note.From = &tiles.LibraryTile{Definition: e.Encoded, Data: f.Data}, f.Version
				break
			}
		}
		if shown != nil && note.From == 0 {
			layout.Added = append(layout.Added, *shown)
			continue
		}
		if !v.Latest {
			if shown != nil {
				layout.Added = append(layout.Added, *shown)
				layout.Notes = append(layout.Notes, note)
			}
			continue
		}
		if shown == nil {
			shown = &tiles.LibraryTile{Definition: entry.Encoded, Data: json.RawMessage(`{}`), Pending: true}
		}
		note.Filling = true
		layout.Added = append(layout.Added, *shown)
		layout.Notes = append(layout.Notes, note)
		missing = append(missing, a.Name)
	}
	return layout, missing
}

// fillValidator checks held fills; it compiles its schemas once.
var fillValidator = sync.OnceValue(tiles.NewValidator)

// Generated are the library types a stored composition holds filled.
func Generated(composition []byte) map[string]bool {
	out := map[string]bool{}
	if len(composition) == 0 {
		return out
	}
	c, err := tiles.ParseComposition(composition)
	if err != nil {
		return out
	}
	for _, lib := range c.Library {
		var def struct {
			Name string `json:"name"`
		}
		if !lib.Pending && json.Unmarshal(lib.Definition, &def) == nil && def.Name != "" {
			out[def.Name] = true
		}
	}
	return out
}
