package uiserver

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// A document rewritten in place, to the same length and within the same
// clock tick, is served as rewritten by /api/document and its viewer's
// routes, never from the viewer of the bytes before; an unchanged document is
// a 304 for its ETag and is not parsed again.
func TestRewrittenDocumentIsNeverServedStale(t *testing.T) {
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	dir := appdbtest.TempDir(t)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	clone, err := registry.Add(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "owner/repo", Number: 7}
	document := func(checks string) []byte {
		return []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":1,"head":"aaaaaaaa","base":"bbbbbbbb"},"checks":"` + checks + `"}`)
	}
	saved := store.Version{Metadata: store.Metadata{Version: 1}, Document: document("aaaa")}
	if err := versions.Put(ctx, key, store.Version{}, saved); err != nil {
		t.Fatal(err)
	}
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	handler.documents.resolve = func(context.Context, locallink.Request) (locallink.Result, error) {
		return locallink.Result{Available: true}, nil
	}
	get := func(path, etag string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "http://local"+path+"?clone="+clone.ID+"&pr=7", nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	builds := func() int {
		handler.documents.mu.Lock()
		defer handler.documents.mu.Unlock()
		return handler.documents.built
	}

	first := get("/api/document", "")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || first.Body.String() != string(saved.Document) || etag == "" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first read: %d %q %q %v", first.Code, etag, first.Body.String(), first.Header())
	}
	for range 3 {
		if w := get("/api/document", etag); w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != etag {
			t.Fatalf("unchanged document: %d %q %v", w.Code, w.Body.String(), w.Header())
		}
		if w := get("/api/sessions", ""); w.Code != 200 {
			t.Fatalf("sessions: %d", w.Code)
		}
	}
	if builds() != 1 {
		t.Fatal("an unchanged document was built again", builds())
	}

	previous, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, checks := range []string{"bbbb", "cccc"} {
		if err := versions.RefreshCurrent(ctx, key, previous, document(checks)); err != nil {
			t.Fatal(err)
		}
		if previous, err = versions.Latest(ctx, key); err != nil {
			t.Fatal(err)
		}
		w := get("/api/document", etag)
		if w.Code != 200 || w.Body.String() != string(document(checks)) || w.Header().Get("ETag") == etag {
			t.Fatalf("rewritten document %s: %d %q %q", checks, w.Code, w.Header().Get("ETag"), w.Body.String())
		}
		etag = w.Header().Get("ETag")
	}
	if builds() != 3 {
		t.Fatal("builds", builds())
	}
}
