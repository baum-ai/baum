package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
)

// archivedIDs reads the ids of the journal's archived rows, oldest first.
func archivedIDs(t *testing.T, path string) []string {
	t.Helper()
	db, err := appdb.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT id FROM jobs WHERE archived=1 ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// shortArchiveInterval makes a running queue archive every few milliseconds.
func shortArchiveInterval(t *testing.T) {
	t.Helper()
	old := archiveInterval
	archiveInterval = 2 * time.Millisecond
	t.Cleanup(func() { archiveInterval = old })
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !done(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func TestOpenArchivesJobsThatEndedLongAgo(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	now := time.Now().UTC()
	old, recent := now.Add(-ArchiveAfter-time.Hour), now.Add(-ArchiveAfter+time.Hour)
	q := openAt(t, path)
	addJobs(t, q,
		Job{ID: "old-done", State: "completed", Prepared: Prepared{Repository: "org/repo", Item: Item{PR: 1}, HeadSHA: "a1"}, CreatedAt: old, UpdatedAt: old},
		// Automatic jobs wait for a gate, which this queue never gets.
		Job{ID: "old-queued", State: "queued", Automatic: true, Prepared: Prepared{Repository: "org/repo", Item: Item{PR: 2}, HeadSHA: "b1"}, CreatedAt: old, UpdatedAt: old},
		Job{ID: "recent", State: "failed", CreatedAt: recent, UpdatedAt: recent},
		Job{ID: "old-cancelled", State: "cancelled", CreatedAt: old, UpdatedAt: old},
	)
	q.Close()

	reopened := openAt(t, path)
	if got := ids(reopened.List()); !slices.Equal(got, []string{"old-queued", "recent"}) {
		t.Fatalf("history %v", got)
	}
	if got := archivedIDs(t, path); !slices.Equal(got, []string{"old-done", "old-cancelled"}) {
		t.Fatalf("archived %v", got)
	}
	if saved, _ := readJournal(path); len(saved) != 4 {
		t.Fatalf("rows deleted: %d left", len(saved))
	}
	reopened.mu.Lock()
	runs := reopened.heads["org/repo#1@a1"]
	reopened.mu.Unlock()
	if runs.runs != 1 {
		t.Fatalf("head counts %+v", runs)
	}
	reopened.Close()

	// Archived jobs stay out, and their heads' counts stay, after a reopen.
	again := openAt(t, path)
	if got := ids(again.List()); !slices.Equal(got, []string{"old-queued", "recent"}) {
		t.Fatalf("history after reopen %v", got)
	}
	again.mu.Lock()
	defer again.mu.Unlock()
	if again.heads["org/repo#1@a1"].runs != 1 {
		t.Fatalf("head counts after reopen %+v", again.heads)
	}
}

func TestRunningQueueArchivesAndNeverArchivesQueuedOrRunningJobs(t *testing.T) {
	shortArchiveInterval(t)
	q, f := newQueue(t)
	now := time.Now().UTC()
	addJobs(t, q, Job{ID: "done", State: "completed", HasTranscript: true, CreatedAt: now, UpdatedAt: now})
	if err := os.MkdirAll(q.transcripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath(q.transcripts, "done"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	running, err := q.Submit(context.Background(), estimate(t, q, "single", Item{Clone: "clone", PR: 1}).ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	awaitStart(t, q, f, running[0].ID)
	queued, err := q.Submit(context.Background(), estimate(t, q, "single", Item{Clone: "clone", PR: 2}).ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	// 121 days on, by the queue's clock.
	later := now.Add(ArchiveAfter + 24*time.Hour)
	q.SetClock(func() time.Time { return later })
	waitFor(t, "the finished job was not archived", func() bool { return !slices.Contains(ids(q.List()), "done") })
	if got := ids(q.List()); !slices.Equal(got, []string{running[0].ID, queued[0].ID}) {
		t.Fatalf("history %v", got)
	}
	if _, err := os.Stat(transcriptPath(q.transcripts, "done")); !os.IsNotExist(err) {
		t.Fatalf("transcript kept: %v", err)
	}
	// The running job moved in the history; it still records its end.
	releaseJob(t, f)
	waitJournal(t, q, running[0].ID, "completed")
	releaseJob(t, f)
	waitJournal(t, q, queued[0].ID, "completed")
	// Once ended, they too are past the cutoff of this clock.
	waitFor(t, "ended jobs were not archived", func() bool { return len(q.List()) == 0 })
	if got := archivedIDs(t, q.path); !slices.Equal(got, []string{"done", running[0].ID, queued[0].ID}) {
		t.Fatalf("archived %v", got)
	}
}

func TestArchivingAChangedJobKeepsItsLatestContent(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	q := openAt(t, path)
	old := time.Now().UTC().Add(-ArchiveAfter - time.Hour)
	addJobs(t, q, Job{ID: "old", State: "failed", CreatedAt: old, UpdatedAt: old})
	// Changed, and its write failed: it is still marked when archived.
	q.mu.Lock()
	q.jobs[0].Summary = "latest"
	q.mark("old")
	q.mu.Unlock()
	if err := q.archive(); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	if len(q.dirty) != 0 || len(q.jobs) != 0 {
		t.Fatalf("after archive: jobs %v, marks %v", q.jobs, q.dirty)
	}
	// A later save must not delete the archived row.
	q.mark("other")
	err := q.save()
	q.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rowOf(t, path, "old")
	if !ok || row.Summary != "latest" || !slices.Equal(archivedIDs(t, path), []string{"old"}) {
		t.Fatalf("row %+v, %v", row, ok)
	}
}

func TestFailedArchiveKeepsTheJobs(t *testing.T) {
	q := openAt(t, filepath.Join(privateDir(t), "app.sqlite"))
	old := time.Now().UTC().Add(-ArchiveAfter - time.Hour)
	addJobs(t, q, Job{ID: "old", State: "completed", CreatedAt: old, UpdatedAt: old})
	restore := failSaves(t, q, time.Hour)
	if err := q.archive(); err == nil {
		t.Fatal("archived without a write")
	}
	if got := ids(q.List()); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("history %v", got)
	}
	restore()
	if err := q.archive(); err != nil || len(q.List()) != 0 {
		t.Fatalf("retry: %v, %v", err, q.List())
	}
}

func TestArchivedHeadsStayDeduplicated(t *testing.T) {
	path := filepath.Join(privateDir(t), "app.sqlite")
	f := &autoExecutor{heads: map[int]string{1: "a1", 2: "b1", 3: "c1", 4: "d1", 5: "e1", 6: "f1"}, backend: "claude", started: make(chan Prepared, 10), release: make(chan struct{}),
		members: map[string]string{"g": "1@a1,2@b1"}}
	open := func() *Queue {
		q, err := Open(path, f, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(q.Close)
		q.SetAutomaticGate(func(Job) Admission { return Admission{} })
		return q
	}
	q := open()
	now := time.Now().UTC()
	old := now.Add(-ArchiveAfter - time.Hour)
	pr := func(id string, n int, head, state string) Job {
		return Job{ID: id, Automatic: true, State: state, Prepared: Prepared{Repository: "Org/Repo", Item: Item{Clone: "clone", PR: n}, HeadSHA: head}, CreatedAt: old, UpdatedAt: old}
	}
	group := pr("group", 0, "", "completed")
	group.Group, group.Members = "g", "1@a1,2@b1"
	interruptedGroup := pr("group-again", 0, "", "interrupted")
	interruptedGroup.Group, interruptedGroup.Members, interruptedGroup.UpdatedAt = "g", "1@a1,2@b1", now
	interrupted := pr("again", 1, "a1", "interrupted")
	interrupted.UpdatedAt = now
	draft := pr("draft", 4, "d1", "cancelled")
	draft.Reason = ReasonDraft
	refusedNow := pr("refused-now", 2, "b1", "cancelled")
	refusedNow.Reason, refusedNow.UpdatedAt = ReasonDraft, now
	fixed := pr("fixed", 3, "c1", "completed")
	fixed.Kind = KindFix
	refusedThenRun := pr("refused-then-run", 6, "f1", "cancelled")
	refusedThenRun.Reason = ReasonNeedsClone
	addJobs(t, q,
		pr("done", 1, "a1", "completed"), group,
		// Interrupted long ago: archived, it is never retried.
		pr("lost", 5, "e1", "interrupted"),
		// Neither a fix nor a refused draft uses up a head.
		fixed, draft,
		// The jobs of a head with a refused job stay: archiving the run
		// would make Refused list the refusal.
		refusedThenRun, pr("ran", 6, "f1", "completed"),
		interrupted, interruptedGroup, refusedNow)
	refusedBefore := ids(q.Refused())
	if err := q.archive(); err != nil {
		t.Fatal(err)
	}
	if got := ids(q.List()); !slices.Equal(got, []string{"draft", "refused-then-run", "ran", "again", "group-again", "refused-now"}) {
		t.Fatalf("history %v", got)
	}
	check := func(q *Queue) {
		t.Helper()
		for _, r := range []AutomaticRequest{auto(1, "a1"), auto(5, "e1"), auto(6, "f1"),
			{Item: Item{Clone: "clone", Group: "g"}, Repository: "org/repo", Head: "1@a1,2@b1", Trigger: TriggerStackReviewed}} {
			if _, err := q.SubmitAutomatic(context.Background(), r); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("%+v: %v", r, err)
			}
		}
		// The interrupted runs of archived heads are not the only ones.
		if got := q.Interrupted(); len(got) != 0 {
			t.Fatalf("interrupted %v", ids(got))
		}
		if got := ids(q.Refused()); !slices.Equal(got, refusedBefore) {
			t.Fatalf("refused %v, before archiving %v", got, refusedBefore)
		}
	}
	check(q)
	q.Close()
	q = open()
	check(q)
	// An archived fix, or a refused draft, leaves its head free.
	for _, r := range []AutomaticRequest{auto(3, "c1"), auto(4, "d1")} {
		if _, err := q.SubmitAutomatic(context.Background(), r); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		<-f.started
		f.release <- struct{}{}
		waitFor(t, "the job did not end", func() bool { return q.Idle() })
	}
}

func TestArchivedJobIsUnknown(t *testing.T) {
	q := openAt(t, filepath.Join(privateDir(t), "app.sqlite"))
	old := time.Now().UTC().Add(-ArchiveAfter - time.Hour)
	addJobs(t, q, Job{ID: "old", State: "failed", HasTranscript: true, CreatedAt: old, UpdatedAt: old})
	if err := q.archive(); err != nil {
		t.Fatal(err)
	}
	handler := q.Handler()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/jobs/old/seen", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/jobs/old/cancel", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/jobs/old/transcript", nil),
		httptest.NewRequest(http.MethodGet, "/api/jobs/old/draft", nil),
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", request.Method, request.URL.Path, w.Code)
		}
	}
	if state, reason, ended := q.ended("old"); state != "interrupted" || reason != "interrupted" || !ended {
		t.Errorf("ended: %s %s %v", state, reason, ended)
	}
}

func TestShutdownStopsTheArchivePass(t *testing.T) {
	shortArchiveInterval(t)
	q := openAt(t, filepath.Join(privateDir(t), "app.sqlite"))
	old := time.Now().UTC().Add(-time.Hour)
	addJobs(t, q, Job{ID: "done", State: "completed", CreatedAt: old, UpdatedAt: old})
	q.SetClock(func() time.Time { return time.Now().Add(ArchiveAfter) })
	waitFor(t, "not archived", func() bool { return len(q.List()) == 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// The pass stopped with the queue: another process may open it.
	openAt(t, q.path)
}
