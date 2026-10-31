package uiserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

func TestLocalAPIMountsTheClonesProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.Method + " " + r.URL.Path)) })

	api := LocalAPI{Registry: registry, Clones: provider}.Handler()
	for _, request := range []struct{ method, path string }{{"GET", "/api/clones/found"}, {"POST", "/api/clones/found/refresh"}} {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if want := request.method + " " + request.path; rec.Body.String() != want {
			t.Errorf("%s = %q, want %q", request.path, rec.Body, want)
		}
	}

	// Without a provider the capability is reported unavailable.
	rec := httptest.NewRecorder()
	LocalAPI{Registry: registry}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/clones/found", nil))
	if !strings.Contains(rec.Body.String(), "clones_unavailable") {
		t.Errorf("body = %s", rec.Body)
	}
}
