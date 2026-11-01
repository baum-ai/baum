package store

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
)

func dropTriggers(db *sql.DB) error {
	for _, name := range []string{"insert", "insert_invalid", "update", "update_invalid", "delete"} {
		if _, err := db.Exec(`DROP TRIGGER summaries_` + name); err != nil {
			return err
		}
	}
	return nil
}

func TestTriggersProjectWritesFromOlderBinaries(t *testing.T) {
	s := opened(t)
	k := Key{"a/b", 1}
	putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"closed"`, `"title":"same","review_minutes":10`))
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// An older binary refreshes in place with a document of the same length.
	if _, err = db.Exec(`UPDATE versions SET document=? WHERE repository='a/b' AND pr=1`, docFor(k, 1, `,"pr_state":"merged"`, `"title":"same","review_minutes":10`)); err != nil {
		t.Fatal(err)
	}
	if rows := snapshotRows(t, s); len(rows) != 1 || rows[0].State != "merged" || !reflect.DeepEqual(rows, oldRows(t, s)) || !strings.Contains(summaryRows(t, s)[k], "merged") {
		t.Fatalf("same-length refresh not projected: %+v %v", rows, summaryRows(t, s))
	}
	// ... appends a new version.
	if _, err = db.Exec(`INSERT INTO versions VALUES ('a/b',1,2,'{"version":2}',?)`, docFor(k, 2, `,"pr_state":"open"`, `"title":"v2","review_minutes":20`)); err != nil {
		t.Fatal(err)
	}
	rows := snapshotRows(t, s)
	if len(rows) != 1 || rows[0].Title != "v2" || rows[0].Count != 2 || !strings.HasPrefix(summaryRows(t, s)[k], "2 ") {
		t.Fatalf("insert not projected: %+v", rows)
	}
	// A write to a version that is not the latest changes nothing.
	if _, err = db.Exec(`INSERT INTO versions VALUES ('a/b',1,0,'{"version":0}',?)`, docFor(k, 0, ``, `"title":"ancient"`)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE versions SET document=? WHERE repository='a/b' AND pr=1 AND version=1`, docFor(k, 1, ``, `"title":"edited old"`)); err != nil {
		t.Fatal(err)
	}
	if rows := snapshotRows(t, s); rows[0].Title != "v2" || !reflect.DeepEqual(rows, oldRows(t, s)) {
		t.Fatalf("non-latest write changed the projection: %+v", rows)
	}
	// Deleting the latest recomputes from the previous version; deleting all drops the row.
	if _, err = db.Exec(`DELETE FROM versions WHERE repository='a/b' AND pr=1 AND version=2`); err != nil {
		t.Fatal(err)
	}
	if rows := snapshotRows(t, s); rows[0].Title != "edited old" || !reflect.DeepEqual(rows, oldRows(t, s)) {
		t.Fatalf("delete not recomputed: %+v", rows)
	}
	if _, err = db.Exec(`DELETE FROM versions`); err != nil {
		t.Fatal(err)
	}
	if got := summaryRows(t, s); len(got) != 0 {
		t.Fatalf("summaries remain: %v", got)
	}
}

func TestDocumentSQLiteCannotParseStillWrites(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"a/b", 1}
	deep := `"deep":` + strings.Repeat("[", 1500) + strings.Repeat("]", 1500)
	doc := func(n int, state string) []byte {
		return docFor(k, n, `,"pr_state":"`+state+`"`, `"title":"t",`+deep)
	}
	v1 := putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"fine"`))
	if len(summaryRows(t, s)) != 1 {
		t.Fatal("no projection")
	}
	if err := s.RefreshCurrent(ctx, k, v1, doc(1, "merged")); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if got := summaryRows(t, s); len(got) != 0 {
		t.Fatalf("stale projection kept: %v", got)
	}
	// Snapshot behaves as it did before summaries: the extraction decides.
	var oldErr error
	db, _ := s.database(false)
	rows, oldErr := db.Query(oldExtraction)
	if oldErr == nil {
		for rows.Next() {
		}
		oldErr = rows.Err()
		_ = rows.Close()
	}
	_ = db.Close()
	if _, err := s.Snapshot(ctx); (oldErr == nil) != (err == nil) {
		t.Fatalf("old extraction err=%v, snapshot err=%v", oldErr, err)
	}
	// A new, parseable version projects again.
	v1.Document = doc(1, "merged")
	putDoc(t, s, k, v1, docFor(k, 2, `,"pr_state":"open"`, `"title":"back"`))
	if got := snapshotRows(t, s); got[0].Title != "back" {
		t.Fatalf("%+v", got)
	}
}

func TestSnapshotUsesProjectionWhenCurrent(t *testing.T) {
	s := opened(t)
	k := Key{"a/b", 1}
	putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"real"`))
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE summaries SET title='sentinel'`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if rows := snapshotRows(t, s); rows[0].Title != "sentinel" {
		t.Fatalf("projection not used: %+v", rows)
	}
}

func TestNoopWriteOpenDoesNotWrite(t *testing.T) {
	s := opened(t)
	k := Key{"a/b", 1}
	putDoc(t, s, k, Version{}, docFor(k, 1, "", `"title":"x"`))
	before, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open("", s.Root(), true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.path())
	if string(before) != string(after) {
		t.Fatal("a write-open of a current store changed the file")
	}
}

// BenchmarkNoopBackfill shows a write-open of a current store reads no document
// bodies: with large documents it stays at millisecond cost.
func BenchmarkNoopBackfill(b *testing.B) {
	s, err := Open("", b.TempDir(), true)
	if err != nil {
		b.Fatal(err)
	}
	pad := strings.Repeat("x", 1<<20)
	for pr := 1; pr <= 50; pr++ {
		k := Key{"a/b", pr}
		v := version(k, 1)
		v.Document = docFor(k, 1, "", `"title":"big","pad":"`+pad+`"`)
		if err := s.Put(context.Background(), k, Version{}, v); err != nil {
			b.Fatal(err)
		}
	}
	db, err := s.database(true)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	b.ReportAllocs()
	for b.Loop() {
		if err := backfill(context.Background(), db); err != nil {
			b.Fatal(err)
		}
	}
}
