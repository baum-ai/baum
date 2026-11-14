package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

var compositionKey = Key{"acme/widgets", 42}

// reviewVersion is the tiles package's synthetic review as version n of
// compositionKey, which converts to tiles.
func reviewVersion(t *testing.T, n int) Version {
	t.Helper()
	doc := tileviewtest.Base(t)
	r := doc["review"].(map[string]any)
	r["version"] = n
	delete(r, "previous_version")
	raw := tileviewtest.Encode(t, doc)
	return Version{Metadata: Metadata{Version: n, Head: "revision", BundleDigest: "digest", Backend: "claude", PromptVersion: "prompt"}, Document: raw}
}

func columnNames(t *testing.T, s *Store) []string {
	t.Helper()
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('versions') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestPutStoresANormalisedCompositionAndReadsReturnIt(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	raw, pinned := tileviewtest.PinnedComposition(t, v.Document)
	v.Composition = raw
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() (Version, error){
		func() (Version, error) { return s.Latest(ctx, compositionKey) },
		func() (Version, error) { return s.Get(ctx, compositionKey, 1) },
	} {
		got, err := read()
		if err != nil {
			t.Fatal(err)
		}
		c, err := tiles.ParseComposition(got.Composition)
		if err != nil {
			t.Fatal(err)
		}
		// The stored composition is normalised: it lists every tile, and the
		// pin the writer gave is kept.
		if len(c.Layout.Entries) < 3 || c.Layout.Entries[0].Tile != pinned && !pinnedEntry(c, pinned) {
			t.Errorf("entries %v", c.Layout.Entries)
		}
		// Normalising it again changes nothing.
		normalized, err := review.NormalizeStored(got.Document)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := tiles.FromReview(normalized, tiles.Options{})
		if err != nil {
			t.Fatal(err)
		}
		again, report := tiles.NormalizeComposition(doc, c)
		encoded, _ := json.Marshal(again)
		if report.Total != 0 || !bytes.Equal(encoded, got.Composition) {
			t.Errorf("the stored composition is not a fixed point (%d fixes)", report.Total)
		}
	}
}

func pinnedEntry(c *tiles.Composition, id string) bool {
	for _, e := range c.Layout.Entries {
		if e.Tile == id && e.Pin {
			return true
		}
	}
	return false
}

// A composition a model wrote is repaired before it is stored, and the
// generator the host set is kept.
func TestPutNormalisesWhatAModelWrote(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	normalized, err := review.NormalizeStored(v.Document)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := tilestest.RandomComposition(rand.New(rand.NewPCG(7, 1)), converted, true)
	c.Layout.Entries = append(c.Layout.Entries, tiles.Entry{Tile: "no_such_tile", Size: tiles.SizeFull})
	c.Generator = &tiles.Generator{PromptVersion: "tiles-1", Model: "example-model"}
	if v.Composition, err = json.Marshal(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(ctx, compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := tiles.ParseComposition(got.Composition)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Generator == nil || stored.Generator.PromptVersion != "tiles-1" {
		t.Errorf("generator %+v", stored.Generator)
	}
	for _, e := range stored.Layout.Entries {
		if e.Tile == "no_such_tile" {
			t.Error("an entry naming no tile was stored")
		}
	}
	want, report := tiles.NormalizeComposition(converted, c)
	if report.Total == 0 {
		t.Fatal("the hostile composition needed no repair")
	}
	// The hidden list is the host's, kept as given.
	want.Layout.Hidden = c.Layout.Hidden
	if err := want.SetGenerator(*c.Generator); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(want)
	if !bytes.Equal(encoded, got.Composition) {
		t.Error("the stored composition is not the normalised one")
	}
}

func TestPutStoresNoCompositionWhenThereIsNothingToStore(t *testing.T) {
	ctx := context.Background()
	empty := `{"schema":"baum.composition/1","layout":{"entries":[]}}`
	for name, composition := range map[string]string{"none": "", "null": "null", "empty": empty} {
		s := opened(t)
		v := reviewVersion(t, 1)
		v.Composition = []byte(composition)
		if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := s.Latest(ctx, compositionKey); err != nil || got.Composition != nil {
			t.Errorf("%s: composition %s, %v", name, got.Composition, err)
		}
	}
	// A review that does not convert to tiles keeps its place in the history
	// without the composition.
	s := opened(t)
	key := Key{"owner/repo", 7}
	v := version(key, 1)
	raw, _ := tileviewtest.PinnedComposition(t, reviewVersion(t, 1).Document)
	v.Composition = raw
	before := workstats.Take().Counters["store.compositions_dropped"]
	if err := s.Put(ctx, key, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Latest(ctx, key); err != nil || got.Composition != nil || got.Metadata.Version != 1 {
		t.Errorf("composition %s, %v", got.Composition, err)
	}
	if workstats.Take().Counters["store.compositions_dropped"] != before+1 {
		t.Error("the dropped composition was not counted")
	}
}

func TestPutRefusesWhatIsNotAComposition(t *testing.T) {
	ctx := context.Background()
	for _, composition := range []string{`{"schema":"baum.composition/2","layout":{"entries":[]}}`, `not json`, `[]`} {
		s := opened(t)
		v := reviewVersion(t, 1)
		v.Composition = []byte(composition)
		if err := s.Put(ctx, compositionKey, Version{}, v); !errors.Is(err, ErrComposition) {
			t.Errorf("%s: %v", composition, err)
		}
		if _, err := s.Latest(ctx, compositionKey); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: a version was stored: %v", composition, err)
		}
	}
	// A generator the schema would not accept is refused too.
	s := opened(t)
	v := reviewVersion(t, 1)
	raw, _ := tileviewtest.PinnedComposition(t, v.Document)
	var c tiles.Composition
	_ = json.Unmarshal(raw, &c)
	c.Generator = &tiles.Generator{Template: "Not A Template"}
	v.Composition, _ = json.Marshal(c)
	if err := s.Put(ctx, compositionKey, Version{}, v); !errors.Is(err, ErrComposition) {
		t.Errorf("generator: %v", err)
	}
}

// A refresh or a completion rewrites the document and leaves the composition,
// and a later version has its own.
func TestRefreshAndCompleteKeepTheComposition(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition, _ = tileviewtest.PinnedComposition(t, v.Document)
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	first, err := s.Latest(ctx, compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(first.Document, &doc)
	doc["summary"].(map[string]any)["title"] = "Refreshed title"
	refreshed, _ := json.Marshal(doc)
	if err := s.RefreshCurrent(ctx, compositionKey, first, refreshed); err != nil {
		t.Fatal(err)
	}
	after, err := s.Latest(ctx, compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(after.Document, first.Document) || !bytes.Equal(after.Composition, first.Composition) || after.Revision == first.Revision {
		t.Errorf("refresh: document changed %v, composition kept %v, revision changed %v", !bytes.Equal(after.Document, first.Document), bytes.Equal(after.Composition, first.Composition), after.Revision != first.Revision)
	}
	next := reviewVersion(t, 2)
	if err := s.Put(ctx, compositionKey, after, next); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Latest(ctx, compositionKey); err != nil || got.Composition != nil {
		t.Errorf("a version put without one has %s, %v", got.Composition, err)
	}
	if got, err := s.Get(ctx, compositionKey, 1); err != nil || !bytes.Equal(got.Composition, first.Composition) {
		t.Errorf("version 1 lost its composition: %v", err)
	}
}

// Any write to a version's row renews its revision, the composition's included,
// so an answer kept under a revision is never served for another composition.
func TestWritingTheCompositionRenewsTheRevision(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	_, before, err := s.Revision(ctx, compositionKey, 1)
	if err != nil || before == 0 {
		t.Fatal(before, err)
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE versions SET composition=?`, `{"schema":"baum.composition/1"}`); err != nil {
		t.Fatal(err)
	}
	if _, after, err := s.Revision(ctx, compositionKey, 1); err != nil || after == before {
		t.Errorf("revision %d before and %d after: %v", before, after, err)
	}
}

// What the column holds is returned as written, even when unusable: the
// reader (tileview) ignores it.
func TestReadReturnsAnUnusableCompositionAsWritten(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	if err := s.Put(ctx, compositionKey, Version{}, reviewVersion(t, 1)); err != nil {
		t.Fatal(err)
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE versions SET composition='not json'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Latest(ctx, compositionKey); err != nil || string(got.Composition) != "not json" {
		t.Errorf("%s, %v", got.Composition, err)
	}
}

func TestMetadataTilesFieldsAreOptional(t *testing.T) {
	raw, err := json.Marshal(Metadata{Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tiles", "tiles_prompt_version", "tiles_prompt_digest"} {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if _, ok := m[name]; ok {
			t.Errorf("a version without the tiles stage writes %s", name)
		}
	}
	full, _ := json.Marshal(Metadata{Version: 2, Tiles: true, TilesPromptVersion: "v1", TilesPromptDigest: "sha256:x"})
	var back Metadata
	if err := json.Unmarshal(full, &back); err != nil || !back.Tiles || back.TilesPromptVersion != "v1" || back.TilesPromptDigest != "sha256:x" {
		t.Errorf("%+v %v", back, err)
	}
	// Metadata stored by a build that knew nothing of them decodes.
	if err := json.Unmarshal([]byte(`{"version":1,"head":"h"}`), &back); err != nil {
		t.Fatal(err)
	}
}

// The schema and statements of the build before compositions existed, as it
// wrote them.
const (
	oldVersionsTable = `CREATE TABLE IF NOT EXISTS versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version))`
	oldInsert        = `INSERT INTO versions(repository,pr,version,metadata,document) SELECT ?,?,?,?,? WHERE COALESCE((SELECT MAX(version) FROM versions WHERE repository=? AND pr=?),0)=?`
	oldSelect        = `SELECT v.metadata,v.document,0 FROM versions v WHERE v.repository=? AND v.pr=? ORDER BY v.version DESC LIMIT 1`
	oldVersions      = `SELECT metadata FROM versions WHERE repository=? AND pr=? ORDER BY version DESC`
	oldUpdate        = `UPDATE versions SET document=? WHERE repository=? AND pr=? AND version=? AND document=?`
)

func rawDatabase(t *testing.T, s *Store, mode string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(s.path())+"?mode="+mode+"&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Rollback: an older build opening a store a newer one wrote runs only its own
// statements, and they all work on the wider table. What it writes has no
// composition, and the newer build reads that as the default layout.
func TestOlderBuildWorksOnAStoreWithTheColumn(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition, _ = tileviewtest.PinnedComposition(t, v.Document)
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if names := columnNames(t, s); names[len(names)-1] != "composition" {
		t.Fatalf("columns %v", names)
	}
	db := rawDatabase(t, s, "rw")
	// Its CREATE TABLE IF NOT EXISTS is a no-op on the wider table.
	if _, err := db.Exec(oldVersionsTable); err != nil {
		t.Fatal(err)
	}
	// Its reads name their columns and ignore the new one.
	var m, doc []byte
	var revision int64
	if err := db.QueryRow(oldSelect, "acme/widgets", 42).Scan(&m, &doc, &revision); err != nil || !bytes.Equal(doc, mustStored(t, v)) {
		t.Fatalf("select: %v", err)
	}
	var metadata []byte
	if err := db.QueryRow(oldVersions, "acme/widgets", 42).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	// Its refresh updates the document, with the triggers of this build
	// still firing.
	if _, err := db.Exec(oldUpdate, doc, "acme/widgets", 42, 1, doc); err != nil {
		t.Fatal(err)
	}
	// Its generation inserts the next version without the column.
	next := reviewVersion(t, 2)
	meta, _ := json.Marshal(next.Metadata)
	if result, err := db.Exec(oldInsert, "acme/widgets", 42, 2, meta, next.Document, "acme/widgets", 42, 1); err != nil {
		t.Fatal(err)
	} else if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("insert wrote %d rows", n)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The newer build reads both: version 1 with its composition, the older
	// build's version 2 with none.
	got1, err := s.Get(ctx, compositionKey, 1)
	if err != nil || len(got1.Composition) == 0 {
		t.Fatalf("version 1: %s %v", got1.Composition, err)
	}
	got2, err := s.Latest(ctx, compositionKey)
	if err != nil || got2.Metadata.Version != 2 || got2.Composition != nil {
		t.Fatalf("version 2: %d %s %v", got2.Metadata.Version, got2.Composition, err)
	}
	if got2.Revision == 0 {
		t.Error("the older build's insert has no revision")
	}
}

func mustStored(t *testing.T, v Version) []byte {
	t.Helper()
	stored, _, err := review.StripHunkEvidence(v.Document)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// An older store, without the column, is migrated by the first writable open
// without losing a row, and an open that cannot write reads it as having no
// compositions and leaves its bytes alone.
func TestOlderStoreIsMigratedOrReadAsHavingNoComposition(t *testing.T) {
	ctx := context.Background()
	build := func() *Store {
		root := t.TempDir()
		s := &Store{root: root}
		f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		db := rawDatabase(t, s, "rw")
		if _, err := db.Exec(oldVersionsTable); err != nil {
			t.Fatal(err)
		}
		v := reviewVersion(t, 1)
		meta, _ := json.Marshal(v.Metadata)
		if _, err := db.Exec(oldInsert, "acme/widgets", 42, 1, meta, v.Document, "acme/widgets", 42, 0); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		return s
	}

	// Read-only: no composition, no change to the file.
	old := build()
	before, err := os.ReadFile(old.path())
	if err != nil {
		t.Fatal(err)
	}
	read, err := Open("", old.Root(), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := read.Latest(ctx, compositionKey)
	if err != nil || got.Composition != nil || got.Metadata.Version != 1 {
		t.Fatalf("read of an old store: %s %v", got.Composition, err)
	}
	if _, err := read.Get(ctx, compositionKey, 1); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(old.path()); !bytes.Equal(before, after) {
		t.Error("a read changed the store")
	}
	if names := columnNames(t, read); len(names) != 5 {
		t.Errorf("a read added a column: %v", names)
	}

	// A write on a store that cannot take the column: the version is stored
	// without its composition, which is counted.
	v2 := reviewVersion(t, 2)
	v2.Composition, _ = tileviewtest.PinnedComposition(t, v2.Document)
	dropped := workstats.Take().Counters["store.compositions_dropped"]
	// A handle that never ran the migration, as one on a store that cannot be
	// altered is.
	if err := (&Store{root: old.Root()}).Put(ctx, compositionKey, mustLatest(t, read), v2); err != nil {
		t.Fatal(err)
	}
	if got, err := read.Latest(ctx, compositionKey); err != nil || got.Metadata.Version != 2 || got.Composition != nil {
		t.Fatalf("version 2: %s %v", got.Composition, err)
	}
	if workstats.Take().Counters["store.compositions_dropped"] != dropped+1 {
		t.Error("the composition left out was not counted")
	}

	// A writable open migrates, keeping every row.
	migrated := build()
	w, err := Open("", migrated.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	if names := columnNames(t, w); names[len(names)-1] != "composition" || len(names) != 6 {
		t.Fatalf("columns %v", names)
	}
	again, err := Open("", migrated.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	_ = again
	v := reviewVersion(t, 2)
	v.Composition, _ = tileviewtest.PinnedComposition(t, v.Document)
	if err := w.Put(ctx, compositionKey, mustLatest(t, w), v); err != nil {
		t.Fatal(err)
	}
	one, err := w.Get(ctx, compositionKey, 1)
	if err != nil || one.Composition != nil {
		t.Fatalf("version 1: %s %v", one.Composition, err)
	}
	if two, err := w.Get(ctx, compositionKey, 2); err != nil || len(two.Composition) == 0 {
		t.Fatalf("version 2: %s %v", two.Composition, err)
	}
}

func mustLatest(t *testing.T, s *Store) Version {
	t.Helper()
	v, err := s.Latest(context.Background(), compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
