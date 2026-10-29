package uiserver

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestAgentRequestRoutesAreAuthenticatedAndOptional(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	served := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"path": r.URL.Path})
	})
	for name, tt := range map[string]struct {
		handler http.Handler
		get     int
		post    int
	}{
		"served":      {served, http.StatusOK, http.StatusOK},
		"unavailable": {nil, http.StatusOK, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			handler := LocalAPI{Registry: registry, AgentRequests: tt.handler}.Handler()
			defer handler.Close()
			s, err := New(Options{API: handler})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Listen(0); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.listener.Close() }()
			for _, path := range []string{"/api/agent-requests", "/api/agent-requests/x/answer", "/api/agent-messages", "/api/agent-messages/claude-code/s1"} {
				if w := request(t, s, "GET", path, "", "", "", "", ""); w.Code != http.StatusUnauthorized {
					t.Fatalf("%s without credentials: %d", path, w.Code)
				}
			}
			if w := request(t, s, "GET", "/api/agent-requests", "", "", "", "Bearer "+s.launch, ""); w.Code != tt.get {
				t.Fatalf("list: %d %s", w.Code, w.Body.String())
			}
			if w := request(t, s, "POST", "/api/agent-messages", "", s.origin, "application/json", "Bearer "+s.launch, "{}"); w.Code != tt.post {
				t.Fatalf("queue: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
