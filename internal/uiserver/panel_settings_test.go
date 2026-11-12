package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// The session panel's defaults follow the fix agent until the person chooses
// their own, and are kept in the application database across restarts.
func TestPanelSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	start := func() (*httptest.Server, *repos.Registry, func()) {
		t.Helper()
		registry, err := repos.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		handler := LocalAPI{Registry: registry}.Handler()
		server := httptest.NewServer(handler)
		return server, registry, func() { server.Close(); handler.Close(); _ = registry.Close() }
	}
	call := func(server *httptest.Server, method, body string, code int) repos.Panel {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/settings/panel", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != code {
			t.Fatalf("%s %s: got %d, want %d", method, body, response.StatusCode, code)
		}
		var panel repos.Panel
		if code == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&panel); err != nil {
				t.Fatal(err)
			}
		}
		return panel
	}
	server, registry, closeFirst := start()
	if got := call(server, "GET", "", 200); got != (repos.Panel{Agent: "claude-code", Mode: "full", Position: "bottom", AgentSource: "fix", ModelSource: "fix"}) {
		t.Fatalf("defaults: %#v", got)
	}
	codex, model := config.BackendCodex, "gpt-5"
	if _, err := registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
		p.FixBackend, p.FixModel = &codex, &model
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if got := call(server, "GET", "", 200); got.Agent != "codex" || got.Model != "gpt-5" || got.AgentSource != "fix" {
		t.Fatalf("the fix agent: %#v", got)
	}
	if got := call(server, "PUT", `{"agent":"claude-code","mode":"workspace","position":"top"}`, 200); got != (repos.Panel{Agent: "claude-code", Mode: "workspace", Position: "top", AgentSource: "panel", ModelSource: "panel"}) {
		t.Fatalf("another agent: %#v", got)
	}
	call(server, "PUT", `{"model":"opus"}`, 200)
	// Another agent drops the model chosen for the first, unless the same
	// change sets one.
	if got := call(server, "PUT", `{"agent":"codex"}`, 200); got.Agent != "codex" || got.Model != "gpt-5" || got.ModelSource != "fix" {
		t.Fatalf("another agent kept the model: %#v", got)
	}
	if got := call(server, "PUT", `{"agent":"claude-code","model":"opus"}`, 200); got.Model != "opus" {
		t.Fatalf("agent and model together: %#v", got)
	}
	if got := call(server, "PUT", `{"agent":"claude-code"}`, 200); got.Model != "opus" {
		t.Fatalf("the same agent dropped its model: %#v", got)
	}
	for _, invalid := range []string{`{"agent":"cursor"}`, `{"mode":"partial"}`, `{"position":"left"}`, `{"model":"a\nb"}`, `{"extra":1}`, `{} {}`} {
		call(server, "PUT", invalid, 400)
	}
	closeFirst()
	server, _, closeSecond := start()
	defer closeSecond()
	if got := call(server, "GET", "", 200); got != (repos.Panel{Agent: "claude-code", Model: "opus", Mode: "workspace", Position: "top", AgentSource: "panel", ModelSource: "panel"}) {
		t.Fatalf("after restart: %#v", got)
	}
	// An empty agent follows the fix agent again, model included.
	if got := call(server, "PUT", `{"agent":""}`, 200); got != (repos.Panel{Agent: "codex", Model: "gpt-5", Mode: "workspace", Position: "top", AgentSource: "fix", ModelSource: "fix"}) {
		t.Fatalf("back to the fix agent: %#v", got)
	}
}
