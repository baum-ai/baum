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
		switch {
		case r.Method != "GET":
			http.NotFound(w, r)
		case r.URL.Path == "/api/live-sessions":
			writeJSON(w, map[string]any{"sessions": []any{}})
		case r.URL.Path == "/api/live-sessions/claude-code/s1/transcript":
			writeJSON(w, map[string]any{"available": true, "version": "v1"})
		default:
			http.NotFound(w, r)
		}
	})
	for name, tt := range map[string]struct {
		live                 http.Handler
		want, wantTranscript string
	}{
		"served":      {live, `{"sessions":[]}`, `"version":"v1"`},
		"unavailable": {nil, `"live_sessions_unavailable"`, `"live_sessions_unavailable"`},
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
			// A session's transcript is behind the same boundary.
			const transcript = "/api/live-sessions/claude-code/s1/transcript"
			if w := request(t, s, "GET", transcript, "", "", "", "", ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("transcript without credentials: %d", w.Code)
			}
			w = request(t, s, "GET", transcript, "", "", "", "Bearer "+s.launch, "")
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tt.wantTranscript) {
				t.Fatalf("transcript: %d %s", w.Code, w.Body.String())
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
	// The Timeline page reads the decision log again on this one.
	if err := s.Publish("timeline", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish("sessions", struct{}{}); err == nil {
		t.Fatal("an unknown event kind was accepted")
	}
}
