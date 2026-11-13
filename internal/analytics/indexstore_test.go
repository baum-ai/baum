package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/session"
)

// storedRows returns the index rows in the database, by path.
func storedRows(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT path, entry FROM analytics_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var p, raw string
		if err := rows.Scan(&p, &raw); err != nil {
			t.Fatal(err)
		}
		out[p] = raw
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func execDB(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	db, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

// byPath decodes the stored rows, by session file path, and fails unless
// they are exactly the snapshot's entries.
func byPath(entries []Entry, rows map[string]string, t *testing.T) map[string]Entry {
	t.Helper()
	out := map[string]Entry{}
	var decoded []Entry
	for p, raw := range rows {
		var e Entry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		out[p] = e
		decoded = append(decoded, e)
	}
	bySession := func(a, b Entry) int { return strings.Compare(a.SessionID, b.SessionID) }
	snapshot := slices.Clone(entries)
	slices.SortFunc(snapshot, bySession)
	slices.SortFunc(decoded, bySession)
	if len(decoded) != len(snapshot) || (len(decoded) > 0 && !reflect.DeepEqual(decoded, snapshot)) {
		t.Fatalf("stored rows differ from the snapshot:\n%+v\nwant\n%+v", decoded, snapshot)
	}
	return out
}

// sessionIDs are n distinct Claude session ids.
func sessionIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("cccccccc-0000-4000-8000-%012d", i)
	}
	return ids
}

func (f fixture) reopen() *Index {
	return &Index{Path: f.index.Path, Clones: f.index.Clones, Stores: f.index.Stores, mapSession: f.index.mapSession}
}

func TestIndexMovesTheDocumentIntoRows(t *testing.T) {
	f := newFixture(t)
	ids := sessionIDs(3)
	for _, id := range ids {
		f.write(t, f.repo, id, claudeTranscript(f.repo, id))
	}
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rows := storedRows(t, f.index.Path)
	entries, _ := f.index.Snapshot()
	built := byPath(entries, rows, t)
	// An older baum kept the same entries in one document and no tables.
	raw, err := json.Marshal(indexFile{Version: indexVersion, Bands: pricing.Active().PromptBands(), Entries: built})
	if err != nil {
		t.Fatal(err)
	}
	execDB(t, f.index.Path, `DROP TABLE analytics_sessions; DROP TABLE analytics_index`)
	if err := appdb.Write(f.index.Path, "cache", "analytics-index", raw); err != nil {
		t.Fatal(err)
	}

	moved := f.reopen()
	saves := 0
	moved.write = func(path string, b indexBatch) error { saves++; return writeIndex(path, b) }
	got, _ := moved.Snapshot()
	if len(got) != len(ids) {
		t.Fatalf("after the move: %d entries, want %d", len(got), len(ids))
	}
	if after := byPath(got, storedRows(t, f.index.Path), t); !reflect.DeepEqual(after, built) {
		t.Fatalf("moved rows differ:\n%+v\nwant\n%+v", after, built)
	}
	if _, err := appdb.Read(f.index.Path, "cache", "analytics-index"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the document stayed: %v", err)
	}
	mapped := *f.mapped
	if err := moved.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped != mapped || saves != 0 {
		t.Fatalf("a moved index read %d sessions again and saved %d times", *f.mapped-mapped, saves)
	}

	// A document of another version would have been ignored: it is deleted
	// unread and leaves the rows alone.
	stale, _ := json.Marshal(indexFile{Version: indexVersion - 1, Entries: map[string]Entry{"/gone": {Key: "k"}}})
	if err := appdb.Write(f.index.Path, "cache", "analytics-index", stale); err != nil {
		t.Fatal(err)
	}
	again := f.reopen()
	if got, _ := again.Snapshot(); len(got) != len(ids) {
		t.Fatalf("a stale document changed the entries: %d", len(got))
	}
	if _, err := appdb.Read(f.index.Path, "cache", "analytics-index"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the stale document stayed: %v", err)
	}
	if rows := storedRows(t, f.index.Path); len(rows) != len(ids) || rows["/gone"] != "" {
		t.Fatalf("rows after a stale document: %v", rows)
	}
}

func TestIndexDocumentReplacesRowsAfterADowngrade(t *testing.T) {
	f := newFixture(t)
	ids := sessionIDs(2)
	for _, id := range ids {
		f.write(t, f.repo, id, claudeTranscript(f.repo, id))
	}
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	entries, _ := f.index.Snapshot()
	built := byPath(entries, storedRows(t, f.index.Path), t)
	// An older baum, run after the rows were written, saved only one entry
	// in its document.
	kept := map[string]Entry{}
	for p, e := range built {
		if e.SessionID == ids[0] {
			kept[p] = e
		}
	}
	raw, _ := json.Marshal(indexFile{Version: indexVersion, Bands: pricing.Active().PromptBands(), Entries: kept})
	if err := appdb.Write(f.index.Path, "cache", "analytics-index", raw); err != nil {
		t.Fatal(err)
	}
	x := f.reopen()
	got, _ := x.Snapshot()
	if moved := byPath(got, storedRows(t, f.index.Path), t); !reflect.DeepEqual(moved, kept) {
		t.Fatalf("after the move: %+v, want the document's %+v", moved, kept)
	}
	// The session the document lacked is read again, the other is not.
	mapped := *f.mapped
	if err := x.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped-mapped != 1 {
		t.Fatalf("read %d sessions again, want 1", *f.mapped-mapped)
	}
	entries, _ = x.Snapshot()
	if final := byPath(entries, storedRows(t, f.index.Path), t); len(final) != 2 {
		t.Fatalf("rows = %+v", final)
	}
}

func TestIndexClearsRowsOfAnotherVersion(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.repo, sessionID, claudeTranscript(f.repo, sessionID))
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	execDB(t, f.index.Path, `UPDATE analytics_index SET version = ?`, indexVersion-1)
	execDB(t, f.index.Path, `INSERT INTO analytics_sessions(path, entry) VALUES('/old', '{}')`)
	x := f.reopen()
	if got, _ := x.Snapshot(); len(got) != 0 {
		t.Fatalf("rows of another version were used: %+v", got)
	}
	if err := x.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped != 2 {
		t.Fatalf("mapped %d, want the session read again", *f.mapped)
	}
	rows := storedRows(t, f.index.Path)
	if _, old := rows["/old"]; old || len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	if again, _ := f.reopen().Snapshot(); len(again) != 1 {
		t.Fatalf("after the rebuild: %d entries", len(again))
	}
}

func TestIndexWritesOnlyTheChangedRows(t *testing.T) {
	f := newFixture(t)
	ids := sessionIDs(4)
	paths := map[string]string{}
	for _, id := range ids[:3] {
		paths[id] = f.write(t, f.repo, id, claudeTranscript(f.repo, id))
	}
	var batches []indexBatch
	f.index.write = func(path string, b indexBatch) error {
		batches = append(batches, b)
		return writeIndex(path, b)
	}
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || !batches[0].reset || len(batches[0].upsert) != 3 || len(batches[0].remove) != 0 {
		t.Fatalf("first build batches = %+v", batches)
	}
	before := storedRows(t, f.index.Path)

	// One session changes, one is deleted and one is new.
	f.write(t, f.repo, ids[0], claudeTranscript(f.repo, ids[0])+claudeTranscript(f.repo, ids[0]))
	if err := os.Remove(paths[ids[1]]); err != nil {
		t.Fatal(err)
	}
	paths[ids[3]] = f.write(t, f.repo, ids[3], claudeTranscript(f.repo, ids[3]))
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("saves = %d, want 2", len(batches))
	}
	b := batches[1]
	upserted := slices.Sorted(maps.Keys(b.upsert))
	want := []string{paths[ids[0]], paths[ids[3]]}
	slices.Sort(want)
	if b.reset || !slices.Equal(upserted, want) || !slices.Equal(b.remove, []string{paths[ids[1]]}) {
		t.Fatalf("incremental batch: reset %v, upsert %v, remove %v", b.reset, upserted, b.remove)
	}
	after := storedRows(t, f.index.Path)
	if len(after) != 3 || after[paths[ids[2]]] != before[paths[ids[2]]] || after[paths[ids[0]]] == before[paths[ids[0]]] {
		t.Fatalf("rows after the refresh: %d", len(after))
	}
	entries, _ := f.index.Snapshot()
	if got := byPath(entries, after, t); len(got) != 3 {
		t.Fatalf("rows = %+v", got)
	}

	// Nothing changed: nothing is written.
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("an unchanged refresh saved")
	}
}

func TestIndexInterruptedBuildLeavesAUsableIndex(t *testing.T) {
	f := newFixture(t)
	const total = saveEvery + 5
	for _, id := range sessionIDs(total) {
		f.write(t, f.repo, id, claudeTranscript(f.repo, id))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The daemon stops after reading two sessions past the first save.
	reads := 0
	f.index.mapSession = func(s collector.Session) (*session.Graph, []string, error) {
		reads++
		if reads == saveEvery+2 {
			cancel()
		}
		return mapSession(s)
	}
	if err := f.index.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh = %v, want it cancelled", err)
	}
	// Stopping saves what was read since the first save.
	if rows := storedRows(t, f.index.Path); len(rows) != saveEvery+2 {
		t.Fatalf("stored %d rows, want the %d read", len(rows), saveEvery+2)
	}

	// The next daemon starts from the saved rows and reads only the rest.
	next := f.reopen()
	if got, _ := next.Snapshot(); len(got) != saveEvery+2 {
		t.Fatalf("restart sees %d entries, want %d", len(got), saveEvery+2)
	}
	before := reads
	if err := next.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := reads - before; read != total-saveEvery-2 {
		t.Fatalf("restart read %d sessions, want %d", read, total-saveEvery-2)
	}
	rows := storedRows(t, f.index.Path)
	entries, progress := next.Snapshot()
	if len(rows) != total || len(entries) != total || !progress.Complete {
		t.Fatalf("rows %d, entries %d, progress %+v", len(rows), len(entries), progress)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.SessionID] {
			t.Fatalf("session %s counted twice", e.SessionID)
		}
		seen[e.SessionID] = true
	}

	// A process killed mid-build, here every save after the first failing,
	// keeps the rows of the saves before it, and the next resumes from them.
	g := newFixture(t)
	for _, id := range sessionIDs(total) {
		g.write(t, g.repo, id, claudeTranscript(g.repo, id))
	}
	saves := 0
	g.index.write = func(path string, b indexBatch) error {
		saves++
		if saves >= 2 {
			return errors.New("killed")
		}
		return writeIndex(path, b)
	}
	if err := g.index.Refresh(context.Background()); err == nil {
		t.Fatal("the failed save was not reported")
	}
	if rows := storedRows(t, g.index.Path); len(rows) != saveEvery {
		t.Fatalf("after a failed save: %d rows, want %d", len(rows), saveEvery)
	}
	restarted := g.reopen()
	if got, _ := restarted.Snapshot(); len(got) != saveEvery {
		t.Fatalf("restart sees %d entries, want %d", len(got), saveEvery)
	}
	mapped := *g.mapped
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := *g.mapped - mapped; read != total-saveEvery {
		t.Fatalf("restart read %d sessions, want %d", read, total-saveEvery)
	}
	entries, _ = restarted.Snapshot()
	if got := byPath(entries, storedRows(t, g.index.Path), t); len(got) != total {
		t.Fatalf("after the restart: %d rows, want %d", len(got), total)
	}
	// The same process retrying its failed save stores the rest too.
	h := newFixture(t)
	for _, id := range sessionIDs(total) {
		h.write(t, h.repo, id, claudeTranscript(h.repo, id))
	}
	fail := true
	h.index.write = func(path string, b indexBatch) error {
		if fail && len(b.upsert) < saveEvery {
			return errors.New("disk full")
		}
		return writeIndex(path, b)
	}
	if err := h.index.Refresh(context.Background()); err == nil {
		t.Fatal("the failed save was not reported")
	}
	fail = false
	if err := h.index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := storedRows(t, h.index.Path); len(rows) != total {
		t.Fatalf("the retried save stored %d rows, want %d", len(rows), total)
	}
}

func TestIndexConcurrentRefreshesStoreEachSessionOnce(t *testing.T) {
	f := newFixture(t)
	for _, id := range sessionIDs(6) {
		f.write(t, f.repo, id, claudeTranscript(f.repo, id))
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.index.Refresh(context.Background())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if *f.mapped != 6 {
		t.Fatalf("mapped %d sessions, want each once", *f.mapped)
	}
	entries, _ := f.index.Snapshot()
	if got := byPath(entries, storedRows(t, f.index.Path), t); len(got) != 6 {
		t.Fatalf("rows = %d", len(got))
	}
}
