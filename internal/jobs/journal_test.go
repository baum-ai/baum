package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb"
)

// readJournal reads the journal's rows, oldest first, without opening the
// queue.
func readJournal(path string) ([]Job, error) {
	db, err := appdb.Open(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT job FROM jobs ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var j Job
		if err = json.Unmarshal(raw, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func ids(jobs []Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

func openAt(t *testing.T, path string) *Queue {
	t.Helper()
	q, err := Open(path, &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{})}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	return q
}

// addJobs appends jobs to an open queue's history and saves them.
func addJobs(t *testing.T, q *Queue, jobs ...Job) {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range jobs {
		q.jobs = append(q.jobs, j)
		q.mark(j.ID)
	}
	if err := q.save(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMovesTheJobDocumentIntoRows(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	seen := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	created := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	old := []Job{
		{ID: "z-first", State: "completed", Phase: "finished", Summary: "fixed it", SeenAt: &seen, CreatedAt: created, UpdatedAt: created},
		{ID: "a-running", State: "running", Phase: "generating", Draft: 3, Accounting: &agent.Accounting{}, CreatedAt: created.Add(time.Minute), UpdatedAt: created.Add(time.Minute)},
		{ID: "m-queued", State: "queued", Phase: "queued", CreatedAt: created.Add(-time.Hour), UpdatedAt: created},
	}
	raw, _ := json.Marshal(old)
	if err := appdb.Write(path, "state", "jobs", raw); err != nil {
		t.Fatal(err)
	}
	q := openAt(t, path)
	listed := q.List()
	if got := ids(listed); !slices.Equal(got, []string{"z-first", "a-running", "m-queued"}) {
		t.Fatalf("order %v", got)
	}
	if j := listed[0]; j.Summary != "fixed it" || j.SeenAt == nil || !j.SeenAt.Equal(seen) || !j.CreatedAt.Equal(created) {
		t.Fatalf("fields lost: %+v", j)
	}
	if j := listed[1]; j.State != "interrupted" || j.Reason != "interrupted" || j.Phase != "finished" || j.Draft != 0 || j.CostReason != "usage_incomplete" || !j.Accounting.Usage.Unreported {
		t.Fatalf("running job not interrupted: %+v", j)
	}
	if _, err := appdb.Read(path, "state", "jobs"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("document kept: %v", err)
	}
	saved, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(saved); !slices.Equal(got, []string{"z-first", "a-running", "m-queued"}) || saved[1].State != "interrupted" {
		t.Fatalf("rows %+v", saved)
	}
	q.Close()

	reopened := openAt(t, path)
	if got := ids(reopened.List()); !slices.Equal(got, []string{"z-first", "a-running", "m-queued"}) {
		t.Fatalf("order after reopen %v", got)
	}
}

func TestFailedMoveKeepsTheDocument(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	raw, _ := json.Marshal([]Job{{ID: "good", State: "completed"}, {ID: "bad", State: "completed"}})
	if err := appdb.Write(path, "state", "jobs", raw); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	// The second row's insert fails after the first one's succeeded.
	if _, err = db.Exec(journalSchema + `; CREATE TRIGGER refuse BEFORE INSERT ON jobs WHEN NEW.id='bad' BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	f := &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{})}
	if q, err := Open(path, f, 1, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		if q != nil {
			q.Close()
		}
		t.Fatalf("open succeeded or failed otherwise: %v", err)
	}
	if kept, err := appdb.Read(path, "state", "jobs"); err != nil || !bytes.Equal(kept, raw) {
		t.Fatalf("document %s, %v", kept, err)
	}
	if rows, err := readJournal(path); err != nil || len(rows) != 0 {
		t.Fatalf("rows left behind: %+v, %v", rows, err)
	}
	// Once the move can succeed, the next open makes it.
	if _, err = db.Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	q := openAt(t, path)
	if got := ids(q.List()); !slices.Equal(got, []string{"good", "bad"}) {
		t.Fatalf("order %v", got)
	}
}

func TestReopenKeepsCreationOrder(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	q := openAt(t, path)
	now := time.Now().UTC()
	// Neither the ids nor the times sort like the history does.
	addJobs(t, q, Job{ID: "z", State: "completed", CreatedAt: now}, Job{ID: "a", State: "completed", CreatedAt: now.Add(-time.Hour)})
	addJobs(t, q, Job{ID: "m", State: "completed", CreatedAt: now.Add(-2 * time.Hour)})
	// Rewriting the oldest keeps its place.
	q.mu.Lock()
	q.jobs[0].Summary = "changed"
	q.mark("z")
	if err := q.save(); err != nil {
		t.Fatal(err)
	}
	q.mu.Unlock()
	q.Close()
	reopened := openAt(t, path)
	listed := reopened.List()
	if got := ids(listed); !slices.Equal(got, []string{"z", "a", "m"}) || listed[0].Summary != "changed" {
		t.Fatalf("after reopen: %+v", listed)
	}
}

func TestSaveWritesOnlyChangedJobs(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	q := openAt(t, path)
	addJobs(t, q, Job{ID: "a", State: "completed"}, Job{ID: "b", State: "completed"}, Job{ID: "c", State: "failed"})
	// Change b's row behind the queue's back: a save that rewrote it would
	// undo this.
	db, err := appdb.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tampered, _ := json.Marshal(Job{ID: "b", State: "completed", Summary: "untouched"})
	if _, err = db.Exec(`UPDATE jobs SET job=? WHERE id='b'`, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Seen("c"); err != nil {
		t.Fatal(err)
	}
	saved, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved[2].SeenAt == nil {
		t.Fatalf("changed job not written: %+v", saved[2])
	}
	if saved[1].Summary != "untouched" {
		t.Fatalf("unchanged job rewritten: %+v", saved[1])
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.dirty) != 0 {
		t.Fatalf("changes left after a write: %v", q.dirty)
	}
}

func TestRolledBackSubmissionLeavesNoRow(t *testing.T) {
	q, _ := newQueue(t)
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	restore := failSaves(t, q, time.Hour)
	if _, err := q.Submit(context.Background(), e.ID, true, false); err == nil {
		t.Fatal("submitted without a journal write")
	}
	restore()
	q.mu.Lock()
	if len(q.jobs) != 0 || len(q.dirty) != 1 {
		t.Fatalf("jobs %+v, changes %v", q.jobs, q.dirty)
	}
	err := q.save()
	q.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := readJournal(q.path); err != nil || len(saved) != 0 {
		t.Fatalf("rows %+v, %v", saved, err)
	}
	// The same submission, now journaled, is a single row.
	e = estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := readJournal(q.path); err != nil || len(saved) != 1 || saved[0].ID != jobs[0].ID {
		t.Fatalf("rows %+v, %v", saved, err)
	}
}

func TestFailedWriteKeepsChangesForTheRetry(t *testing.T) {
	q := openAt(t, filepath.Join(privateDir(t), "app.sqlite"))
	addJobs(t, q, Job{ID: "a", State: "completed"}, Job{ID: "b", State: "failed"})
	restore := failSaves(t, q, time.Millisecond)
	q.mu.Lock()
	q.jobs[1].State = "cancelled"
	q.mark("b")
	err := q.save()
	if err == nil {
		t.Fatal("write did not fail")
	}
	if !q.dirty["b"] || q.dirty["a"] {
		t.Fatalf("changes after a failed write: %v", q.dirty)
	}
	q.saveFailed(err)
	q.mu.Unlock()
	restore()
	waitJournal(t, q, "b", "cancelled")
}

func TestOversizedJobIsRefused(t *testing.T) {
	q := openAt(t, filepath.Join(privateDir(t), "app.sqlite"))
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, Job{ID: "huge", State: "completed", Summary: strings.Repeat("x", maxJobJSON)})
	q.mark("huge")
	if err := q.save(); err == nil || !strings.Contains(err.Error(), "huge") {
		t.Fatalf("oversized job written: %v", err)
	}
	if !q.dirty["huge"] {
		t.Fatal("refused job forgotten")
	}
}

func TestHistoryLargerThanTheOldLimitSavesAndReopens(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	q := openAt(t, path)
	summary := strings.Repeat("s", 512<<10)
	var history []Job
	for i := range 70 {
		history = append(history, Job{ID: "job-" + string(rune('A'+i)), State: "completed", Summary: summary})
	}
	// 70 × 512 KiB is past the 32 MB a single document could hold.
	addJobs(t, q, history...)
	q.Close()
	reopened := openAt(t, path)
	listed := reopened.List()
	if !slices.Equal(ids(listed), ids(history)) || len(listed[69].Summary) != len(summary) {
		t.Fatalf("history after reopen: %d jobs", len(listed))
	}
}
