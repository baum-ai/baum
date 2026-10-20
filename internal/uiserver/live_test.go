package uiserver

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestLiveSessionsRouteIsAuthenticatedAndOptional(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	live := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/live-sessions" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"sessions": []any{}})
	})
	for name, tt := range map[string]struct {
		live http.Handler
		want string
	}{
		"served":      {live, `{"sessions":[]}`},
		"unavailable": {nil, `"live_sessions_unavailable"`},
	} {
		t.Run(name, func(t *testing.T) {
			handler := LocalAPI{Registry: registry, Live: tt.live}.Handler()
			defer handler.Close()
			s, err := New(Options{API: handler})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Listen(0); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.listener.Close() }()
			if w := request(t, s, "GET", "/api/live-sessions", "", "", "", "", ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("without credentials: %d", w.Code)
			}
			if w := request(t, s, "GET", "/api/live-sessions", "", "", "", "Bearer wrong", ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("with a wrong token: %d", w.Code)
			}
			w := request(t, s, "GET", "/api/live-sessions", "", "", "", "Bearer "+s.launch, "")
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestLiveEventsAreNotificationsOnTheStream(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Publish("live", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish("sessions", struct{}{}); err == nil {
		t.Fatal("an unknown event kind was accepted")
	}
}
