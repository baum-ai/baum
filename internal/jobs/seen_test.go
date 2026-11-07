package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeenRecordsAFailedJobOnceAndSurvivesARestart(t *testing.T) {
	path := filepath.Join(privateDir(t), "jobs.json")
	f := &fakeExecutor{head: "first", started: make(chan Prepared, 10), release: make(chan struct{})}
	q, err := Open(path, f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	q.jobs = append(q.jobs, Job{ID: "broken", State: "failed"}, Job{ID: "fine", State: "completed"})
	q.mark("broken")
	q.mark("fine")
	err = q.save()
	q.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	handler := q.Handler()
	post := func(id string) (int, Job) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/seen", strings.NewReader(`{}`)))
		var job Job
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, job
	}
	code, first := post("broken")
	if code != http.StatusOK || first.SeenAt == nil {
		t.Fatalf("seen: %d %+v", code, first)
	}
	// Seeing it again keeps the first time.
	if _, again := post("broken"); again.SeenAt == nil || !again.SeenAt.Equal(*first.SeenAt) {
		t.Fatalf("seen again: %+v, first %v", again, first.SeenAt)
	}
	// Only failures are flagged, so only they are marked.
	if code, fine := post("fine"); code != http.StatusOK || fine.SeenAt != nil {
		t.Fatalf("completed: %d %+v", code, fine)
	}
	if code, _ := post("missing"); code != http.StatusNotFound {
		t.Fatalf("missing: %d", code)
	}
	q.Close()

	reopened, err := Open(path, f, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	found := false
	for _, job := range reopened.List() {
		if job.ID == "broken" {
			found = true
			if job.SeenAt == nil || !job.SeenAt.Equal(*first.SeenAt) {
				t.Fatalf("after a restart: %+v", job)
			}
		}
	}
	if !found {
		t.Fatalf("the failed job did not survive a restart: %+v", reopened.List())
	}
}
