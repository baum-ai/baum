package uiserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestLocalAPIMountsTheAnalyticsProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.URL.RawQuery)) })
	rec := httptest.NewRecorder()
	LocalAPI{Registry: registry, Analytics: provider}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/analytics?days=7", nil))
	if rec.Body.String() != "days=7" {
		t.Errorf("body = %q", rec.Body)
	}
	rec = httptest.NewRecorder()
	LocalAPI{Registry: registry}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/analytics", nil))
	if !strings.Contains(rec.Body.String(), "analytics_unavailable") {
		t.Errorf("body = %s", rec.Body)
	}
}
