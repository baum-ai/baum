package uiserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/uistate"
)

func TestUIStateSurvivesNewOriginBehindSecurityBoundary(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	start := func() *Server {
		t.Helper()
		registry, err := repos.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		api := LocalAPI{Registry: registry, UIState: (&uistate.Store{Path: path}).Handler()}.Handler()
		t.Cleanup(func() { api.Close(); _ = registry.Close() })
		server, err := New(Options{API: api})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = server.Listen(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.listener.Close() })
		return server
	}
	s := start()
	body := `{"revision":0,"mutation_id":"mutation-00000001:1","value":{"seen":["one"],"notes":{"one":"draft"},"note":"overall"}}`
	route := "api/ui-state/walkthrough?key=acme%2Fdemo%237%40v1"
	if w := request(t, s, "PUT", "/"+route, "", s.origin, "application/json", "Bearer "+s.launch, body); w.Code != 200 {
		t.Fatalf("write %d %s", w.Code, w.Body.String())
	}
	for _, tt := range []struct {
		path, origin, auth string
		status             int
	}{
		{"/" + route, s.origin, "", 401},
		{s.appPath() + route, "https://evil.example", "Bearer " + s.launch, 403},
		{"/s/wrong/" + route, s.origin, "Bearer " + s.launch, 404},
	} {
		if w := request(t, s, "PUT", tt.path, "", tt.origin, "application/json", tt.auth, body); w.Code != tt.status {
			t.Fatalf("security %d %s", w.Code, w.Body.String())
		}
	}
	reopened := start()
	got := request(t, reopened, "GET", "/"+route, "", "", "", "Bearer "+reopened.launch, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"one":"draft"`) || !strings.Contains(got.Body.String(), `"revision":1`) {
		t.Fatalf("reopen %d %s", got.Code, got.Body.String())
	}
	exchange := request(t, reopened, "POST", "/api/auth/exchange", "", reopened.origin, "application/json", "", fmt.Sprintf(`{"token":%q}`, reopened.launch))
	cookie := exchange.Result().Cookies()[0]
	request := httptest.NewRequest(http.MethodGet, reopened.origin+reopened.appPath()+route, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	reopened.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("cookie %d", response.Code)
	}
}
