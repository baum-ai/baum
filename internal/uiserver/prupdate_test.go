package uiserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeUpdater struct {
	calls []string
	err   error
}

func (f *fakeUpdater) ReviewThreads(_ context.Context, _ github.Repository, _, unresolved int) (prupdate.Threads, error) {
	f.calls = append(f.calls, fmt.Sprintf("threads %d", unresolved))
	return prupdate.Threads{Threads: []prupdate.Thread{{ID: "RT_1", Path: "a.go", Line: 3, Author: "alice", Excerpt: "Rename this.", Comments: 1, URL: "u", CanResolve: true}}}, f.err
}

func (f *fakeUpdater) RerunJob(_ context.Context, r github.Repository, number int, head string, job int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, fmt.Sprintf("rerun job %s#%d %s %d", r, number, head, job))
	return nil
}

func (f *fakeUpdater) FailingChecks(_ context.Context, _ github.Repository, _ int, head string) (prupdate.Checks, error) {
	f.calls = append(f.calls, "checks "+head)
	return prupdate.Checks{HeadSHA: "abc", Checks: []prupdate.FailingCheck{{Name: "test", Conclusion: "failure", Title: "2 tests failed", URL: "u", JobID: 21}}}, f.err
}

func (f *fakeUpdater) AllChecks(_ context.Context, _ github.Repository, _ int, head string) (prupdate.CheckList, error) {
	f.calls = append(f.calls, "all checks "+head)
	return prupdate.CheckList{HeadSHA: "abc", Checks: []prupdate.CheckStatus{{Name: "build", Status: "in_progress"}, {Name: "test", Status: "completed", Conclusion: "failure", JobID: 21}}}, f.err
}

func (f *fakeUpdater) Conflicts(_ context.Context, r github.Repository, number int) (prupdate.ConflictFiles, error) {
	f.calls = append(f.calls, fmt.Sprintf("conflicts %s#%d", r, number))
	return prupdate.ConflictFiles{HeadSHA: "abc", BaseRef: "main", BaseSHA: "def", Files: []string{"a.go"}}, f.err
}

func (f *fakeUpdater) ResolveThread(_ context.Context, r github.Repository, number int, id string) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, fmt.Sprintf("resolve %s#%d %s", r, number, id))
	return nil
}

func (f *fakeUpdater) UpdateOptions(context.Context, github.Repository, int) (prupdate.UpdateOptions, error) {
	return prupdate.UpdateOptions{State: "OPEN", HeadSHA: "abc", HeadRef: "feature", BaseRef: "main", Behind: true}, f.err
}

func (f *fakeUpdater) UpdateBranch(_ context.Context, r github.Repository, number int, head string) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, "update "+r.String()+" "+head)
	return nil
}

func (f *fakeUpdater) RerunOptions(context.Context, github.Repository, int) (prupdate.RerunOptions, error) {
	return prupdate.RerunOptions{State: "OPEN", HeadSHA: "abc", Runs: []prupdate.Run{{ID: 11, Name: "CI"}}, NotRerunnable: []string{"ci/circle"}}, f.err
}

func (f *fakeUpdater) RerunFailedChecks(_ context.Context, r github.Repository, number int, head string) (prupdate.RerunResult, error) {
	if f.err != nil {
		return prupdate.RerunResult{}, f.err
	}
	f.calls = append(f.calls, "rerun "+r.String()+" "+head)
	return prupdate.RerunResult{Rerun: []prupdate.Run{{ID: 11, Name: "CI"}}, Refused: []prupdate.Refusal{}}, nil
}

func TestUpdateHandler(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	updater := &fakeUpdater{}
	var refreshed []string
	changed := 0
	h := UpdateHandler(registry, updater, func(_ context.Context, r github.Repository) { refreshed = append(refreshed, r.String()) }, func() { changed++ })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/api/prs/update-branch-options?repository=ACME/bare&pr=7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"behind":true`) || !strings.Contains(w.Body.String(), `"base_ref":"main"`) {
		t.Fatalf("update options: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/prs/rerun-checks-options?repository=acme/bare&pr=7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"CI"`) || !strings.Contains(w.Body.String(), `"not_rerunnable":["ci/circle"]`) {
		t.Fatalf("rerun options: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/prs/update-branch-options", "/api/prs/rerun-checks-options"} {
		if w := do("GET", path+"?repository=acme/other&pr=7", ""); w.Code != 404 {
			t.Fatalf("%s unpicked: %d", path, w.Code)
		}
		if w := do("GET", path+"?repository=acme/bare", ""); w.Code != 400 {
			t.Fatalf("%s missing pr: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/prs/update-branch", "/api/prs/rerun-checks"} {
		for body, code := range map[string]int{
			`{"repository":"acme/other","pr":7,"head_sha":"abc"}`:             404,
			`{"repository":"acme/bare","pr":7}`:                               400,
			`{"repository":"acme/bare","pr":7,"head_sha":"abc","extra":true}`: 400,
			`{"repository":"acme/bare","head_sha":"abc"}`:                     400,
		} {
			if w := do("POST", path, body); w.Code != code {
				t.Fatalf("%s %s: %d %s, want %d", path, body, w.Code, w.Body.String(), code)
			}
		}
	}
	if len(updater.calls) != 0 || len(refreshed) != 0 {
		t.Fatalf("rejected requests wrote: %v", updater.calls)
	}
	if w := do("POST", "/api/prs/update-branch", `{"repository":"ACME/bare","pr":7,"head_sha":"abc"}`); w.Code != 200 || strings.Join(updater.calls, ",") != "update acme/bare abc" || len(refreshed) != 1 || changed != 1 {
		t.Fatalf("update: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	if w := do("POST", "/api/prs/rerun-checks", `{"repository":"ACME/bare","pr":7,"head_sha":"abc"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"rerun":[{"id":11,"name":"CI"}]`) || len(updater.calls) != 2 || len(refreshed) != 2 || changed != 2 {
		t.Fatalf("rerun: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	for _, c := range []struct {
		err  error
		code int
		body string
	}{
		{&prupdate.Error{Code: prupdate.HeadMoved, Message: "moved"}, 409, `"code":"head_moved"`},
		{&prupdate.Error{Code: prupdate.Conflict, Message: "conflicts"}, 409, `"code":"conflict"`},
		{&prupdate.Error{Code: prupdate.Blocked, Message: "protected"}, 422, `"message":"protected"`},
		{&prupdate.Error{Code: prupdate.NoRuns, Message: "none"}, 422, `"code":"no_runs"`},
		{&prupdate.Error{Code: prupdate.Forbidden, Message: "no"}, 403, `"code":"forbidden"`},
		{context.DeadlineExceeded, 502, `"code":"failed"`},
	} {
		updater.err = c.err
		before := len(refreshed)
		for _, path := range []string{"/api/prs/update-branch", "/api/prs/rerun-checks"} {
			w := do("POST", path, `{"repository":"acme/bare","pr":7,"head_sha":"abc"}`)
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.body) || len(refreshed) != before {
				t.Fatalf("%s %v: %d %s", path, c.err, w.Code, w.Body.String())
			}
		}
	}
}

func TestUpdateHandlerReviewThreads(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	updater := &fakeUpdater{}
	refreshed, changed := 0, 0
	h := UpdateHandler(registry, updater, func(context.Context, github.Repository) { refreshed++ }, func() { changed++ })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/api/prs/review-threads?repository=acme/bare&pr=7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"RT_1"`) || !strings.Contains(w.Body.String(), `"can_resolve":true`) {
		t.Fatalf("threads: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/prs/review-threads?repository=acme/bare&pr=7&unresolved=3", ""); w.Code != 200 {
		t.Fatalf("threads with a count: %d", w.Code)
	}
	if got := strings.Join(updater.calls, ","); got != "threads -1,threads 3" {
		t.Fatalf("counts passed: %s", got)
	}
	updater.calls = nil
	if w := do("GET", "/api/prs/failing-checks?repository=acme/bare&pr=7&head_sha=abc", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"2 tests failed"`) || strings.Join(updater.calls, ",") != "checks abc" {
		t.Fatalf("failing checks: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	updater.calls = nil
	if w := do("GET", "/api/prs/checks?repository=acme/bare&pr=7&head_sha=abc", ""); w.Code != 200 || w.Body.String() != `{"head_sha":"abc","checks":[{"name":"build","status":"in_progress","conclusion":""},{"name":"test","status":"completed","conclusion":"failure","job_id":21}]}`+"\n" || strings.Join(updater.calls, ",") != "all checks abc" {
		t.Fatalf("all checks: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	updater.calls = nil
	if w := do("GET", "/api/prs/conflicts?repository=acme/bare&pr=7", ""); w.Code != 200 || w.Body.String() != `{"head_sha":"abc","base_ref":"main","base_sha":"def","files":["a.go"]}`+"\n" || strings.Join(updater.calls, ",") != "conflicts acme/bare#7" {
		t.Fatalf("conflicts: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	for _, path := range []string{"/api/prs/checks", "/api/prs/conflicts"} {
		if w := do("GET", path+"?repository=acme/other&pr=7", ""); w.Code != 404 {
			t.Fatalf("%s unpicked: %d", path, w.Code)
		}
		if w := do("GET", path+"?repository=acme/bare", ""); w.Code != 400 {
			t.Fatalf("%s missing pr: %d", path, w.Code)
		}
	}
	updater.err = &prupdate.Error{Code: prupdate.NoClone, Message: "baum has no local clone of this repository to compare the branches in."}
	if w := do("GET", "/api/prs/conflicts?repository=acme/bare&pr=7", ""); w.Code != 422 || w.Body.String() != `{"code":"no_clone","message":"baum has no local clone of this repository to compare the branches in."}`+"\n" {
		t.Fatalf("no clone: %d %s", w.Code, w.Body.String())
	}
	updater.err = nil
	updater.calls = nil
	if w := do("GET", "/api/prs/failing-checks?repository=acme/other&pr=7", ""); w.Code != 404 {
		t.Fatalf("unpicked checks: %d", w.Code)
	}
	updater.calls = nil
	for body, code := range map[string]int{
		`{"repository":"acme/other","pr":7,"head_sha":"abc","job_id":21}`: 404,
		`{"repository":"acme/bare","pr":7,"head_sha":"abc"}`:              400,
		`{"repository":"acme/bare","pr":7,"job_id":21}`:                   400,
	} {
		if w := do("POST", "/api/prs/rerun-job", body); w.Code != code {
			t.Fatalf("rerun job %s: %d, want %d", body, w.Code, code)
		}
	}
	if w := do("POST", "/api/prs/rerun-job", `{"repository":"acme/bare","pr":7,"head_sha":"abc","job_id":21}`); w.Code != 200 || strings.Join(updater.calls, ",") != "rerun job acme/bare#7 abc 21" || refreshed != 1 || changed != 1 {
		t.Fatalf("rerun job: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	refreshed, changed = 0, 0
	updater.calls = nil
	if w := do("GET", "/api/prs/review-threads?repository=acme/other&pr=7", ""); w.Code != 404 {
		t.Fatalf("unpicked threads: %d", w.Code)
	}
	for body, code := range map[string]int{
		`{"repository":"acme/other","pr":7,"thread":"RT_1"}`:             404,
		`{"repository":"acme/bare","pr":7}`:                              400,
		`{"repository":"acme/bare","thread":"RT_1"}`:                     400,
		`{"repository":"acme/bare","pr":7,"thread":"RT_1","extra":true}`: 400,
	} {
		if w := do("POST", "/api/prs/resolve-thread", body); w.Code != code {
			t.Fatalf("%s: %d %s, want %d", body, w.Code, w.Body.String(), code)
		}
	}
	if len(updater.calls) != 0 || refreshed != 0 {
		t.Fatalf("rejected requests wrote: %v", updater.calls)
	}
	if w := do("POST", "/api/prs/resolve-thread", `{"repository":"ACME/bare","pr":7,"thread":"RT_1"}`); w.Code != 200 || strings.Join(updater.calls, ",") != "resolve acme/bare#7 RT_1" || refreshed != 1 || changed != 1 {
		t.Fatalf("resolve: %d %s %v", w.Code, w.Body.String(), updater.calls)
	}
	for _, c := range []struct {
		err  error
		code int
	}{
		{&prupdate.Error{Code: prupdate.NotFound, Message: "elsewhere"}, 404},
		{&prupdate.Error{Code: prupdate.Forbidden, Message: "no"}, 403},
		{context.DeadlineExceeded, 502},
	} {
		updater.err = c.err
		if w := do("POST", "/api/prs/resolve-thread", `{"repository":"acme/bare","pr":7,"thread":"RT_1"}`); w.Code != c.code || refreshed != 1 {
			t.Fatalf("%v: %d %s", c.err, w.Code, w.Body.String())
		}
	}
}
