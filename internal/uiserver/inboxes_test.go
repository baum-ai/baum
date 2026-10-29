package uiserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestLocalAPIMountsTheInboxesProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.Method)) })
	rec := httptest.NewRecorder()
	LocalAPI{Registry: registry, Inboxes: provider}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/inboxes", nil))
	if rec.Body.String() != "GET" {
		t.Errorf("body = %q", rec.Body)
	}
	rec = httptest.NewRecorder()
	LocalAPI{Registry: registry}.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/inboxes", nil))
	if !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), "inboxes_unavailable") {
		t.Errorf("body = %s", rec.Body)
	}
}
