package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/hosted/agentplugins"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

func TestCodexHookTrustRouteRefreshesAndHandlesUnavailableProvider(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	state := "untrusted"
	api := LocalAPI{Registry: registry, HookTrust: func(ctx context.Context) agentplugins.HookTrust {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("query has no deadline")
		}
		return agentplugins.HookTrust{State: state}
	}}
	handler := api.Handler()
	defer handler.Close()
	for _, want := range []string{"untrusted", "trusted", "unknown"} {
		state = want
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/agent-hooks/codex", nil))
		var got agentplugins.HookTrust
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.State != want {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
	api.HookTrust = nil
	old := api.Handler()
	defer old.Close()
	w := httptest.NewRecorder()
	old.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/agent-hooks/codex", nil))
	if w.Code != 200 || w.Body.String() != "{\"available\":false}\n" {
		t.Fatalf("unavailable=%d %s", w.Code, w.Body.String())
	}
}
