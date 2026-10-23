package uiserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestLocalAPIMountsTheGitHubRepositoriesProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.Method + " " + r.URL.Path)) })
	api := LocalAPI{Registry: registry, GitHubRepositories: provider}.Handler()
	for _, request := range []struct{ method, path string }{
		{"GET", "/api/github/repositories"},
		{"GET", "/api/repositories/acme/widgets/clone-target"},
		{"GET", "/api/repositories/acme/widgets/clone"},
		{"POST", "/api/repositories/acme/widgets/clone"},
	} {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if want := request.method + " " + request.path; rec.Body.String() != want {
			t.Errorf("%s %s = %q, want the provider", request.method, request.path, rec.Body)
		}
	}

	// The registry's own routes next to them still work.
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/repositories", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("GET /api/repositories = %d %q", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/repositories/acme/widgets/settings", strings.NewReader(`{}`)))
	if rec.Code != 400 {
		t.Errorf("PUT settings = %d, want the registry's validation (400)", rec.Code)
	}

	rec = httptest.NewRecorder()
	LocalAPI{Registry: registry}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/github/repositories", nil))
	if !strings.Contains(rec.Body.String(), "github_repositories_unavailable") {
		t.Errorf("body = %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	LocalAPI{Registry: registry}.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/repositories/acme/widgets/clone", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST without a provider = %d", rec.Code)
	}
}
