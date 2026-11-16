package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
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
	v.Composition = tileviewtest.ViewComposition(t)
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
		c, err := view.Parse(got.Composition)
		if err != nil {
			t.Fatal(err)
		}
		if c.Overview.RiskSummary != tileviewtest.RiskSummary {
			t.Errorf("stored %s", got.Composition)
		}
		// Normalising it again changes nothing.
		normalized, err := review.NormalizeStored(got.Document)
		if err != nil {
			t.Fatal(err)
		}
		r, err := view.ParseReview(normalized)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := tiles.FromReview(normalized, tiles.Options{})
		if err != nil {
			t.Fatal(err)
		}
		again, report := view.Normalize(r, doc, c)
		encoded, _ := json.Marshal(again)
		if report.Total() != 0 || !bytes.Equal(encoded, got.Composition) {
			t.Errorf("the stored composition is not a fixed point (%d fixes)", report.Total())
		}
	}
}

// A review view composition is checked against the document and stored
// with the caller's generator: what the document cannot take is dropped
// item by item, never the whole composition. One that chooses nothing,
// without a generator, is stored as none.
func TestPutChecksAndStoresAReviewViewComposition(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition = []byte(`{"schema":"baum.composition/2","generator":{"model":"written by a model"},
	 "overview":{"risk_summary":"Retries can pile up.","verdict":null,"screenshots":["att_aa01","att_nope"],"diagram":null,"intent":[]},
	 "sections":[{"id":"sec_nope","tiles":["dg_x"]},{"id":"context","tiles":["dg_x"]}],
	 "diagrams":[{"id":"dg_x","type":"table","title":"Waits","data":{"source":[{"type":"hunk","id":"h_000000000001"},{"type":"hunk","id":"h_0000000000ff"}],"columns":["Try","Wait"],"rows":[["1","1 s"]]}}]}`)
	v.CompositionGenerator = &tiles.Generator{PromptVersion: "review-view-public-v1", Model: "stub"}
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	got := mustLatest(t, s)
	c, err := view.Parse(got.Composition)
	if err != nil {
		t.Fatalf("%v: %s", err, got.Composition)
	}
	if c.Generator == nil || c.Generator.Model != "stub" || c.Overview.RiskSummary == "" || !slices.Equal(c.Overview.Screenshots, []string{"att_aa01"}) {
		t.Fatalf("stored %s", got.Composition)
	}
	if len(c.Sections) != 1 || c.Sections[0].ID != "context" || len(c.Diagrams) != 1 || strings.Contains(string(c.Diagrams[0].Data), "h_0000000000ff") {
		t.Fatalf("stored %s", got.Composition)
	}
	if parsed := ParseComposition(got.Composition); parsed == nil || parsed.Overview.RiskSummary == "" {
		t.Fatalf("parsed %+v", parsed)
	}

	s = opened(t)
	v = reviewVersion(t, 1)
	v.Composition = []byte(`{"schema":"baum.composition/2","overview":{"screenshots":[],"intent":[]},"sections":[{"id":"sec_nope","tiles":["dg_x"]}]}`)
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if got := mustLatest(t, s); got.Composition != nil {
		t.Fatalf("an empty composition was stored: %s", got.Composition)
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
	v.Composition = tileviewtest.ViewComposition(t)
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

// A composition problem never costs the review its place in the history: the
// version is stored, without the composition when it is unusable, and without
// the generator when only that is refused; each is counted.
func TestACompositionProblemNeverCostsTheVersion(t *testing.T) {
	ctx := context.Background()
	count := func(name string) int64 { return workstats.Take().Counters[name] }
	for name, composition := range map[string][]byte{
		"other schema": []byte(`{"schema":"baum.composition/9","layout":{"entries":[]}}`),
		"tile grid":    []byte(`{"schema":"baum.composition/1","layout":{"entries":[{"tile":"overview","size":"full"}]}}`),
		"not json":     []byte(`not json`),
		"array":        []byte(`[]`),
		"too large":    append(append([]byte(`{"schema":"baum.composition/1","x":"`), bytes.Repeat([]byte("a"), tiles.MaxStoredComposition)...), `"}`...),
	} {
		s := opened(t)
		v := reviewVersion(t, 1)
		v.Composition = composition
		before := count("store.compositions_dropped")
		if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if got, err := s.Latest(ctx, compositionKey); err != nil || got.Composition != nil || got.Metadata.Version != 1 {
			t.Errorf("%s: version %d, composition %s, %v", name, got.Metadata.Version, got.Composition, err)
		}
		if count("store.compositions_dropped") != before+1 {
			t.Errorf("%s: not counted", name)
		}
	}
	// A generator the schema refuses is left off; the composition is kept.
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition = tileviewtest.ViewComposition(t)
	v.CompositionGenerator = &tiles.Generator{Problems: make([]string, 21)}
	before, dropped := count("store.composition_generators_dropped"), count("store.compositions_dropped")
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(ctx, compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	c := ParseComposition(got.Composition)
	if c == nil || c.Generator != nil || c.Overview.RiskSummary == "" {
		t.Fatalf("composition %s", got.Composition)
	}
	if count("store.composition_generators_dropped") != before+1 || count("store.compositions_dropped") != dropped {
		t.Error("the dropped generator was not counted on its own")
	}
}

// A tile grid's composition (baum.composition/1) is stored as none.
func TestATileGridCompositionIsStoredAsNone(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition = []byte(`{"schema":"baum.composition/1","layout":{"entries":[],"hidden":["media"]}}`)
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Latest(ctx, compositionKey); err != nil || got.Composition != nil {
		t.Errorf("composition %s, %v", got.Composition, err)
	}
}

// A refresh or a completion rewrites the document and leaves the composition,
// and a later version has its own.
func TestRefreshAndCompleteKeepTheComposition(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	v := reviewVersion(t, 1)
	v.Composition = tileviewtest.ViewComposition(t)
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

// The schema and statements of the build before compositions existed, as main
// wrote them: the load query with its revision join (revisionSelect, for a
// store whose triggers are current), the fenced refresh, and the insert and
// version list. Rows are returned by these, never by column position past
// the ones named.
const (
	oldVersionsTable = `CREATE TABLE IF NOT EXISTS versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version))`
	oldInsert        = `INSERT INTO versions(repository,pr,version,metadata,document) SELECT ?,?,?,?,? WHERE COALESCE((SELECT MAX(version) FROM versions WHERE repository=? AND pr=?),0)=?`
	oldSelect        = `SELECT v.metadata,v.document,COALESCE(r.revision,0) FROM versions v LEFT JOIN document_revisions r ON r.repository=v.repository AND r.pr=v.pr AND r.version=v.version WHERE v.repository=? AND v.pr=? ORDER BY v.version DESC LIMIT 1`
	oldVersions      = `SELECT metadata FROM versions WHERE repository=? AND pr=? ORDER BY version DESC`
	oldUpdate        = `UPDATE versions SET document=? WHERE repository=? AND pr=? AND version=? AND document=? AND version=(SELECT MAX(version) FROM versions WHERE repository=? AND pr=?)`
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
	v.Composition = tileviewtest.ViewComposition(t)
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
	// Its refresh updates the document, fenced as main's is, with the
	// triggers of this build still firing.
	if _, err := db.Exec(oldUpdate, doc, "acme/widgets", 42, 1, doc, "acme/widgets", 42); err != nil {
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

	// A write through a handle opened before the column existed adds it
	// first, so the generated layout is kept.
	v2 := reviewVersion(t, 2)
	v2.Composition = tileviewtest.ViewComposition(t)
	if err := (&Store{root: old.Root()}).Put(ctx, compositionKey, mustLatest(t, read), v2); err != nil {
		t.Fatal(err)
	}
	if got, err := read.Latest(ctx, compositionKey); err != nil || got.Metadata.Version != 2 || len(got.Composition) == 0 {
		t.Fatalf("version 2: %s %v", got.Composition, err)
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
	v.Composition = tileviewtest.ViewComposition(t)
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

func TestParseCompositionIsNilForNoneAndUnusable(t *testing.T) {
	raw := tileviewtest.ViewComposition(t)
	if ParseComposition(raw) == nil {
		t.Error("a stored composition did not parse")
	}
	huge := `{"schema":"baum.composition/2","x":"` + strings.Repeat("a", tiles.MaxStoredComposition) + `"}`
	for _, bad := range []string{"", "null", "not json", `{"schema":"baum.composition/9"}`, `{"schema":"baum.composition/1","layout":{"entries":[]}}`, huge} {
		if ParseComposition([]byte(bad)) != nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
