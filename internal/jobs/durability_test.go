package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
)

// snapshotDatabase copies what is committed to the database at path into a
// new database at copy, as a crash at that moment would leave it.
func snapshotDatabase(path, copy string) error {
	db, err := appdb.Open(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`VACUUM INTO ?`, copy); err != nil {
		return err
	}
	return os.Chmod(copy, 0o600)
}

// Every change the queue publishes is committed first: a progress report,
// which a crash right after its publication leaves on disk, and the job's
// end. Progress is written outside the queue's lock (saveOutside).
func TestPublishedChangesSurviveACrash(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "app.sqlite")
	type crash struct {
		published Job
		path      string
	}
	crashes := make(chan crash, 50)
	var mu sync.Mutex
	n := 0
	changed := func(j Job) {
		mu.Lock()
		n++
		copyDir := filepath.Join(dir, "crash", strconv.Itoa(n))
		mu.Unlock()
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Error(err)
			return
		}
		copy := filepath.Join(copyDir, "app.sqlite")
		if err := snapshotDatabase(path, copy); err != nil {
			t.Error(err)
			return
		}
		crashes <- crash{j, copy}
	}
	f := &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(path, f, 1, changed)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	jobs, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	next := func(want func(Job) bool) crash {
		t.Helper()
		for {
			select {
			case c := <-crashes:
				if c.published.ID == jobs[0].ID && want(c.published) {
					return c
				}
			case <-time.After(5 * time.Second):
				t.Fatal("not published")
			}
		}
	}
	progress := next(func(j Job) bool { return j.Phase == "repairing" })
	if _, err = q.Cancel(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	ended := next(func(j Job) bool { return j.State == "cancelled" })

	// Reopened after a crash during the run, the job is interrupted with the
	// usage it had reported.
	reopened, err := Open(progress.path, &fakeExecutor{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.List()
	reopened.Close()
	if len(got) != 1 || got[0].State != "interrupted" || got[0].Phase != "finished" || got[0].Repairs != 1 || got[0].Accounting == nil || got[0].Accounting.Usage.CacheRead != 100 {
		t.Fatalf("after a crash during the run: %+v", got)
	}
	// After a crash past its end, it stays as it ended.
	reopened, err = Open(ended.path, &fakeExecutor{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	got = reopened.List()
	reopened.Close()
	if len(got) != 1 || got[0].State != "cancelled" || got[0].Accounting == nil || got[0].Accounting.Usage.CacheRead != 100 {
		t.Fatalf("after a crash past the end: %+v", got)
	}
}

// Progress saves of several running jobs at once, and a save under the lock
// meanwhile, all reach the journal in order: it keeps the last report of
// each job, and no job is left marked.
func TestConcurrentProgressSavesKeepTheLatest(t *testing.T) {
	q, _ := newQueue(t)
	const jobCount, reports = 4, 25
	q.mu.Lock()
	for i := range jobCount {
		now := time.Now().UTC()
		j := Job{ID: "job-" + strconv.Itoa(i), State: "running", Phase: "generating", CreatedAt: now, UpdatedAt: now}
		q.jobs = append(q.jobs, j)
		q.mark(j.ID)
	}
	err := q.save()
	q.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range jobCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "job-" + strconv.Itoa(i)
			for r := 1; r <= reports; r++ {
				q.mu.Lock()
				q.jobs[q.find(id)].Calls = r
				q.mark(id)
				if err := q.saveOutside(); err != nil {
					t.Error(err)
					return
				}
				_ = q.List()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := range reports {
			q.mu.Lock()
			q.jobs[q.find("job-0")].Summary = "changed " + strconv.Itoa(r)
			q.mark("job-0")
			err := q.save()
			q.mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	saved, err := readJournal(q.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != jobCount {
		t.Fatalf("journal %+v", saved)
	}
	for _, j := range saved {
		if j.Calls != reports {
			t.Fatalf("job %s journaled at report %d of %d", j.ID, j.Calls, reports)
		}
		if j.ID == "job-0" && j.Summary != "changed "+strconv.Itoa(reports-1) {
			t.Fatalf("job-0 journaled with %q", j.Summary)
		}
	}
	q.mu.Lock()
	marked := len(q.dirty)
	q.mu.Unlock()
	if marked != 0 {
		t.Fatalf("%d jobs left marked", marked)
	}
}

// A failed progress write marks its jobs again, so the next save writes them.
func TestFailedProgressSaveMarksAgain(t *testing.T) {
	q, _ := newQueue(t)
	now := time.Now().UTC()
	q.mu.Lock()
	q.jobs = append(q.jobs, Job{ID: "job", State: "running", Phase: "generating", CreatedAt: now, UpdatedAt: now})
	q.mark("job")
	path := q.path
	q.path = filepath.Join(path, "not-a-directory")
	if err := q.saveOutside(); err == nil {
		t.Fatal("write to a missing database succeeded")
	}
	q.mu.Lock()
	marked := q.dirty["job"]
	q.path = path
	err := q.save()
	q.mu.Unlock()
	if !marked || err != nil {
		t.Fatalf("marked %v, retry %v", marked, err)
	}
	if saved, err := readJournal(path); err != nil || len(saved) != 1 || saved[0].ID != "job" {
		t.Fatalf("journal %+v %v", saved, err)
	}
}
