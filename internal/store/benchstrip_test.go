package store_test

import (
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/benchcorpus"
	"github.com/baum-ai/baum/review"
)

// TestBenchCorpusStripped measures what a new publish stores: it copies the
// corpus store to a temporary directory (the source is only read), rewrites
// every version's document through review.StripHunkEvidence, and reports the
// document bytes and database file size before and after, each after a
// VACUUM of its own copy so the file sizes compare fairly. Runs only when
// BAUM_BENCH_STORE names a store:
//
//	go test -run TestBenchCorpusStripped -v ./internal/store/
func TestBenchCorpusStripped(t *testing.T) {
	if os.Getenv(benchcorpus.Env) == "" {
		t.Skip("set " + benchcorpus.Env)
	}
	root, label := benchcorpus.Root(t)
	open := func(path string) *sql.DB {
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()+"?_pragma=busy_timeout(10000)")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	copyTo := func(name string) string {
		src, err := os.Open(filepath.Join(root, "versions.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		path := filepath.Join(t.TempDir(), name)
		dst, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		if err := dst.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	size := func(path string) int64 {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	const latest = ` FROM versions v JOIN (SELECT repository,pr,MAX(version) latest FROM versions GROUP BY repository,pr) l ON v.repository=l.repository AND v.pr=l.pr AND v.version=l.latest`
	docBytes := func(db *sql.DB, from string) (n int64) {
		if err := db.QueryRow(`SELECT COALESCE(SUM(length(CAST(v.document AS BLOB))),0)` + from).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	plainPath := copyTo("plain.sqlite")
	plain := open(plainPath)
	beforeLatest, beforeAll := docBytes(plain, latest), docBytes(plain, ` FROM versions v`)
	fileBefore := size(plainPath)
	if _, err := plain.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	_ = plain.Close()
	vacuumedBefore := size(plainPath)

	strippedPath := copyTo("stripped.sqlite")
	stripped := open(strippedPath)
	rows, err := stripped.Query(`SELECT repository,pr,version,document FROM versions`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		repository  string
		pr, version int
		document    []byte
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.repository, &r.pr, &r.version, &r.document); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	changed := 0
	for _, r := range all {
		out, did, err := review.StripHunkEvidence(r.document)
		if err != nil {
			t.Fatalf("%s#%d v%d: %v", r.repository, r.pr, r.version, err)
		}
		if did {
			changed++
		}
		if _, err := stripped.Exec(`UPDATE versions SET document=? WHERE repository=? AND pr=? AND version=?`, out, r.repository, r.pr, r.version); err != nil {
			t.Fatal(err)
		}
	}
	afterLatest, afterAll := docBytes(stripped, latest), docBytes(stripped, ` FROM versions v`)
	if _, err := stripped.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	_ = stripped.Close()
	after := size(strippedPath)

	saved := func(before, after int64) float64 { return 100 * float64(before-after) / float64(before) }
	t.Logf("corpus=%s versions=%d rewritten=%d", label, len(all), changed)
	t.Logf("document_bytes latest before=%d after=%d saved=%.1f%%", beforeLatest, afterLatest, saved(beforeLatest, afterLatest))
	t.Logf("document_bytes all    before=%d after=%d saved=%.1f%%", beforeAll, afterAll, saved(beforeAll, afterAll))
	t.Logf("db_file_bytes original=%d vacuumed_before=%d vacuumed_after=%d saved_vs_vacuumed=%.1f%% saved_vs_original=%.1f%%",
		fileBefore, vacuumedBefore, after, saved(vacuumedBefore, after), saved(fileBefore, after))
}
