package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// oldExtraction is Snapshot's query from before summaries existed.
const oldExtraction = `SELECT v.repository,v.pr,COALESCE(json_extract(v.document,'$.summary.title'),''),COALESCE(json_extract(v.document,'$.review.pr_state'),''),json_extract(v.document,'$.summary.risk_score'),CASE WHEN json_type(v.document,'$.summary.review_minutes') IN ('integer','real') AND json_extract(v.document,'$.summary.review_minutes') BETWEEN 1 AND 240 AND CAST(json_extract(v.document,'$.summary.review_minutes') AS INTEGER)=json_extract(v.document,'$.summary.review_minutes') THEN CAST(json_extract(v.document,'$.summary.review_minutes') AS INTEGER) END,summary.n FROM versions v JOIN (SELECT repository,pr,MAX(version) latest,COUNT(*) n FROM versions GROUP BY repository,pr) summary ON v.repository=summary.repository AND v.pr=summary.pr AND v.version=summary.latest ORDER BY v.repository,v.pr`

type projectedRow struct {
	Key     Key
	Title   string
	State   string
	Risk    string
	HasRisk bool
	Minutes int
	Count   int
}

func oldRows(t *testing.T, s *Store) []projectedRow {
	t.Helper()
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(oldExtraction)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []projectedRow
	for rows.Next() {
		var r projectedRow
		var risk sql.NullString
		var minutes sql.NullInt64
		if err := rows.Scan(&r.Key.Repository, &r.Key.Number, &r.Title, &r.State, &risk, &minutes, &r.Count); err != nil {
			t.Fatal(err)
		}
		r.Risk, r.HasRisk, r.Minutes = risk.String, risk.Valid, int(minutes.Int64)
		if !minutes.Valid {
			r.Minutes = -1
		}
		out = append(out, r)
	}
	return out
}

func snapshotRows(t *testing.T, s *Store) []projectedRow {
	t.Helper()
	got, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []projectedRow
	for _, g := range got {
		r := projectedRow{Key: g.Key, Title: g.Title, State: g.PRState, Risk: string(g.RiskScore), HasRisk: g.RiskScore != nil, Minutes: -1, Count: g.VersionCount}
		if g.ReviewMinutes != nil {
			r.Minutes = *g.ReviewMinutes
		}
		out = append(out, r)
	}
	return out
}

func docFor(k Key, n int, review, summary string) []byte {
	return []byte(fmt.Sprintf(`{"review":{"repository":%q,"pull_request":%d,"version":%d%s},"summary":{%s}}`, k.documentRepository(), k.Number, n, review, summary))
}

func putDoc(t *testing.T, s *Store, k Key, previous Version, doc []byte) Version {
	t.Helper()
	next := version(k, previous.Metadata.Version+1)
	next.Document = doc
	if err := s.Put(context.Background(), k, previous, next); err != nil {
		t.Fatal(err)
	}
	return next
}

func summaryRows(t *testing.T, s *Store) map[Key]string {
	t.Helper()
	db, err := s.database(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT repository,pr,version,document_length,title,pr_state,COALESCE(risk_score,'NULL'),COALESCE(review_minutes,-1) FROM summaries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[Key]string{}
	for rows.Next() {
		var k Key
		var v, l, m int
		var title, state, risk string
		if err := rows.Scan(&k.Repository, &k.Number, &v, &l, &title, &state, &risk, &m); err != nil {
			t.Fatal(err)
		}
		out[k] = fmt.Sprint(v, l, title, state, risk, m)
	}
	return out
}

func TestSummaryEquivalenceWithExtraction(t *testing.T) {
	cases := []struct{ name, review, summary string }{
		{"complete", `,"pr_state":"open"`, `"title":"T","risk_score":{"score": 4},"review_minutes":30`},
		{"missing title and state", ``, `"review_minutes":5`},
		{"minutes zero", ``, `"title":"a","review_minutes":0`},
		{"minutes one", ``, `"title":"a","review_minutes":1`},
		{"minutes 240", ``, `"title":"a","review_minutes":240`},
		{"minutes 241", ``, `"title":"a","review_minutes":241`},
		{"minutes 30.0", ``, `"title":"a","review_minutes":30.0`},
		{"minutes 30.5", ``, `"title":"a","review_minutes":30.5`},
		{"minutes string", ``, `"title":"a","review_minutes":"30"`},
		{"minutes null", ``, `"title":"a","review_minutes":null`},
		{"minutes negative", ``, `"title":"a","review_minutes":-3`},
		{"risk absent", ``, `"title":"a"`},
		{"risk null", ``, `"title":"a","risk_score":null`},
		{"risk object", ``, `"title":"a","risk_score":{"rubric":"v1","score":7,"reasons":["x"]}`},
		{"risk number", ``, `"title":"a","risk_score":3`},
		{"numeric title", `,"pr_state":7`, `"title":12`},
		{"unicode title", `,"pr_state":"merged"`, `"title":"héllo é \"q\""`},
	}
	for _, mode := range []string{"projected", "backfilled", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			s := opened(t)
			for i, c := range cases {
				k := Key{"acme/widgets", i + 1}
				v1 := putDoc(t, s, k, Version{}, docFor(k, 1, "", `"title":"old"`))
				putDoc(t, s, k, v1, docFor(k, 2, c.review, c.summary))
			}
			db, err := s.database(true)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "backfilled":
				if _, err = db.Exec(`DELETE FROM summaries`); err == nil {
					err = backfill(context.Background(), db)
				}
			case "fallback":
				_, err = db.Exec(`DROP TABLE summaries`)
			}
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			want, got := oldRows(t, s), snapshotRows(t, s)
			if len(want) != len(cases) || !reflect.DeepEqual(want, got) {
				t.Fatalf("old=%+v\nnew=%+v", want, got)
			}
		})
	}
}

func TestSnapshotRejectsInvalidRiskLikeExtraction(t *testing.T) {
	ctx := context.Background()
	for _, drop := range []bool{false, true} {
		s := opened(t)
		k := Key{"a/b", 1}
		putDoc(t, s, k, Version{}, docFor(k, 1, "", `"risk_score":"not json"`))
		if drop {
			db, _ := s.database(true)
			_, _ = db.Exec(`DROP TABLE summaries`)
			_ = db.Close()
		}
		if _, err := s.Snapshot(ctx); err == nil || err.Error() != "invalid stored risk assessment" {
			t.Fatalf("drop=%v err=%v", drop, err)
		}
	}
}

func TestPutRefreshCompleteMaintainProjection(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"acme/widgets", 3}
	check := func(title, state string, minutes int, count int) {
		t.Helper()
		rows := snapshotRows(t, s)
		if len(rows) != 1 || rows[0].Title != title || rows[0].State != state || rows[0].Minutes != minutes || rows[0].Count != count {
			t.Fatalf("snapshot=%+v", rows)
		}
		if !reflect.DeepEqual(rows, oldRows(t, s)) {
			t.Fatalf("snapshot %+v differs from extraction %+v", rows, oldRows(t, s))
		}
		stored := summaryRows(t, s)
		if len(stored) != 1 || !strings.Contains(stored[k], title) {
			t.Fatalf("projection not maintained: %v", stored)
		}
	}
	v1 := putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"one","review_minutes":10`))
	check("one", "open", 10, 1)
	refreshed := docFor(k, 1, `,"pr_state":"merged"`, `"title":"refreshed","review_minutes":20`)
	if err := s.RefreshCurrent(ctx, k, v1, refreshed); err != nil {
		t.Fatal(err)
	}
	check("refreshed", "merged", 20, 1)
	v1.Document = refreshed
	completed := docFor(k, 1, `,"pr_state":"closed"`, `"title":"completed","review_minutes":300`)
	if err := s.CompleteCurrent(ctx, k, v1, completed); err != nil {
		t.Fatal(err)
	}
	check("completed", "closed", -1, 1)
	v1.Document = completed
	putDoc(t, s, k, v1, docFor(k, 2, `,"pr_state":"open"`, `"title":"two","review_minutes":45`))
	check("two", "open", 45, 2)
	if got := summaryRows(t, s)[k]; !strings.HasPrefix(got, "2 ") {
		t.Fatal(got)
	}
}

func TestFailedWritesLeaveProjectionUnchanged(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"acme/widgets", 4}
	v1 := putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"one","review_minutes":10`))
	before := summaryRows(t, s)
	stale := v1
	stale.Document = docFor(k, 1, `,"pr_state":"open"`, `"title":"someone else's"`)
	other := docFor(k, 1, `,"pr_state":"merged"`, `"title":"conflicting"`)
	if err := s.RefreshCurrent(ctx, k, stale, other); err == nil {
		t.Fatal("accepted refresh of changed bytes")
	}
	if err := s.CompleteCurrent(ctx, k, stale, other); err == nil {
		t.Fatal("accepted completion of changed bytes")
	}
	conflicting := version(k, 1)
	conflicting.Document = docFor(k, 1, "", `"title":"again"`)
	if err := s.Put(ctx, k, Version{}, conflicting); err == nil {
		t.Fatal("accepted put over existing history")
	}
	if got := summaryRows(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("projection changed: %v -> %v", before, got)
	}
	// A failing trigger rolls the document write back with the projection.
	db, _ := s.database(true)
	_, _ = db.Exec(`DROP TABLE summaries; CREATE TABLE summaries (x)`)
	_ = db.Close()
	if err := s.RefreshCurrent(ctx, k, v1, docFor(k, 1, `,"pr_state":"merged"`, `"title":"new"`)); err == nil {
		t.Fatal("projection failure was ignored")
	}
	next := version(k, 2)
	next.Document = docFor(k, 2, "", `"title":"two"`)
	if err := s.Put(ctx, k, v1, next); err == nil {
		t.Fatal("projection failure was ignored by put")
	}
	latest, err := s.Latest(ctx, k)
	if err != nil || latest.Metadata.Version != 1 || string(latest.Document) != string(v1.Document) {
		t.Fatalf("write persisted although the projection failed: %s %v", latest.Document, err)
	}
}

const oldSchema = `CREATE TABLE versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version)); CREATE TABLE runs (id INTEGER PRIMARY KEY AUTOINCREMENT, repository TEXT NOT NULL, pr INTEGER NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL, started INTEGER NOT NULL, expires INTEGER NOT NULL, finished INTEGER NOT NULL);`

// oldStore creates a store the way a binary before summaries did.
func oldStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{root: t.TempDir()}
	if err := os.WriteFile(s.path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Stores older binaries wrote use the rollback journal.
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	for pr := 1; pr <= 3; pr++ {
		k := Key{"acme/widgets", pr}
		for n := 1; n <= pr; n++ {
			meta := fmt.Sprintf(`{"version":%d}`, n)
			doc := docFor(k, n, `,"pr_state":"open"`, fmt.Sprintf(`"title":"pr %d v%d","review_minutes":%d`, pr, n, pr*10+n))
			if _, err := db.Exec(`INSERT INTO versions VALUES (?,?,?,?,?)`, k.repository(), pr, n, []byte(meta), doc); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s
}

func TestWriteOpenBackfillsOldStore(t *testing.T) {
	old := oldStore(t)
	want := oldRows(t, old)
	reopened, err := Open("", old.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryRows(t, reopened); len(got) != 3 {
		t.Fatalf("backfill: %v", got)
	}
	if got := snapshotRows(t, reopened); !reflect.DeepEqual(got, want) || len(got) != 3 || got[2].Title != "pr 3 v3" || got[2].Minutes != 33 {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	before := summaryRows(t, reopened)
	if _, err = Open("", old.Root(), true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, summaryRows(t, reopened)) {
		t.Fatal("second open changed summaries")
	}
}

func TestReadOnlyOpenOfOldStoreDoesNotWrite(t *testing.T) {
	old := oldStore(t)
	want := oldRows(t, old)
	ro, err := Open("", old.Root(), false)
	if err != nil {
		t.Fatal(err)
	}
	state := func() (time.Time, []byte) {
		info, err := os.Stat(old.path())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(old.path())
		if err != nil {
			t.Fatal(err)
		}
		return info.ModTime(), raw
	}
	mtime, raw := state()
	got := snapshotRows(t, ro)
	mtime2, raw2 := state()
	if !reflect.DeepEqual(got, want) || len(got) != 3 {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	if !mtime.Equal(mtime2) || string(raw) != string(raw2) {
		t.Fatal("read-only snapshot modified the store")
	}
	if _, err := os.Stat(old.path() + "-wal"); err == nil {
		t.Fatal("wal created")
	}
}

func TestStaleSummaryFallsBackToExtraction(t *testing.T) {
	s := opened(t)
	k1, k2, k3 := Key{"a/b", 1}, Key{"a/b", 2}, Key{"a/b", 3}
	for _, k := range []Key{k1, k2, k3} {
		putDoc(t, s, k, Version{}, docFor(k, 1, `,"pr_state":"open"`, `"title":"original","review_minutes":10`))
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	// Without triggers (a store no new binary has write-opened), an older binary
	// appends a version to PR 1 and rewrites PR 2's document in place; neither
	// touches summaries.
	err = dropTriggers(db)
	if err == nil {
		_, err = db.Exec(`INSERT INTO versions VALUES ('a/b',1,2,'{"version":2}',?)`, docFor(k1, 2, `,"pr_state":"merged"`, `"title":"newer","review_minutes":99`))
	}
	if err == nil {
		_, err = db.Exec(`UPDATE versions SET document=? WHERE repository='a/b' AND pr=2`, docFor(k2, 1, `,"pr_state":"closed"`, `"title":"rewritten, longer","review_minutes":77`))
	}
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := snapshotRows(t, s)
	want := []projectedRow{
		{Key: k1, Title: "newer", State: "merged", Minutes: 99, Count: 2},
		{Key: k2, Title: "rewritten, longer", State: "closed", Minutes: 77, Count: 1},
		{Key: k3, Title: "original", State: "open", Minutes: 10, Count: 1},
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, oldRows(t, s)) {
		t.Fatalf("got=%+v", got)
	}
	// The next write-open repairs the stale rows.
	if _, err = Open("", s.Root(), true); err != nil {
		t.Fatal(err)
	}
	if rows := summaryRows(t, s); !strings.HasPrefix(rows[k1], "2 ") || !strings.Contains(rows[k2], "rewritten") {
		t.Fatalf("not repaired: %v", rows)
	}
}
