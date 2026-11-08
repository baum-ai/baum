package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

func job(id, state, repository string, pr int, kind string) Job {
	return Job{ID: id, State: state, UpdatedAt: time.Now().UTC(), Prepared: Prepared{Repository: repository, Item: Item{PR: pr, Kind: kind}}}
}

func seed(t *testing.T, jobs ...Job) http.Handler {
	t.Helper()
	q, _ := newQueue(t)
	q.mu.Lock()
	q.jobs = append(q.jobs, jobs...)
	q.mu.Unlock()
	return q.Handler()
}

func get(t *testing.T, h http.Handler, query string) (int, []string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs"+query, nil))
	var answer struct {
		Available bool
		Jobs      []Job
		Next      string
	}
	if w.Code == 200 {
		if e := json.Unmarshal(w.Body.Bytes(), &answer); e != nil || !answer.Available {
			t.Fatalf("%v %s", e, w.Body)
		}
	}
	ids := []string{}
	for _, j := range answer.Jobs {
		ids = append(ids, j.ID)
	}
	return w.Code, ids, answer.Next
}

func TestCurrentViewKeepsWhatTheClientsRead(t *testing.T) {
	seen := time.Now().UTC()
	failedSeen := job("failed-seen", "failed", "o/a", 1, KindChat)
	failedSeen.SeenAt = &seen
	group := func(id, g, state string) Job {
		j := job(id, state, "o/a", 0, "")
		j.Group = g
		return j
	}
	groupOf := func(j Job, g string) Job {
		j.Group = g
		return j
	}
	h := seed(t,
		job("review-old", "completed", "o/a", 1, ""),
		job("fix-old", "completed", "o/a", 1, KindFix),
		job("review-new", "completed", "O/A", 1, ""), // same PR, other case
		job("fix-new", "failed", "o/a", 1, KindFix),  // unseen failed
		failedSeen,
		job("chat-old", "completed", "o/a", 1, KindChat),
		job("chat-new", "completed", "o/a", 1, KindChat),
		job("other-pr", "completed", "o/a", 2, ""),
		group("group-old", "g1", "completed"),
		group("group-new", "g1", "completed"),
		job("details-old", "completed", "o/a", 1, KindDetails),
		job("details-new", "completed", "o/a", 1, KindDetails),
		groupOf(job("group-fix-old", "completed", "o/a", 0, KindFix), "g1"),
		groupOf(job("group-fix-new", "completed", "o/a", 0, KindFix), "g1"),
		job("build-old", "completed", "o/a", 0, KindFeatureBuild),
		job("build-new", "cancelled", "o/a", 0, KindFeatureBuild),
		job("other-repo", "completed", "o/b", 1, ""),
		job("queued-old", "queued", "o/c", 5, ""),
		job("queued-new", "completed", "o/c", 5, ""),
		job("running", "running", "o/c", 5, ""),
	)
	code, ids, next := get(t, h, "")
	want := []string{"fix-new", "chat-new", "review-new", "other-pr", "details-new", "group-new", "group-fix-new", "build-new", "other-repo", "queued-old", "running"}
	// History order is kept.
	order := []string{"review-old", "fix-old", "review-new", "fix-new", "failed-seen", "chat-old", "chat-new", "other-pr", "group-old", "group-new", "details-old", "details-new", "group-fix-old", "group-fix-new", "build-old", "build-new", "other-repo", "queued-old", "queued-new", "running"}
	slices.SortFunc(want, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
	if code != 200 || !slices.Equal(ids, want) || next != "" {
		t.Fatalf("%d %v want %v", code, ids, want)
	}
}

func TestPullRequestViewIsEveryJobOfOnePullRequest(t *testing.T) {
	grouped := job("group", "completed", "o/a", 1, "")
	grouped.Group = "g"
	h := seed(t,
		job("a", "completed", "o/a", 1, KindFix),
		job("b", "completed", "o/a", 2, KindFix),
		grouped,
		job("c", "completed", "O/A", 1, KindChat),
		job("d", "completed", "o/b", 1, KindFix),
		job("e", "failed", "o/a", 1, ""),
	)
	code, ids, next := get(t, h, "?repository=o/a&pr=1")
	if code != 200 || !slices.Equal(ids, []string{"a", "c", "e"}) || next != "" {
		t.Fatalf("%d %v", code, ids)
	}
	if _, ids, _ := get(t, h, "?repository=o/a&pr=3"); len(ids) != 0 {
		t.Fatalf("%v", ids)
	}
	for _, query := range []string{"?repository=o/a", "?pr=1", "?repository=o/a&pr=x", "?repository=o/a&pr=0", "?repository=o/a&pr=1&limit=5", "?pr=1&before=a", "?other=1"} {
		if code, _, _ := get(t, h, query); code != 400 {
			t.Errorf("%s answered %d", query, code)
		}
	}
}

func TestPagedViewWalksNewestFirstWithACursor(t *testing.T) {
	var all []Job
	var ids []string
	for i := 1; i <= 7; i++ {
		id := "j" + strconv.Itoa(i)
		all = append(all, job(id, "completed", "o/a", i, ""))
		ids = append(ids, id)
	}
	h := seed(t, all...)
	code, page, next := get(t, h, "?limit=3")
	if code != 200 || !slices.Equal(page, []string{"j7", "j6", "j5"}) || next != "j5" {
		t.Fatalf("%d %v %q", code, page, next)
	}
	_, page, next = get(t, h, "?limit=3&before=j5")
	if !slices.Equal(page, []string{"j4", "j3", "j2"}) || next != "j2" {
		t.Fatalf("%v %q", page, next)
	}
	_, page, next = get(t, h, "?limit=3&before=j2")
	if !slices.Equal(page, []string{"j1"}) || next != "" {
		t.Fatalf("%v %q", page, next)
	}
	// Exactly the rest: nothing older, so no next.
	if _, page, next = get(t, h, "?limit=2&before=j3"); !slices.Equal(page, []string{"j2", "j1"}) || next != "" {
		t.Fatalf("%v %q", page, next)
	}
	// before alone pages by 50; limit alone starts at the newest.
	if _, page, next = get(t, h, "?before=j4"); !slices.Equal(page, []string{"j3", "j2", "j1"}) || next != "" {
		t.Fatalf("%v %q", page, next)
	}
	if _, page, _ = get(t, h, "?limit=200"); len(page) != 7 {
		t.Fatalf("%v", page)
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=-1", "?limit=x", "?limit=", "?before=missing", "?limit=3&before=missing", "?before="} {
		want := 400
		if query == "?limit=" {
			want = 200 // an empty limit is none: the current view
		}
		if code, _, _ := get(t, h, query); code != want {
			t.Errorf("%s answered %d", query, code)
		}
	}
}

func TestTranscriptFindsAJobOutsideTheCurrentView(t *testing.T) {
	q, _ := newQueue(t)
	old := job("old", "completed", "o/a", 1, KindFix)
	old.HasTranscript = true
	q.mu.Lock()
	q.jobs = append(q.jobs, old, job("new", "completed", "o/a", 1, KindFix))
	q.mu.Unlock()
	if err := os.MkdirAll(q.transcripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath(q.transcripts, "old"), []byte(`{"seq":1,"kind":"message","text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := q.Handler()
	if _, ids, _ := get(t, h, ""); !slices.Equal(ids, []string{"new"}) {
		t.Fatalf("%v", ids)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs/old/transcript", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestOneJobIsServedByID(t *testing.T) {
	h := seed(t, job("a", "completed", "o/a", 1, ""), job("b", "completed", "o/a", 1, ""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs/a", nil))
	var j Job
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.ID != "a" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs/missing", nil))
	if w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}
