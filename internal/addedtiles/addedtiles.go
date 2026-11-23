// Package addedtiles keeps the tiles a person added to a pull request's
// review: a library type added from the session panel (add_tile) or a tile
// made in chat (add_custom_tile). Each is kept per pull request, in the
// private application database (app.sqlite), never in the review store a
// version is saved in, with the section of the review page it sits on; it
// shows on every version of the pull request. The tile routes read them and
// serve each with the data the host holds for it on the version served
// (fills.go). Removing one keeps its row, marked removed, so the page's Undo
// puts it back where it was.
package addedtiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/tileview"
)

// schema is the added tiles' table: one row per pull request and tile,
// removed set while the tile is removed (an Undo puts it back). It is
// created when a write opens the database; a read of a database without it
// finds no added tiles.
const schema = `CREATE TABLE IF NOT EXISTS added_tiles(
  pr TEXT NOT NULL,
  tile TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  digest TEXT NOT NULL,
  section TEXT NOT NULL,
  by TEXT NOT NULL,
  added TEXT NOT NULL,
  removed TEXT,
  PRIMARY KEY(pr, tile))`

// Keep is how many pull requests' added tiles are kept, the most recently
// changed first.
const Keep = 200

// KeepRemoved is how long a removed tile is kept for its Undo.
const KeepRemoved = 24 * time.Hour

// The kinds of an added tile.
const (
	// KindLibrary is a type of baum's tile library, by its name.
	KindLibrary = "library"
	// KindCustom is a type made in chat, by its name and the digest of its
	// kept definition.
	KindCustom = "custom"
	// KindNative is a type the page draws itself (flow, columns, sequence,
	// table, code, images), by its name; its digest is NativeDigest. Its
	// data is the agent's, written once (native.go).
	KindNative = "native"
)

var (
	// ErrNotFound is a remove of a tile that is not added, or a restore of
	// one that was not removed.
	ErrNotFound = errors.New("no such added tile")
	// ErrUnsaved is a write the database did not take.
	ErrUnsaved = errors.New("The added tiles could not be saved; try again")
)

// pullKey is a pull request's key: owner/repository#number, lowercased,
// optionally after an Enterprise host, as uistate keys reviews.
var pullKey = regexp.MustCompile(`^[a-z0-9._-]+(?:/[a-z0-9._-]+){1,2}#[1-9][0-9]*$`)

// Key is the key of a pull request's added tiles.
func Key(repository string, number int) string {
	return strings.ToLower(strings.TrimSpace(repository)) + "#" + strconv.Itoa(number)
}

// ValidKey reports whether key is a pull request's key.
func ValidKey(key string) bool { return len(key) <= 300 && pullKey.MatchString(key) }

// Added is a tile a person added: its tile ID (tiles.LibraryTileID of its
// type's name), its kind, its type's name and the digest of its definition,
// the section of the review page it sits on (a section ID or "context";
// empty to place it by the hunks it covers), who added it ("chat" or
// "page") and when.
type Added struct {
	Tile    string `json:"tile"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Digest  string `json:"digest"`
	Section string `json:"section,omitempty"`
	By      string `json:"by"`
	At      string `json:"at"`
}

// Service reads and writes the added tiles. The zero Path is the default
// application database.
type Service struct {
	Path string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Changed is told of every committed write, with the pull request's key:
	// the daemon publishes the added tiles event.
	Changed func(pr string)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) changed(pr string) {
	if s.Changed != nil {
		s.Changed(pr)
	}
}

// List returns a pull request's added tiles, the first added first. A
// database or table that does not exist yet holds none.
func (s *Service) List(ctx context.Context, pr string) ([]Added, error) {
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
	rows, err := db.QueryContext(ctx, `SELECT tile, kind, name, digest, section, by, added FROM added_tiles WHERE pr = ? AND removed IS NULL ORDER BY added, tile`, pr)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Added
	for rows.Next() {
		var a Added
		if err := rows.Scan(&a.Tile, &a.Kind, &a.Name, &a.Digest, &a.Section, &a.By, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Add adds a tile to a pull request, or replaces the tile of the same ID
// (removed or not) with it. The caller has checked the type and written the
// digest; At is the host's.
func (s *Service) Add(ctx context.Context, pr string, a Added) error {
	if !ValidKey(pr) || !tileIDPattern(a.Tile) || a.Kind != KindLibrary && a.Kind != KindCustom && a.Kind != KindNative || a.Name == "" || a.Digest == "" || len(a.Section) > 128 {
		return errors.New("invalid added tile")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	err := s.write(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO added_tiles(pr, tile, kind, name, digest, section, by, added, removed) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(pr, tile) DO UPDATE SET kind = excluded.kind, name = excluded.name, digest = excluded.digest, section = excluded.section, by = excluded.by, added = excluded.added, removed = NULL`,
			pr, a.Tile, a.Kind, a.Name, a.Digest, a.Section, a.By, now)
		return err
	})
	if err != nil {
		return err
	}
	s.changed(pr)
	return nil
}

// Remove removes an added tile from a pull request. Its row is kept, marked
// removed, for Restore.
func (s *Service) Remove(ctx context.Context, pr, tile string) error {
	return s.mark(ctx, pr, tile, true)
}

// Restore puts back a tile Remove removed, where it was.
func (s *Service) Restore(ctx context.Context, pr, tile string) error {
	return s.mark(ctx, pr, tile, false)
}

func (s *Service) mark(ctx context.Context, pr, tile string, remove bool) error {
	if !ValidKey(pr) || !tileIDPattern(tile) {
		return ErrNotFound
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	err := s.write(ctx, func(conn *sql.Conn) error {
		query := `UPDATE added_tiles SET removed = NULL WHERE pr = ? AND tile = ? AND removed IS NOT NULL`
		args := []any{pr, tile}
		if remove {
			query = `UPDATE added_tiles SET removed = ? WHERE pr = ? AND tile = ? AND removed IS NULL`
			args = []any{now, pr, tile}
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.changed(pr)
	return nil
}

// write runs change in one write transaction, then keeps the Keep most
// recently changed pull requests' added tiles, drops removed tiles past
// KeepRemoved, and the fills of tiles no pull request has.
func (s *Service) write(ctx context.Context, change func(conn *sql.Conn) error) error {
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	defer release()
	refused := false
	err = appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if err := ensureTables(ctx, conn); err != nil {
			return err
		}
		if err := change(conn); err != nil {
			refused = true
			return err
		}
		return prune(ctx, conn, s.now())
	})
	switch {
	case err == nil:
		return nil
	case refused && errors.Is(err, ErrNotFound):
		return ErrNotFound
	case refused:
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnsaved, err)
}

// prune keeps the Keep most recently changed pull requests, drops removed
// tiles older than KeepRemoved, the fills of tiles no longer added, and the
// images of pull requests without added tiles once they are ImageGrace old.
func prune(ctx context.Context, conn *sql.Conn, now time.Time) error {
	cutoff := now.Add(-KeepRemoved).UTC().Format(time.RFC3339Nano)
	images := now.Add(-ImageGrace).UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM added_tiles WHERE removed IS NOT NULL AND removed < ?`, []any{cutoff}},
		{`DELETE FROM added_tiles WHERE pr NOT IN (SELECT pr FROM added_tiles GROUP BY pr ORDER BY MAX(COALESCE(removed, added)) DESC, pr LIMIT ?)`, []any{Keep}},
		{`DELETE FROM tile_fills WHERE NOT EXISTS (SELECT 1 FROM added_tiles a WHERE a.pr = tile_fills.pr AND a.tile = tile_fills.tile)`, nil},
		{`DELETE FROM added_images WHERE at < ? AND NOT EXISTS (SELECT 1 FROM added_tiles a WHERE a.pr = added_images.pr)`, []any{images}},
	} {
		if _, err := conn.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

// Added is a pull request's added tiles as the tile views apply them to a
// version being generated (a draft): each shows its newest fill. None when
// they cannot be read.
func (s *Service) Added(repository string, number int) []tileview.AddedTile {
	ctx := context.Background()
	pr := Key(repository, number)
	added, err := s.List(ctx, pr)
	if err != nil || len(added) == 0 {
		return nil
	}
	fills, _ := s.Fills(ctx, pr)
	held, _ := AddedFor(added, fills, Version{Number: math.MaxInt32}, func(digest string) (json.RawMessage, bool) { return s.Definition(ctx, digest) })
	return held
}
