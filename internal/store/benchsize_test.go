package store_test

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/benchcorpus"
)

// TestBenchCorpusSize reports where the bytes of the benchmark corpus are. It
// runs when BAUM_BENCH_STORE names a store, or with BAUM_BENCH_SIZE=1 for the
// synthetic corpus, and always opens the database read-only:
//
//	go test -run TestBenchCorpusSize -v ./internal/store/
func TestBenchCorpusSize(t *testing.T) {
	if os.Getenv(benchcorpus.Env) == "" && os.Getenv("BAUM_BENCH_SIZE") == "" {
		t.Skip("set " + benchcorpus.Env + " or BAUM_BENCH_SIZE=1")
	}
	root, label := benchcorpus.Root(t)
	path := filepath.Join(root, "versions.sqlite")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	one := func(query string) (n int64) {
		t.Helper()
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	const latest = `FROM versions v JOIN (SELECT repository,pr,MAX(version) latest FROM versions GROUP BY repository,pr) l ON v.repository=l.repository AND v.pr=l.pr AND v.version=l.latest`
	bytes := func(expr string) string { return `COALESCE(SUM(length(CAST(` + expr + ` AS BLOB))),0)` }
	// A hunk field's bytes are its compact JSON text: diff is an object, so this
	// counts its re-serialized form, not the stored formatting.
	hunks := func(field string, from string) int64 {
		return one(`SELECT COALESCE(SUM(length(CAST(json_extract(h.value,'$.` + field + `') AS BLOB))),0) ` + from + `, json_each(v.document,'$.index.hunks') h`)
	}
	prs := one(`SELECT COUNT(*) FROM (SELECT DISTINCT repository,pr FROM versions)`)
	versions := one(`SELECT COUNT(*) FROM versions`)
	allDocs := one(`SELECT ` + bytes("document") + ` FROM versions`)
	latestDocs := one(`SELECT ` + bytes("v.document") + ` ` + latest)
	latestDiff := hunks("diff", latest)
	allDiff := hunks("diff", `FROM versions v`)
	latestURL := hunks("github_url", latest)
	allURL := hunks("github_url", `FROM versions v`)
	pct := func(part, whole int64) float64 {
		if whole == 0 {
			return 0
		}
		return 100 * float64(part) / float64(whole)
	}
	t.Logf("corpus=%s", label)
	t.Logf("db_file_bytes=%d", info.Size())
	t.Logf("prs=%d versions=%d", prs, versions)
	t.Logf("document_bytes latest=%d all=%d", latestDocs, allDocs)
	t.Logf("diff_bytes latest=%d (%.1f%%) all=%d (%.1f%%)", latestDiff, pct(latestDiff, latestDocs), allDiff, pct(allDiff, allDocs))
	t.Logf("github_url_bytes latest=%d all=%d", latestURL, allURL)
}
