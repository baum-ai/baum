package uiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestAppearanceSurvivesNewDaemonOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	start := func() (*httptest.Server, func()) {
		t.Helper()
		registry, err := repos.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		handler := LocalAPI{Registry: registry}.Handler()
		server := httptest.NewServer(handler)
		return server, func() { server.Close(); handler.Close(); _ = registry.Close() }
	}
	call := func(server *httptest.Server, method, body string, code int) appearanceSettings {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/settings/appearance", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != code {
			t.Fatalf("%s %s: got %d, want %d", method, body, response.StatusCode, code)
		}
		var saved appearanceSettings
		if code == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&saved); err != nil {
				t.Fatal(err)
			}
		}
		return saved
	}
	server, closeFirst := start()
	if got := call(server, "GET", "", 200); got != (appearanceSettings{Theme: "system", KeyHints: true}) {
		t.Fatalf("defaults: %#v", got)
	}
	call(server, "PUT", `{"theme":"dark","key_hints":false}`, 200)
	for _, invalid := range []string{`{"theme":"sepia"}`, `{"key_hints":"false"}`, `{"extra":true}`, `{} {}`} {
		call(server, "PUT", invalid, 400)
	}
	closeFirst()
	// Reopen the on-disk registry behind a newly allocated HTTP origin, as a
	// daemon restart does, with no browser storage or cookies carried over.
	server, closeSecond := start()
	defer closeSecond()
	if got := call(server, "GET", "", 200); got != (appearanceSettings{Theme: "dark", KeyHints: false}) {
		t.Fatalf("after restart: %#v", got)
	}
	if got := call(server, "PUT", `{"theme":"light"}`, 200); got != (appearanceSettings{Theme: "light", KeyHints: false}) {
		t.Fatalf("partial theme edit: %#v", got)
	}
	if got := call(server, "PUT", `{"key_hints":true}`, 200); got != (appearanceSettings{Theme: "light", KeyHints: true}) {
		t.Fatalf("partial hints edit: %#v", got)
	}
}
