package tilelibrary_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newStore(t *testing.T) (*tilelibrary.Store, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	return &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Now: c.Now}, c
}

// change is a type of the given name, written as the built-in schema_change
// type is, with its synthetic samples. note is added to the fill
// instructions, so a different note is a different version.
func change(t testing.TB, name, note string) tilelibrary.Change {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name = name
	def.Title = "Saved " + name
	def.Fill += note
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, ReviewChecks: entry.ReviewChecks, AppliesWith: entry.AppliesWith,
		Paths: []string{"**/*.sql"}, Origin: tilelibrary.OriginChat}
	for _, s := range entry.Samples {
		if tilelibrary.IsSynthetic(s) && len(c.Samples) < 1 {
			c.Samples = append(c.Samples, s)
		}
	}
	if len(c.Samples) == 0 {
		t.Fatal("no synthetic samples")
	}
	return c
}

func mustCreate(t *testing.T, s *tilelibrary.Store, c tilelibrary.Change) tilelibrary.Type {
	t.Helper()
	saved, err := s.Create(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// A saved type is read back as a library entry that passes every static
// check the built-in types pass, with the digest its definition has.
func TestSavedTypePassesTheLibraryChecks(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	saved := mustCreate(t, s, change(t, "migrations", ""))
	entry, err := s.Entry(ctx, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if problems := library.Check(entry); len(problems) > 0 {
		t.Fatalf("library.Check: %v", problems)
	}
	if digest, err := tiles.DefinitionDigest(entry.Encoded); err != nil || digest != entry.Definition.Digest || saved.Entry.Definition.Digest != digest {
		t.Errorf("digest %s, computed %s, %v", entry.Definition.Digest, digest, err)
	}
	if library.IsBuiltin(entry.Encoded) {
		t.Error("a saved type is built in")
	}
	if len(saved.Versions) != 1 || !saved.Versions[0].Current || saved.Origin != "chat" || len(saved.Paths) != 1 {
		t.Errorf("saved: %+v", saved)
	}
	if got, err := s.Lookup(ctx, "schema_change"); err != nil || !got.Builtin {
		t.Errorf("a built-in type is read as built in: %+v %v", got.Builtin, err)
	}
}

// A database without the tables reads as an empty library, and a write
// creates them.
func TestEmptyLibrary(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.Get(ctx, "nothing"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Errorf("Get: %v", err)
	}
	if list, err := s.List(ctx, true); err != nil || len(list) != 0 {
		t.Errorf("List: %v %v", list, err)
	}
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tile_types`).Scan(&n); err == nil {
		t.Error("the table exists before any write")
	}
	mustCreate(t, s, change(t, "migrations", ""))
	if err := db.QueryRow(`SELECT COUNT(*) FROM tile_types`).Scan(&n); err != nil || n != 1 {
		t.Errorf("after a write: %d %v", n, err)
	}
}

func TestNameRules(t *testing.T) {
	s, _ := newStore(t)
	for _, name := range []string{"custom", "risks", "overview", "schema_change", "api_change", "sec_changes", "table_x", "code_x", "prose_x", "diagram_x", "library_x", "Upper", "1abc", "has-dash", "", strings.Repeat("a", 65)} {
		if err := tilelibrary.CheckName(name); err == nil {
			t.Errorf("%q is allowed", name)
		}
	}
	for _, name := range []string{"env_vars", "a", strings.Repeat("a", 64), "tables2"} {
		if err := tilelibrary.CheckName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	// A definition with a name the library refuses is not saved.
	for _, name := range []string{"risks", "schema_change", "sec_x"} {
		c := change(t, "migrations", "")
		var def tiles.TypeDefinition
		_ = json.Unmarshal(c.Definition, &def)
		def.Name = name
		c.Definition, _ = library.Seal(&def)
		if _, err := s.Create(context.Background(), c, nil); err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	mustCreate(t, s, change(t, "migrations", ""))
	if _, err := s.Create(context.Background(), change(t, "migrations", " again"), nil); !errors.Is(err, tilelibrary.ErrExists) {
		t.Errorf("a second type of the name: %v", err)
	}
	if _, err := s.Update(context.Background(), change(t, "unknown_type", ""), nil); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Errorf("editing a type that is not there: %v", err)
	}
}

// Samples are synthetic, and a type that fails a static check is refused
// with the check's words.
func TestSavingRefusesWhatTheLibraryChecksRefuse(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := change(t, "migrations", "")
	real := c
	real.Samples = []library.Sample{{Name: "real", Data: c.Samples[0].Data}}
	if _, err := s.Create(ctx, real, nil); err == nil || !strings.Contains(err.Error(), "synthetic") {
		t.Errorf("a sample with no hunks of its own: %v", err)
	}
	none := c
	none.Samples = nil
	if _, err := s.Create(ctx, none, nil); err == nil {
		t.Error("a type with no sample was saved")
	}
	glance := c
	glance.Glance = ""
	if _, err := s.Create(ctx, glance, nil); err == nil || !strings.Contains(err.Error(), "glance") {
		t.Errorf("no glance: %v", err)
	}
	paths := c
	paths.Paths = make([]string, tilelibrary.MaxPaths+1)
	for i := range paths.Paths {
		paths.Paths[i] = "**/*.go"
	}
	if _, err := s.Create(ctx, paths, nil); err == nil {
		t.Error("too many paths")
	}
	origin := c
	origin.Origin = "web"
	if _, err := s.Create(ctx, origin, nil); err == nil {
		t.Error("an unknown origin")
	}
	var def tiles.TypeDefinition
	_ = json.Unmarshal(c.Definition, &def)
	def.Source.HTML = strings.Replace(def.Source.HTML, "<style>", `<img src="https://example.com/a.png"><style>`, 1)
	bad := c
	bad.Definition, _ = library.Seal(&def)
	if _, err := s.Create(ctx, bad, nil); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Errorf("an external URL: %v", err)
	}
	if list, _ := s.List(ctx, true); len(list) != 0 {
		t.Errorf("a refused save left %d types", len(list))
	}
}

func TestBounds(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	// 200 types: the rows are written directly, as saving each would only
	// repeat the checks 200 times.
	mustCreate(t, s, change(t, "t000", ""))
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i := 1; i < tilelibrary.MaxTypes; i++ {
		if _, err := db.Exec(`INSERT INTO tile_types(name, digest, glance, review_checks, applies_with, paths, samples, origin, created, updated)
SELECT ?, digest, glance, review_checks, applies_with, paths, samples, origin, created, updated FROM tile_types WHERE name = 't000'`, fmt.Sprintf("t%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	over := change(t, "one_too_many", "")
	if _, err := s.Create(ctx, over, nil); !errors.Is(err, tilelibrary.ErrTooMany) {
		t.Fatalf("the 201st type: %v", err)
	}
	// A deleted type makes room, and bringing it back needs room again.
	if err := s.Delete(ctx, "t000"); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, over)
	if err := s.Undelete(ctx, "t000"); !errors.Is(err, tilelibrary.ErrTooMany) {
		t.Errorf("undo into a full library: %v", err)
	}
}

// A database whose tile_definitions was created with the layout fills' own
// DDL works for the library's inserts and sweep.
func TestSharedDefinitionsTable(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tile_definitions(digest TEXT PRIMARY KEY, definition BLOB NOT NULL, at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tile_definitions VALUES ('sha256:orphan', x'7b7d', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	saved := mustCreate(t, s, change(t, "migrations", ""))
	var at string
	if err := db.QueryRow(`SELECT at FROM tile_definitions WHERE digest = ?`, saved.Entry.Definition.Digest).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		t.Errorf("at %q: %v", at, err)
	}
	// The sweep removes the orphan the other writer left, and only it.
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(31 * 24 * time.Hour)
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tile_definitions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d definitions after the sweep, %v", n, err)
	}
}

func TestVersionBound(t *testing.T) {
	s, c := newStore(t)
	if tilelibrary.MaxVersions != 20 {
		t.Fatal("the limit is 20 versions")
	}
	const kept = 4
	defer tilelibrary.SetMaxVersions(kept)()
	ctx := context.Background()
	mustCreate(t, s, change(t, "migrations", " v0"))
	for i := 1; i <= kept+2; i++ {
		c.now = c.now.Add(time.Minute)
		if _, err := s.Update(ctx, change(t, "migrations", fmt.Sprintf(" v%d", i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != kept {
		t.Fatalf("%d versions", len(got.Versions))
	}
	if !got.Versions[0].Current || !strings.Contains(got.Entry.Definition.Fill, fmt.Sprintf("v%d", kept+2)) {
		t.Errorf("the newest is not current: %+v", got.Versions[0])
	}
	// The oldest are gone, with their definitions.
	db, err := appdb.Open(s.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tile_definitions`).Scan(&n); err != nil || n != kept {
		t.Errorf("%d definitions, %v", n, err)
	}
}

func TestSizeBound(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	restore := tilelibrary.SetMaxBytes(300 << 10)
	defer restore()
	mustCreate(t, s, change(t, "first", ""))
	var last error
	for i := range 40 {
		_, last = s.Create(ctx, change(t, fmt.Sprintf("more%d", i), ""), nil)
		if last != nil {
			break
		}
	}
	if !errors.Is(last, tilelibrary.ErrTooLarge) {
		t.Errorf("the library never filled: %v", last)
	}
}

func TestVersionsAndRestore(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	first := mustCreate(t, s, change(t, "migrations", ""))
	c.now = c.now.Add(time.Hour)
	second, err := s.Update(ctx, change(t, "migrations", " Also list the indexes."), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entry.Definition.Digest == first.Entry.Definition.Digest || len(second.Versions) != 2 || second.Created != first.Created {
		t.Fatalf("second: %+v", second.Versions)
	}
	if !second.Versions[0].Current || second.Versions[0].Digest != second.Entry.Definition.Digest {
		t.Errorf("versions: %+v", second.Versions)
	}
	c.now = c.now.Add(time.Hour)
	restored, err := s.Restore(ctx, "migrations", first.Entry.Definition.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Entry.Definition.Digest != first.Entry.Definition.Digest || len(restored.Versions) != 2 {
		t.Errorf("restored: %s, %d versions", restored.Entry.Definition.Digest, len(restored.Versions))
	}
	if _, err := s.Restore(ctx, "migrations", "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, tilelibrary.ErrNoVersion) {
		t.Errorf("a version the type lacks: %v", err)
	}
	// Saving the same content again adds no version.
	again, err := s.Update(ctx, change(t, "migrations", ""), nil)
	if err != nil || len(again.Versions) != 2 {
		t.Errorf("the same content again: %d versions, %v", len(again.Versions), err)
	}
}

func TestStaleSave(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, change(t, "migrations", ""))
	_, err := s.Update(ctx, change(t, "migrations", " x"), func(st tilelibrary.State) error {
		if !st.Present || st.Digest == "" {
			t.Errorf("state: %+v", st)
		}
		return tilelibrary.ErrStale
	})
	if !errors.Is(err, tilelibrary.ErrStale) {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "migrations")
	if len(got.Versions) != 1 {
		t.Errorf("a stale save wrote a version")
	}
}

func TestRenameChangesTheTitleOnly(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	before := mustCreate(t, s, change(t, "migrations", ""))
	after, err := s.Rename(ctx, "migrations", "  Database migrations ")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "migrations" || after.Entry.Definition.Name != "migrations" || after.Entry.Definition.Title != "Database migrations" {
		t.Errorf("rename: %q %q", after.Name, after.Entry.Definition.Title)
	}
	b, a := before.Entry.Definition, after.Entry.Definition
	b.Title, b.Digest, a.Title, a.Digest = "", "", "", ""
	bj, _ := json.Marshal(b)
	aj, _ := json.Marshal(a)
	if string(bj) != string(aj) || before.Entry.Glance != after.Entry.Glance || len(after.Entry.Samples) != len(before.Entry.Samples) {
		t.Error("rename changed more than the title")
	}
	if _, err := s.Rename(ctx, "migrations", ""); err == nil {
		t.Error("an empty title")
	}
	if _, err := s.Rename(ctx, "nothing", "x"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Error(err)
	}
}

func TestSoftDeleteUndoAndPurge(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, change(t, "migrations", ""))
	mustCreate(t, s, change(t, "keeper", ""))
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "migrations"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Errorf("a deleted type is shown: %v", err)
	}
	if live, _ := s.List(ctx, false); len(live) != 1 {
		t.Errorf("%d live", len(live))
	}
	if all, _ := s.List(ctx, true); len(all) != 2 {
		t.Errorf("%d with deleted", len(all))
	}
	if _, err := s.Create(ctx, change(t, "migrations", " other"), nil); !errors.Is(err, tilelibrary.ErrExists) {
		t.Errorf("a deleted type holds its name: %v", err)
	}
	if _, err := s.Update(ctx, change(t, "migrations", " other"), nil); !errors.Is(err, tilelibrary.ErrDeleted) {
		t.Errorf("editing a deleted type: %v", err)
	}
	c.now = c.now.Add(29 * 24 * time.Hour)
	if err := s.Undelete(ctx, "migrations"); err != nil {
		t.Fatalf("undo inside 30 days: %v", err)
	}
	if got, err := s.Get(ctx, "migrations"); err != nil || len(got.Versions) != 1 {
		t.Fatalf("after undo: %v", err)
	}
	if err := s.Undelete(ctx, "migrations"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Errorf("undoing what is not deleted: %v", err)
	}
	// After 30 days it is purged, with its versions and definitions, and
	// the name is free.
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(31 * 24 * time.Hour)
	if err := s.Undelete(ctx, "migrations"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Errorf("undo after 30 days: %v", err)
	}
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(s.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	count := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM tile_types`); n != 1 {
		t.Errorf("%d types after the purge", n)
	}
	if n := count(`SELECT COUNT(*) FROM tile_type_versions`); n != 1 {
		t.Errorf("%d versions after the purge", n)
	}
	if n := count(`SELECT COUNT(*) FROM tile_definitions`); n != 1 {
		t.Errorf("%d definitions after the purge", n)
	}
	mustCreate(t, s, change(t, "migrations", ""))
}

// A purge keeps a definition a layout edit or a stored fill still names.
func TestPurgeKeepsDefinitionsNamedElsewhere(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	kept := mustCreate(t, s, change(t, "migrations", ""))
	gone := mustCreate(t, s, change(t, "other", ""))
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE tile_overlays(pr TEXT PRIMARY KEY, revision INTEGER NOT NULL, updated TEXT NOT NULL, value BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tile_overlays VALUES ('acme/web#1', 1, 'now', ?)`, `{"batches":[{"add":{"digest":"`+kept.Entry.Definition.Digest+`"}}]}`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations", "other"} {
		if err := s.Delete(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	c.now = c.now.Add(31 * 24 * time.Hour)
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	has := func(digest string) bool {
		var n int
		return db.QueryRow(`SELECT 1 FROM tile_definitions WHERE digest = ?`, digest).Scan(&n) == nil
	}
	if !has(kept.Entry.Definition.Digest) || has(gone.Entry.Definition.Digest) {
		t.Errorf("kept %v, gone %v", has(kept.Entry.Definition.Digest), has(gone.Entry.Definition.Digest))
	}
}

// A save of a definition the library already holds adds only what is new: a
// text-only edit of a type that fills the library does not count its
// definition and samples twice.
func TestResavingAHeldDefinitionIsNotCountedTwice(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	defer tilelibrary.SetMaxBytes(1 << 30)()
	first := mustCreate(t, s, change(t, "first", ""))
	db, err := appdb.Open(s.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var held int64
	if err := db.QueryRow(`SELECT (SELECT SUM(LENGTH(definition)) FROM tile_definitions) + (SELECT SUM(LENGTH(samples)) FROM tile_types)`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	tilelibrary.SetMaxBytes(held + 10)
	edit := change(t, "first", "")
	edit.Glance = "Another glance."
	got, err := s.Update(ctx, edit, nil)
	if err != nil || got.Entry.Definition.Digest != first.Entry.Definition.Digest || got.Entry.Glance != "Another glance." {
		t.Fatalf("a text-only edit of a type that fills the library: %v", err)
	}
	// A new definition does not fit.
	if _, err := s.Update(ctx, change(t, "first", " More."), nil); !errors.Is(err, tilelibrary.ErrTooLarge) {
		t.Errorf("a larger definition: %v", err)
	}
}

// A table another feature owns that cannot be read keeps the definitions it
// may name; only a missing table names nothing.
func TestSweepKeepsDefinitionsWhenAnotherTableCannotBeRead(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, change(t, "migrations", ""))
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// A tile_overlays of a shape this code does not know: no such column.
	if _, err := db.Exec(`CREATE TABLE tile_overlays(pr TEXT PRIMARY KEY, other BLOB)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(31 * 24 * time.Hour)
	if err := s.Purge(ctx); err != nil {
		t.Fatalf("a purge fails on an unreadable table: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tile_definitions`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d definitions kept, %v", n, err)
	}
}

// A type that cannot be loaded is left out of the list, not a failure of it.
func TestListSkipsATypeThatCannotBeLoaded(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, change(t, "good", ""))
	mustCreate(t, s, change(t, "broken", ""))
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE tile_types SET samples = 'not json' WHERE name = 'broken'`); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, false)
	if err != nil || len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("list: %d types, %v", len(list), err)
	}
	if _, err := s.Get(ctx, "broken"); err == nil {
		t.Error("a broken type was read")
	}
}
