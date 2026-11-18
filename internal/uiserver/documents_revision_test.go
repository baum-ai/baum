package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
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
// storedDocumentAPI is a local API over the store of a clone of owner/repo,
// with a GET of a path for its pull request 7.
type storedDocumentAPI struct {
	versions *store.Store
	key      store.Key
	handler  *LocalHandler
	get      func(path, etag string) *httptest.ResponseRecorder
}

func newStoredDocumentAPI(t *testing.T) storedDocumentAPI {
	t.Helper()
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
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
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	t.Cleanup(handler.Close)
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
	return storedDocumentAPI{versions: versions, key: store.Key{Repository: "owner/repo", Number: 7}, handler: handler, get: get}
}

func TestRewrittenDocumentIsNeverServedStale(t *testing.T) {
	ctx := context.Background()
	api := newStoredDocumentAPI(t)
	versions, key, handler, get := api.versions, api.key, api.handler, api.get
	document := func(checks string) []byte {
		return []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":1,"head":"aaaaaaaa","base":"bbbbbbbb"},"checks":"` + checks + `"}`)
	}
	saved := store.Version{Metadata: store.Metadata{Version: 1}, Document: document("aaaa")}
	if err := versions.Put(ctx, key, store.Version{}, saved); err != nil {
		t.Fatal(err)
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

// A version an earlier release stored with tiles on its sections and risks,
// of a kind the catalogue offered last, one it had retired and one it never
// had, is served without them.
func TestStoredDocumentIsServedWithoutTiles(t *testing.T) {
	api := newStoredDocumentAPI(t)
	tiles := `[{"id":"tile_fix","kind":"bug-fix","data":{"bug":"b","fix":"f"}},{"id":"tile_was","kind":"refactor","data":{"was":"w","now":"n"}},{"id":"tile_preview","kind":"ui-preview","data":{}}]`
	document := func(tiles string) string {
		return `{"review":{"repository":"owner/repo","pull_request":7,"version":1,"head":"aaaaaaaa","base":"bbbbbbbb"},"sections":[{"id":"sec_a","risks":[{"id":"rsk_a"` + tiles + `}]` + tiles + `}]}`
	}
	saved := store.Version{Metadata: store.Metadata{Version: 1}, Document: []byte(document(`,"tiles":` + tiles))}
	if err := api.versions.Put(context.Background(), api.key, store.Version{}, saved); err != nil {
		t.Fatal(err)
	}
	w := api.get("/api/document", "")
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	var got, want any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(document("")), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("served %s, want %s", w.Body.String(), document(""))
	}
}
