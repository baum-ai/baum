package uiserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

// The prompt routes are mounted on the local API, and a repository one
// notifies a change like the other repository mutations.
func TestPromptRoutesMounted(t *testing.T) {
	registry, _ := decisionsRegistry(t)
	changes := 0
	handler := LocalAPI{Registry: registry, Config: config.Default(), Changed: func() { changes++ }}.Handler()
	defer handler.Close()
	serve := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if code, body := serve("GET", "/api/settings/prompts", ""); code != 200 || !strings.Contains(body, `"prompts":[{"id":"review"`) {
		t.Fatalf("%d %.200s", code, body)
	}
	if code, body := serve("PUT", "/api/settings/prompts/fix", `{"text":"mine"}`); code != 200 || !strings.Contains(body, `"custom_text":"mine"`) {
		t.Fatalf("%d %.200s", code, body)
	}
	if code, _ := serve("PUT", "/api/settings/prompts/fix", `{"text":null}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if code, _ := serve("GET", "/api/repositories/nobody/none/prompts", ""); code != http.StatusNotFound {
		t.Fatal(code)
	}
	if code, _ := serve("PUT", "/api/repositories/nobody/none/prompts/fix", `{"text":"x"}`); code != http.StatusNotFound || changes != 1 {
		t.Fatalf("%d, %d changes", code, changes)
	}
}
