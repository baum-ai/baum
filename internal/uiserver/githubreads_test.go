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
	"github.com/baum-ai/baum/internal/githubreads"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeReads struct {
	calls []string
	err   error
}

func (f *fakeReads) Threads(_ context.Context, r github.Repository, number int, state string) (githubreads.Threads, error) {
	f.calls = append(f.calls, fmt.Sprintf("threads %s#%d %s", r, number, state))
	return githubreads.Threads{State: state, Threads: []githubreads.Thread{}}, f.err
}

func (f *fakeReads) Timeline(_ context.Context, r github.Repository, number int) (githubreads.Timeline, error) {
	f.calls = append(f.calls, fmt.Sprintf("timeline %s#%d", r, number))
	return githubreads.Timeline{Events: []githubreads.Event{{Type: "closed", At: "2026-01-01T00:00:00Z"}}}, f.err
}

func (f *fakeReads) Issue(_ context.Context, r github.Repository, number int) (githubreads.Issue, error) {
	f.calls = append(f.calls, fmt.Sprintf("issue %s#%d", r, number))
	return githubreads.Issue{Repository: r.String(), Number: number, Title: "Bug"}, f.err
}

func TestGitHubReadsHandler(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	reads := &fakeReads{}
	h := GitHubReadsHandler(registry, reads)
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
		return w
	}
	if w := do("/api/prs/threads?repository=ACME/bare&pr=7"); w.Code != 200 || !strings.Contains(w.Body.String(), `"threads":[]`) || strings.Join(reads.calls, ",") != "threads acme/bare#7 " {
		t.Fatalf("threads: %d %s %v", w.Code, w.Body.String(), reads.calls)
	}
	reads.calls = nil
	if w := do("/api/prs/threads?repository=acme/bare&pr=7&state=all"); w.Code != 200 || strings.Join(reads.calls, ",") != "threads acme/bare#7 all" {
		t.Fatalf("threads all: %d %v", w.Code, reads.calls)
	}
	if w := do("/api/prs/timeline?repository=acme/bare&pr=7"); w.Code != 200 || !strings.Contains(w.Body.String(), `"events":[{"type":"closed"`) {
		t.Fatalf("timeline: %d %s", w.Code, w.Body.String())
	}
	if w := do("/api/issue?repository=acme/bare&number=3"); w.Code != 200 || !strings.Contains(w.Body.String(), `"number":3`) {
		t.Fatalf("issue: %d %s", w.Code, w.Body.String())
	}
	reads.calls = nil
	for _, path := range []string{
		"/api/prs/threads?repository=acme/other&pr=7",
		"/api/prs/timeline?repository=acme/other&pr=7",
		"/api/issue?repository=acme/other&number=3",
	} {
		if w := do(path); w.Code != 404 {
			t.Errorf("%s unpicked: %d", path, w.Code)
		}
	}
	for _, path := range []string{
		"/api/prs/threads?repository=acme/bare",
		"/api/prs/threads?repository=acme/bare&pr=0",
		"/api/prs/threads?repository=acme/bare&pr=x",
		"/api/prs/threads?repository=acme/bare&pr=7&state=bogus",
		"/api/prs/timeline?repository=acme/bare&pr=-1",
		"/api/issue?repository=acme/bare&pr=3",
		"/api/issue?repository=acme/bare&number=0",
	} {
		if w := do(path); w.Code != 400 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if len(reads.calls) != 0 {
		t.Errorf("refused requests reached GitHub: %v", reads.calls)
	}
	reads.err = &prupdate.Error{Code: prupdate.NotFound, Message: "It may be a pull request."}
	if w := do("/api/issue?repository=acme/bare&number=3"); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("not found: %d %s", w.Code, w.Body.String())
	}
	reads.err = &prupdate.Error{Code: prupdate.Failed, Message: "limiting"}
	if w := do("/api/prs/timeline?repository=acme/bare&pr=7"); w.Code != 502 || !strings.Contains(w.Body.String(), `"code":"failed"`) {
		t.Fatalf("failed: %d %s", w.Code, w.Body.String())
	}
}
