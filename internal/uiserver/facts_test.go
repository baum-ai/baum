package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeFacts struct {
	document []byte
	err      error
	heads    []string
	built    bool
}

func (f *fakeFacts) Facts(_ context.Context, clone repos.Clone, number int, head string) ([]byte, error) {
	f.heads = append(f.heads, head)
	if f.err != nil {
		return nil, f.err
	}
	f.built = true
	return f.document, nil
}

func (f *fakeFacts) Cached(repos.Clone, int) ([]byte, string) {
	if !f.built {
		return nil, ""
	}
	return f.document, "aaaaaaaa"
}

func factsRegistry(t *testing.T) (*repos.Registry, repos.Clone) {
	t.Helper()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	clone, err := registry.Add(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry, clone
}

func TestFactsAreServedBesideVersionsAndNeverAsOne(t *testing.T) {
	registry, clone := factsRegistry(t)
	facts := &fakeFacts{document: []byte(`{"schema":"baum.facts/1","review":{"repository":"owner/repo","pull_request":7,"head":"aaaaaaaa","base":"bbbbbbbb","merge_base":"bbbbbbbb"},"index":{"hunks":[{"id":"h_1","path":"main.go"}]}}`)}
	handler := LocalAPI{Registry: registry, Config: config.Default(), Facts: facts}.Handler()
	defer handler.Close()
	const linked = "cccccccccccccccccccccccccccccccccccccccc"
	handler.documents.resolve = func(context.Context, locallink.Request) (locallink.Result, error) {
		return locallink.Result{Available: true, Commits: []locallink.Commit{{OID: linked, Subject: "A change", Links: []locallink.Link{}}}}, nil
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
		return w
	}
	selector := "clone=" + clone.ID + "&pr=7"

	// Before the page asks for its facts, nothing is read against them.
	if w := get("/api/facts/sessions?" + selector); w.Code != 404 {
		t.Fatalf("sessions before facts: %d %s", w.Code, w.Body.String())
	}
	w := get("/api/facts?" + selector + "&head=aaaaaaaa")
	if w.Code != 200 || w.Body.String() != string(facts.document) || len(facts.heads) != 1 || facts.heads[0] != "aaaaaaaa" {
		t.Fatalf("facts: %d %s, heads %v", w.Code, w.Body.String(), facts.heads)
	}
	if w := get("/api/facts?" + selector + "&version=1"); w.Code != 400 {
		t.Errorf("facts at a version: %d", w.Code)
	}
	if w := get("/api/facts?clone=unknown&pr=7"); w.Code != 404 {
		t.Errorf("facts of an unregistered clone: %d", w.Code)
	}
	// The viewer's local routes answer for the facts as for a version.
	if w := get("/api/facts/sessions?" + selector); w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Errorf("sessions: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/facts/commits?" + selector); w.Code != 200 {
		t.Errorf("commits: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/facts/patch?" + selector + "&path=main.go"); w.Code != 200 || !strings.Contains(w.Body.String(), `"patch"`) {
		t.Errorf("patch: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/facts/patch?" + selector + "&path=other.go"); w.Code != 404 {
		t.Errorf("patch of a file the facts do not change: %d", w.Code)
	}
	// A linked commit is read from the clone, which lacks it here; any other
	// commit is not answered for.
	if w := get("/api/facts/commit?" + selector + "&oid=" + linked); w.Code != 200 || !strings.Contains(w.Body.String(), `"files":null`) {
		t.Errorf("commit: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/facts/commit?" + selector + "&oid=dddddddddddddddddddddddddddddddddddddddd"); w.Code != 404 {
		t.Errorf("an unlinked commit: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/facts/commit-patch?" + selector + "&oid=" + linked + "&path=main.go"); w.Code != 404 {
		t.Errorf("a patch of a commit the clone cannot read: %d %s", w.Code, w.Body.String())
	}
	// A page names the head of the facts it shows; facts of another head
	// never answer it.
	if w := get("/api/facts/patch?" + selector + "&head=aaaaaaaa&path=main.go"); w.Code != 200 {
		t.Errorf("patch at the facts' head: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/facts/patch?" + selector + "&head=cccccccc&path=main.go", "/api/facts/sessions?" + selector + "&head=cccccccc", "/api/facts/commits?" + selector + "&head=cccccccc", "/api/facts/sessions/s1?" + selector + "&head=cccccccc"} {
		if w := get(path); w.Code != 404 {
			t.Errorf("%s at a stale head: %d %s", path, w.Code, w.Body.String())
		}
	}
	// They are no version: the store lists none and serves no document.
	if w := get("/api/versions?" + selector); (w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]") && w.Code != 404 {
		t.Errorf("versions: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/document?" + selector); w.Code != 404 {
		t.Errorf("document: %d %s", w.Code, w.Body.String())
	}
	if w := get("/api/patch?" + selector + "&path=main.go"); w.Code != 404 {
		t.Errorf("a version's patch route read the facts: %d", w.Code)
	}

	resume := time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC)
	facts.err = &github.RateLimitedError{ResumeAt: resume}
	w = get("/api/facts?" + selector)
	var body struct {
		Code     string    `json:"code"`
		ResumeAt time.Time `json:"resume_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 503 || body.Code != "rate_limited" || !body.ResumeAt.Equal(resume) {
		t.Errorf("rate limited: %d %s", w.Code, w.Body.String())
	}
}

func TestFactsWithoutASourceAreUnavailable(t *testing.T) {
	registry, clone := factsRegistry(t)
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/facts?clone="+clone.ID+"&pr=7", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "facts_unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
