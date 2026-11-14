// Package tilelibrary keeps the person's own library of tile types on this
// computer: the types a chat made and the person saved, in the private
// application database (app.sqlite), never in a review store and never
// beside a pull request's data.
//
// A saved type is stored as the built-in library's types are written, in
// that directory layout (BuildEntry gives a library.Entry from it), so every
// static check a built-in type passes (library.Check) applies to it
// unchanged. Its definition, with its HTML, is kept by digest in
// tile_definitions, and every version of a type is a digest. A type's
// samples are synthetic: a save never stores data of a real pull request.
//
// A review that uses a type embeds its full definition (LibraryTile), so
// editing, restoring or deleting a type never changes a saved review.
package tilelibrary

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// UndoWindow is how long a deleted type can be brought back; after it the
// type is purged.
const UndoWindow = 30 * 24 * time.Hour

// schema is created when a write opens the database; an older app ignores
// the tables, and a read of a database without them finds an empty library.
// tile_definitions holds the definitions by digest, as a custom tile of a
// layout edit names them.
const schema = `CREATE TABLE IF NOT EXISTS tile_definitions(
  digest TEXT PRIMARY KEY,
  definition BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS tile_types(
  name TEXT PRIMARY KEY,
  digest TEXT NOT NULL,
  glance TEXT NOT NULL,
  review_checks TEXT NOT NULL,
  applies_with TEXT NOT NULL,
  paths TEXT NOT NULL,
  samples BLOB NOT NULL,
  origin TEXT NOT NULL,
  created TEXT NOT NULL,
  updated TEXT NOT NULL,
  used_at TEXT,
  used_in TEXT,
  deleted TEXT);
CREATE TABLE IF NOT EXISTS tile_type_versions(
  name TEXT NOT NULL,
  digest TEXT NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(name, digest))`

// maxBytes is MaxBytes; a test lowers it.
var maxBytes int64 = MaxBytes

// Store reads and writes the library. The zero Path is the default
// application database.
type Store struct {
	Path string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Version is a type's version: a definition digest and when it was saved.
type Version struct {
	Digest  string `json:"digest"`
	At      string `json:"at"`
	Current bool   `json:"current,omitempty"`
}

// Type is a saved type.
type Type struct {
	Name    string
	Entry   library.Entry
	Paths   []string
	Origin  string
	Created string
	Updated string
	// UsedAt and UsedIn say when and where a fill stored in a version last
	// used the type.
	UsedAt string
	UsedIn map[string]string
	// Deleted is when the type was deleted, empty while it is in the library.
	Deleted string
	// Versions are the type's versions, the newest first.
	Versions []Version
}

// State is what a proposal about a type is read at: whether it is in the
// library and which version is current.
type State struct {
	Present bool   `json:"present"`
	Deleted bool   `json:"deleted,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Updated string `json:"updated,omitempty"`
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func noTable(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such column"))
}

type row struct {
	name, digest, glance, checks, appliesWith, paths, origin, created, updated string
	samples                                                                    []byte
	usedAt, usedIn, deleted                                                    sql.NullString
}

const columns = `name, digest, glance, review_checks, applies_with, paths, samples, origin, created, updated, used_at, used_in, deleted`

func (r *row) scan(s interface{ Scan(...any) error }) error {
	return s.Scan(&r.name, &r.digest, &r.glance, &r.checks, &r.appliesWith, &r.paths, &r.samples, &r.origin, &r.created, &r.updated, &r.usedAt, &r.usedIn, &r.deleted)
}

// expired reports whether a deleted row is past its undo window.
func (r row) expired(now time.Time) bool {
	if !r.deleted.Valid {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, r.deleted.String)
	return err != nil || now.Sub(at) >= UndoWindow
}

func (s *Store) read(ctx context.Context, f func(q queryer) error) error {
	db, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	if err := f(db); err != nil && !noTable(err) {
		return err
	}
	return nil
}

// load builds a Type from its row.
func load(ctx context.Context, q queryer, r row) (Type, error) {
	var definition []byte
	if err := q.QueryRowContext(ctx, `SELECT definition FROM tile_definitions WHERE digest = ?`, r.digest).Scan(&definition); err != nil {
		return Type{}, err
	}
	var samples []library.Sample
	var appliesWith, paths []string
	if err := errors.Join(json.Unmarshal(r.samples, &samples), json.Unmarshal([]byte(r.appliesWith), &appliesWith), json.Unmarshal([]byte(r.paths), &paths)); err != nil {
		return Type{}, err
	}
	def, problems := tiles.NewValidator().CheckDefinition(definition)
	if len(problems) > 0 {
		return Type{}, fmt.Errorf("the stored definition of %s is not valid", r.name)
	}
	entry, err := BuildEntry(def, Change{Glance: r.glance, ReviewChecks: r.checks, AppliesWith: appliesWith, Samples: samples})
	if err != nil {
		return Type{}, err
	}
	t := Type{Name: r.name, Entry: entry, Paths: paths, Origin: r.origin, Created: r.created, Updated: r.updated,
		UsedAt: r.usedAt.String, Deleted: r.deleted.String}
	if r.usedIn.Valid {
		_ = json.Unmarshal([]byte(r.usedIn.String), &t.UsedIn)
	}
	rows, err := q.QueryContext(ctx, `SELECT digest, at FROM tile_type_versions WHERE name = ? ORDER BY at DESC, digest`, r.name)
	if err != nil {
		return Type{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Digest, &v.At); err != nil {
			return Type{}, err
		}
		v.Current = v.Digest == r.digest
		t.Versions = append(t.Versions, v)
	}
	return t, rows.Err()
}

// Get returns a type in the library; a deleted one is not, until undone.
func (s *Store) Get(ctx context.Context, name string) (Type, error) {
	t, err := s.get(ctx, name)
	if err == nil && t.Deleted != "" {
		return Type{}, ErrUnknown
	}
	return t, err
}

func (s *Store) get(ctx context.Context, name string) (Type, error) {
	found := false
	var t Type
	err := s.read(ctx, func(q queryer) error {
		var r row
		err := r.scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM tile_types WHERE name = ?`, name))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.expired(s.now()) {
			return nil
		}
		t, err = load(ctx, q, r)
		found = err == nil
		return err
	})
	switch {
	case err != nil:
		return Type{}, err
	case !found:
		return Type{}, ErrUnknown
	}
	return t, nil
}

// Entry builds the library.Entry of a saved type (Get(...).Entry).
func (s *Store) Entry(ctx context.Context, name string) (library.Entry, error) {
	t, err := s.Get(ctx, name)
	return t.Entry, err
}

// List returns the types in the library, by name; with deleted, the deleted
// ones still inside their undo window too.
func (s *Store) List(ctx context.Context, deleted bool) ([]Type, error) {
	var out []Type
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT `+columns+` FROM tile_types ORDER BY name`)
		if err != nil {
			return err
		}
		var list []row
		for rows.Next() {
			var r row
			if err := r.scan(rows); err != nil {
				_ = rows.Close()
				return err
			}
			list = append(list, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range list {
			if r.expired(s.now()) || (r.deleted.Valid && !deleted) {
				continue
			}
			t, err := load(ctx, q, r)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

// State reads what a proposal about the type is read at.
func (s *Store) State(ctx context.Context, name string) (State, error) {
	var st State
	err := s.read(ctx, func(q queryer) error {
		var r row
		err := r.scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM tile_types WHERE name = ?`, name))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.expired(s.now())) {
			return nil
		}
		if err != nil {
			return err
		}
		st = State{Present: true, Deleted: r.deleted.Valid, Digest: r.digest, Updated: r.updated}
		return nil
	})
	return st, err
}

// Create saves a new type: its name must not be in the library (a deleted
// type holds its name until it is purged). unchanged, when set, is called
// on the state the write reads and refuses the write by returning an error.
func (s *Store) Create(ctx context.Context, c Change, unchanged func(State) error) (Type, error) {
	return s.save(ctx, c, true, unchanged)
}

// Update saves a new version of a type in the library, or the same version
// with other texts. A digest the type already has becomes its current
// version again.
func (s *Store) Update(ctx context.Context, c Change, unchanged func(State) error) (Type, error) {
	return s.save(ctx, c, false, unchanged)
}

// write runs f in a write transaction after creating the tables.
func (s *Store) write(ctx context.Context, f func(conn querierExec) error) error {
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return fmt.Errorf("The tile library could not be saved: %v", err)
	}
	defer release()
	return appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		return f(conn)
	})
}

type querierExec interface {
	queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) save(ctx context.Context, c Change, create bool, unchanged func(State) error) (Type, error) {
	p, err := Prepare(c)
	if err != nil {
		return Type{}, err
	}
	name, def := p.Name(), p.Entry.Definition
	encoded := []byte(p.Entry.Encoded)
	samples, _ := json.Marshal(p.Entry.Samples)
	appliesWith, _ := json.Marshal(nonNil(p.Entry.AppliesWith))
	paths, _ := json.Marshal(nonNil(p.Paths))
	now := s.now().Format(time.RFC3339Nano)
	err = s.write(ctx, func(conn querierExec) error {
		if err := purge(ctx, conn, s.now()); err != nil {
			return err
		}
		var existing row
		err := existing.scan(conn.QueryRowContext(ctx, `SELECT `+columns+` FROM tile_types WHERE name = ?`, name))
		present := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if unchanged != nil {
			st := State{}
			if present {
				st = State{Present: true, Deleted: existing.deleted.Valid, Digest: existing.digest, Updated: existing.updated}
			}
			if err := unchanged(st); err != nil {
				return err
			}
		}
		switch {
		case create && present:
			return ErrExists
		case !create && !present:
			return ErrUnknown
		case present && existing.deleted.Valid:
			return ErrDeleted
		}
		var live int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tile_types WHERE deleted IS NULL`).Scan(&live); err != nil {
			return err
		}
		if create && live >= MaxTypes {
			return ErrTooMany
		}
		if err := checkSize(ctx, conn, int64(len(encoded)+len(samples))); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_definitions(digest, definition) VALUES (?, ?) ON CONFLICT(digest) DO NOTHING`, def.Digest, encoded); err != nil {
			return err
		}
		created := now
		if present {
			created = existing.created
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_types(`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(name) DO UPDATE SET digest = excluded.digest, glance = excluded.glance, review_checks = excluded.review_checks,
  applies_with = excluded.applies_with, paths = excluded.paths, samples = excluded.samples, updated = excluded.updated`,
			name, def.Digest, p.Entry.Glance, p.Entry.ReviewChecks, string(appliesWith), string(paths), samples, p.Origin, created, now,
			existing.usedAt, existing.usedIn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_type_versions(name, digest, at) VALUES (?, ?, ?)
ON CONFLICT(name, digest) DO UPDATE SET at = excluded.at`, name, def.Digest, now); err != nil {
			return err
		}
		// The oldest versions go beyond the limit; the current one never does.
		if _, err := conn.ExecContext(ctx, `DELETE FROM tile_type_versions WHERE name = ? AND digest != ? AND digest NOT IN (
  SELECT digest FROM tile_type_versions WHERE name = ? ORDER BY at DESC, digest LIMIT ?)`, name, def.Digest, name, MaxVersions); err != nil {
			return err
		}
		return sweep(ctx, conn)
	})
	if err != nil {
		return Type{}, err
	}
	return s.Get(ctx, name)
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// checkSize refuses a write that would take the library past MaxBytes.
func checkSize(ctx context.Context, q queryer, adding int64) error {
	var definitions, samples int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(definition)), 0) FROM tile_definitions WHERE digest IN (
  SELECT digest FROM tile_type_versions UNION SELECT digest FROM tile_types)`).Scan(&definitions); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(samples)), 0) FROM tile_types`).Scan(&samples); err != nil {
		return err
	}
	if definitions+samples+adding > maxBytes {
		return ErrTooLarge
	}
	return nil
}

// Rename changes a type's title, which is all a rename changes: the name is
// the ID of its tiles. The new title is a new version, as it is in the
// definition.
func (s *Store) Rename(ctx context.Context, name, title string) (Type, error) {
	t, err := s.Get(ctx, name)
	if err != nil {
		return Type{}, err
	}
	var def map[string]json.RawMessage
	if err := json.Unmarshal(t.Entry.Encoded, &def); err != nil {
		return Type{}, err
	}
	def["title"], _ = json.Marshal(strings.TrimSpace(title))
	delete(def, "digest")
	c := ChangeOf(t.Entry, t.Paths, t.Origin)
	c.Definition, _ = json.Marshal(def)
	return s.Update(ctx, c, func(st State) error {
		if st.Digest != t.Entry.Definition.Digest {
			return ErrStale
		}
		return nil
	})
}

// Restore makes an earlier version the current one. It refuses a version
// that no longer fits the type's samples, which are not versioned.
func (s *Store) Restore(ctx context.Context, name, digest string) (Type, error) {
	t, err := s.Get(ctx, name)
	if err != nil {
		return Type{}, err
	}
	if !slices.ContainsFunc(t.Versions, func(v Version) bool { return v.Digest == digest }) {
		return Type{}, ErrNoVersion
	}
	var definition []byte
	err = s.read(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, `SELECT definition FROM tile_definitions WHERE digest = ?`, digest).Scan(&definition)
	})
	if err != nil || len(definition) == 0 {
		return Type{}, ErrNoVersion
	}
	c := ChangeOf(t.Entry, t.Paths, t.Origin)
	c.Definition = definition
	return s.Update(ctx, c, func(st State) error {
		if st.Digest != t.Entry.Definition.Digest {
			return ErrStale
		}
		return nil
	})
}

// Delete hides a type; Undelete brings it back within UndoWindow.
func (s *Store) Delete(ctx context.Context, name string) error {
	return s.setDeleted(ctx, name, true)
}

// Undelete brings back a deleted type that is still inside the undo window.
func (s *Store) Undelete(ctx context.Context, name string) error {
	return s.setDeleted(ctx, name, false)
}

func (s *Store) setDeleted(ctx context.Context, name string, deleted bool) error {
	return s.write(ctx, func(conn querierExec) error {
		if err := purge(ctx, conn, s.now()); err != nil {
			return err
		}
		var r row
		err := r.scan(conn.QueryRowContext(ctx, `SELECT `+columns+` FROM tile_types WHERE name = ?`, name))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.deleted.Valid == deleted) {
			return ErrUnknown
		}
		if err != nil {
			return err
		}
		if !deleted {
			var live int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tile_types WHERE deleted IS NULL`).Scan(&live); err != nil {
				return err
			}
			if live >= MaxTypes {
				return ErrTooMany
			}
			_, err = conn.ExecContext(ctx, `UPDATE tile_types SET deleted = NULL WHERE name = ?`, name)
			return err
		}
		_, err = conn.ExecContext(ctx, `UPDATE tile_types SET deleted = ? WHERE name = ?`, s.now().Format(time.RFC3339Nano), name)
		return err
	})
}

// Purge removes the types deleted longer ago than UndoWindow, their
// versions, and the definitions nothing names any more. Every write does the
// same first; the daemon may also call it.
func (s *Store) Purge(ctx context.Context) error {
	_, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	release()
	return s.write(ctx, func(conn querierExec) error { return purge(ctx, conn, s.now()) })
}

func purge(ctx context.Context, conn querierExec, now time.Time) error {
	cutoff := now.Add(-UndoWindow).Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(ctx, `DELETE FROM tile_type_versions WHERE name IN (SELECT name FROM tile_types WHERE deleted IS NOT NULL AND deleted <= ?)`, cutoff); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM tile_types WHERE deleted IS NOT NULL AND deleted <= ?`, cutoff); err != nil {
		return err
	}
	return sweep(ctx, conn)
}

// sweep removes the definitions no type version names and no layout edit
// or stored fill (tile_overlays, tile_fills, when they exist) names by
// digest: a definition kept while named elsewhere.
func sweep(ctx context.Context, conn querierExec) error {
	rows, err := conn.QueryContext(ctx, `SELECT digest FROM tile_definitions WHERE digest NOT IN (
  SELECT digest FROM tile_type_versions UNION SELECT digest FROM tile_types)`)
	if err != nil {
		return err
	}
	var candidates []string
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, digest)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, digest := range candidates {
		named, err := namedElsewhere(ctx, conn, digest)
		if err != nil {
			return err
		}
		if !named {
			if _, err := conn.ExecContext(ctx, `DELETE FROM tile_definitions WHERE digest = ?`, digest); err != nil {
				return err
			}
		}
	}
	return nil
}

func namedElsewhere(ctx context.Context, q queryer, digest string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM tile_overlays WHERE instr(CAST(value AS TEXT), ?) > 0 LIMIT 1`, digest).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case !errors.Is(err, sql.ErrNoRows) && !noTable(err):
		return false, err
	}
	err = q.QueryRowContext(ctx, `SELECT 1 FROM tile_fills WHERE digest = ? LIMIT 1`, digest).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows) || noTable(err):
		return false, nil
	}
	return false, err
}
