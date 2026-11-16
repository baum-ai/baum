package addedtiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// A tile the person added that the version's generation did not fill shows
// the data the host holds for it: a fill, written for one version of the
// pull request by the session panel's agent (add_tile or add_custom_tile
// with data) or by a fill run, kept in tile_fills. The definition a fill was
// written against is kept in tile_definitions while an added tile or a fill
// names its digest.
const (
	fillsSchema = `CREATE TABLE IF NOT EXISTS tile_fills(
  pr TEXT NOT NULL,
  tile TEXT NOT NULL,
  version INTEGER NOT NULL,
  digest TEXT NOT NULL,
  data BLOB NOT NULL,
  at TEXT NOT NULL,
  origin TEXT NOT NULL DEFAULT 'chat',
  PRIMARY KEY(pr, tile, version))`
	// fillsOrigin adds origin to a tile_fills table made before it had
	// one; its rows read as the agent's.
	fillsOrigin       = `ALTER TABLE tile_fills ADD COLUMN origin TEXT NOT NULL DEFAULT 'chat'`
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
// the version, the digest of the definition it was written against, the
// data, masked and checked when it was stored, and who wrote it (Origin).
type Fill struct {
	Tile    string
	Version int
	Digest  string
	Data    json.RawMessage
	Origin  string
}

// The origins of a fill: the session panel's agent (add_tile with data), a
// person's own data, shown on their page only; or baum's own fill run, which
// a later review may reuse as its generated tile.
const (
	OriginChat = "chat"
	OriginFill = "fill"
)

// ensureTables creates the added tiles' tables, and adds origin to a fills
// table that lacks it.
func ensureTables(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range []string{schema, fillsSchema, definitionsSchema} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, fillsOrigin); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

// PutFill stores data for an added tile on one version of the pull request,
// with the definition it was written against (kept by its digest), keeps
// the KeepFills newest versions' fills of that tile, and tells Changed, so
// the page reads its tiles again. The caller has checked the data
// (library.CheckAddedFill) and masked it.
func (s *Service) PutFill(ctx context.Context, pr, tile string, version int, origin string, definition, data json.RawMessage) error {
	if !ValidKey(pr) || version < 1 || !tileIDPattern(tile) || origin != OriginChat && origin != OriginFill {
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
	err = appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if err := ensureTables(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_definitions(digest, definition, at) VALUES (?, ?, ?)
ON CONFLICT(digest) DO UPDATE SET at = excluded.at`, def.Digest, []byte(definition), now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_fills(pr, tile, version, digest, data, at, origin) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(pr, tile, version) DO UPDATE SET digest = excluded.digest, data = excluded.data, at = excluded.at, origin = excluded.origin`, pr, tile, version, def.Digest, []byte(data), now, origin); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM tile_fills WHERE pr = ? AND tile = ? AND version NOT IN
  (SELECT version FROM tile_fills WHERE pr = ? AND tile = ? ORDER BY version DESC LIMIT ?)`, pr, tile, pr, tile, KeepFills)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	s.changed(pr)
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
	rows, err := db.QueryContext(ctx, `SELECT tile, version, digest, data, origin FROM tile_fills WHERE pr = ? ORDER BY version DESC, tile`, pr)
	if err != nil && strings.Contains(err.Error(), "no such column") {
		// A table made before fills had an origin: every row is the agent's.
		rows, err = db.QueryContext(ctx, `SELECT tile, version, digest, data, 'chat' FROM tile_fills WHERE pr = ? ORDER BY version DESC, tile`, pr)
	}
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
		if err := rows.Scan(&f.Tile, &f.Version, &f.Digest, &data, &f.Origin); err != nil {
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

// Sweep removes what no added tile needs any more: the fills of tiles no
// pull request has added, the overlays of the tile grid this baum no longer
// reads, and the definitions no added tile (a removed one included), fill
// or saved tile type (the tile library's tile_types and tile_type_versions,
// when they exist) names; when the tile library cannot be read, no
// definition is removed. The daemon runs it when it starts.
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
		if err := ensureTables(ctx, conn); err != nil {
			return err
		}
		if err := prune(ctx, conn, s.now()); err != nil {
			return err
		}
		// The tile grid kept each person's layout here; nothing reads it.
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS tile_overlays`); err != nil {
			return err
		}
		named := map[string]bool{}
		rows, err := conn.QueryContext(ctx, `SELECT DISTINCT digest FROM added_tiles`)
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
		// The tile library (saved tile types) keeps its definitions in the
		// same table; what it names is kept. When it cannot be read, no
		// definition is removed.
		for _, table := range []string{"tile_types", "tile_type_versions"} {
			var exists int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
				return nil
			}
			if exists == 0 {
				continue
			}
			digests, err := conn.QueryContext(ctx, `SELECT digest FROM `+table)
			if err != nil {
				if strings.Contains(err.Error(), "no such table") {
					continue
				}
				return nil
			}
			for digests.Next() {
				var digest string
				if err := digests.Scan(&digest); err != nil {
					_ = digests.Close()
					return nil
				}
				named[digest] = true
			}
			if err := digests.Close(); err != nil {
				return nil
			}
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

// FillRequest asks for a fill of an added type for one version of a pull
// request, in a registered clone of its repository: a library type by its
// name, or a type made in chat by its name and the digest of its kept
// definition (Digest).
type FillRequest struct {
	Clone      string
	Repository string
	Number     int
	Version    int
	Type       string
	Digest     string
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

// AddedFor is what the host holds for the added tiles on a version, as the
// tile views apply it (tileview.AddedTile), and the tiles to fill for it. A type the version's generation filled needs nothing: the
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
// A type made in chat (a custom add) is read from its kept definition. It
// is never filled again (its fill instructions came from the session agent
// and are not trusted until the person saves the tile to the library), so
// every version after the one it was made for shows its newest earlier data,
// marked as from that version, and never as being filled.
//
// An unknown type, and a fill whose definition is gone, give nothing.
// definition returns a kept definition by its digest.
func AddedFor(added []Added, fills []Fill, v Version, definition func(digest string) (json.RawMessage, bool)) (held []tileview.AddedTile, missing []Added) {
	validator := fillValidator()
	for _, a := range added {
		entry, fillable, ok := addedEntry(a, definition)
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
		var shown, newest *tiles.LibraryTile
		note := tileview.AddedTile{Section: a.Section}
		newestFrom := 0
		show := func() {
			note.Tile = *shown
			held = append(held, note)
		}
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
			if newest == nil {
				newest, newestFrom = &tiles.LibraryTile{Definition: e.Encoded, Data: f.Data}, f.Version
			}
			if v.Diff == nil || len(library.CheckAddedFill(validator, e, f.Data, v.Diff)) == 0 {
				shown, note.From = &tiles.LibraryTile{Definition: e.Encoded, Data: f.Data}, f.Version
				break
			}
		}
		if shown != nil && note.From == 0 {
			show()
			continue
		}
		if !fillable {
			// Nothing can fill it again: its newest data, whose citations
			// the tile checks still hold to this version.
			if shown == nil && newest != nil {
				shown, note.From = newest, newestFrom
			}
			if shown != nil {
				show()
			}
			continue
		}
		if !v.Latest {
			if shown != nil {
				show()
			}
			continue
		}
		if shown == nil {
			shown = &tiles.LibraryTile{Definition: entry.Encoded, Data: json.RawMessage(`{}`), Pending: true}
		}
		note.Filling = true
		show()
		missing = append(missing, a)
	}
	return held, missing
}

// addedEntry is the type an add names: a library type by its name, or a
// type made in chat by its kept definition. fillable reports whether a fill
// run can fill it: always for a library type, never for a type made in chat.
func addedEntry(a Added, definition func(digest string) (json.RawMessage, bool)) (entry library.Entry, fillable, ok bool) {
	if a.Kind != KindCustom {
		entry, ok = library.Get(a.Name)
		return entry, true, ok
	}
	// A one-off tile made in chat is never refilled: its fill instructions
	// were written by the session agent, which reads untrusted pull request
	// content, and the person has not seen or saved them. Save to library
	// shows them before they become a library tile's.
	entry, ok = CustomEntry(a, definition)
	return entry, false, ok
}

// CustomEntry is a type made in chat as a library entry, from its kept
// definition: what a fill run fills and the fill checks read. It is false
// when the definition is gone or is not the add's type.
func CustomEntry(a Added, definition func(digest string) (json.RawMessage, bool)) (library.Entry, bool) {
	stored, ok := definition(a.Digest)
	if !ok {
		return library.Entry{}, false
	}
	var def tiles.TypeDefinition
	if json.Unmarshal(stored, &def) != nil || def.Name != a.Name || def.Digest != a.Digest {
		return library.Entry{}, false
	}
	return library.Entry{Definition: def, Encoded: stored}, true
}

// fillValidator checks held fills; it compiles its schemas once.
var fillValidator = sync.OnceValue(tiles.NewValidator)

// Generated are the library types a stored composition holds filled.
func Generated(composition []byte) map[string]bool {
	out := map[string]bool{}
	if len(composition) == 0 {
		return out
	}
	c, err := view.Parse(composition)
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

// HeldFill is the fill held for a tile on one version, with its definition,
// or nil when there is none.
func (s *Service) HeldFill(ctx context.Context, pr, tile string, version int) *HeldFill {
	fills, err := s.Fills(ctx, pr)
	if err != nil {
		return nil
	}
	for _, f := range fills {
		if f.Tile == tile && f.Version == version {
			definition, ok := s.Definition(ctx, f.Digest)
			if !ok {
				return nil
			}
			return &HeldFill{Origin: f.Origin, Definition: definition, Data: f.Data}
		}
	}
	return nil
}

// HeldFill is a fill as HeldFill returns it.
type HeldFill struct {
	Origin     string
	Definition json.RawMessage
	Data       json.RawMessage
}

// RestoreFill takes back a fill a refused add wrote: it puts prior back
// (nil: the tile had no fill on that version, so it is deleted).
func (s *Service) RestoreFill(ctx context.Context, pr, tile string, version int, prior *HeldFill) {
	ctx = context.WithoutCancel(ctx)
	if prior != nil {
		_ = s.PutFill(ctx, pr, tile, version, prior.Origin, prior.Definition, prior.Data)
		return
	}
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return
	}
	defer release()
	_, _ = db.ExecContext(ctx, `DELETE FROM tile_fills WHERE pr = ? AND tile = ? AND version = ?`, pr, tile, version)
}
