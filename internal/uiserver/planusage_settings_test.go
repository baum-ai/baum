package uiserver

import (
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

func TestPlanUsageSettings(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	plans := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"plans":[{"provider":"claude","available":true,"windows":[]},{"provider":"codex","available":true,"windows":[]}]}`))
	})
	api := LocalAPI{Registry: registry, Config: cfg, PlanUsage: plans}
	handler := api.Handler()
	defer handler.Close()
	call := func(method, path, body string, code int) []planEntry {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != code {
			t.Fatalf("%s %s %s: %d, want %d: %s", method, path, body, rec.Code, code, rec.Body)
		}
		var out struct {
			Plans []planEntry `json:"plans"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Plans
	}
	shown := func(path string) string {
		var names []string
		for _, p := range call("GET", path, "", 200) {
			names = append(names, p.Provider)
		}
		return strings.Join(names, ",")
	}
	settings := func() string {
		var out []string
		for _, p := range call("GET", "/api/settings/plan-usage", "", 200) {
			if p.Shown {
				out = append(out, p.Provider)
			}
		}
		return strings.Join(out, ",")
	}
	// Default: the reviews' backend only.
	if got := settings(); got != "claude" {
		t.Fatalf("default shown: %q", got)
	}
	// The served plans follow the same default.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/plan-usage", nil))
	if strings.Contains(rec.Body.String(), "codex") || !strings.Contains(rec.Body.String(), "claude") {
		t.Fatalf("filtered plans: %s", rec.Body)
	}
	// A fix backend or decision backend adds its plan.
	if _, err := registry.UpdatePreferences(t.Context(), func(p repos.Preferences) repos.Preferences {
		p.FixBackend = new(config.BackendCodex)
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if got := settings(); got != "claude,codex" {
		t.Fatalf("with fix backend: %q", got)
	}
	// An API backend has no plan; the Cursor and Anthropic backends add none.
	if _, err := registry.UpdatePreferences(t.Context(), func(p repos.Preferences) repos.Preferences {
		p.FixBackend = new(config.BackendAnthropic)
		p.Backend = new(config.BackendCursorAgent)
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if got := settings(); got != "" {
		t.Fatalf("backends without plans: %q", got)
	}
	// Auto uses the first plan that can be read.
	if _, err := registry.UpdatePreferences(t.Context(), func(p repos.Preferences) repos.Preferences {
		p.FixBackend = nil
		p.Backend = new(config.BackendAuto)
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if got := settings(); got != "claude" {
		t.Fatalf("auto: %q", got)
	}
	// An explicit choice survives a backend change; the other plan still follows.
	call("PUT", "/api/settings/plan-usage", `{"provider":"codex","shown":true}`, 200)
	call("PUT", "/api/settings/plan-usage", `{"provider":"claude","shown":false}`, 200)
	if got := settings(); got != "codex" {
		t.Fatalf("explicit: %q", got)
	}
	if _, err := registry.UpdatePreferences(t.Context(), func(p repos.Preferences) repos.Preferences {
		p.Backend = new(config.BackendClaude)
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if got := shown("/api/plan-usage"); got != "codex" {
		t.Fatalf("unexpected list %q", got)
	}
	entries := call("GET", "/api/settings/plan-usage", "", 200)
	if entries[0].Shown || !entries[0].Default || !entries[0].Explicit || !entries[1].Shown || entries[1].Default || !entries[1].Explicit {
		t.Fatalf("after backend change: %#v", entries)
	}
	// Null returns a plan to following the backends.
	call("PUT", "/api/settings/plan-usage", `{"provider":"claude","shown":null}`, 200)
	call("PUT", "/api/settings/plan-usage", `{"provider":"codex","shown":null}`, 200)
	if got := settings(); got != "claude" {
		t.Fatalf("reset: %q", got)
	}
	for _, bad := range []string{`{"provider":"cursor","shown":true}`, `{"provider":"codex","shown":"yes"}`, `{"provider":"codex","x":1}`, `{}{}`} {
		call("PUT", "/api/settings/plan-usage", bad, 400)
	}
}
