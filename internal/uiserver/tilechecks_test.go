package uiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tileframes"
)

// TestTileCheckRoutes checks that the render check's routes sit behind the
// server's boundary like every other route: the Host, the session, and for
// a report or a preflight a same-origin JSON request; and that a browser
// request from anywhere but the app's own pages is refused.
func TestTileCheckRoutes(t *testing.T) {
	checker, err := tilechecks.New(tilechecks.Options{Frames: tileframes.New(tileframes.MaxBytes), Wait: time.Second, Claim: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = checker.Close() }()
	s, err := New(Options{API: checker.Handler()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { stop(); <-done }()
	bearer := "Bearer " + s.launch
	report := `{"problems":[],"runs":[],"engine":"x"}`
	preflight := `{"digest":"sha256:` + strings.Repeat("a", 64) + `","data":{},"size":"half","refs":{"glance":{},"detail":{}}}`
	for _, tt := range []struct {
		name, method, path, host, origin, kind, auth, site, body string
		status                                                   int
	}{
		{name: "next without a session", method: "GET", path: "/api/tile-checks/next?wait=0", status: 401},
		{name: "next with the wrong token", method: "GET", path: "/api/tile-checks/next?wait=0", auth: "Bearer wrong", status: 401},
		{name: "next on another host", method: "GET", path: "/api/tile-checks/next?wait=0", host: "attacker.example", auth: bearer, status: 403},
		{name: "next", method: "GET", path: "/api/tile-checks/next?wait=0", auth: bearer, status: 204},
		{name: "next from another site", method: "GET", path: "/api/tile-checks/next?wait=0", auth: bearer, site: "cross-site", status: 403},
		{name: "next typed into the address bar", method: "GET", path: "/api/tile-checks/next?wait=0", auth: bearer, site: "none", status: 403},
		{name: "next from the app", method: "GET", path: "/api/tile-checks/next?wait=0", auth: bearer, site: "same-origin", status: 204},
		{name: "a bad wait", method: "GET", path: "/api/tile-checks/next?wait=-1", auth: bearer, status: 400},
		{name: "report without an origin", method: "PUT", path: "/api/tile-checks/chk_x/result", kind: "application/json", auth: bearer, body: report, status: 403},
		{name: "report from another origin", method: "PUT", path: "/api/tile-checks/chk_x/result", origin: "https://attacker.example", kind: "application/json", auth: bearer, body: report, status: 403},
		{name: "report as a form", method: "PUT", path: "/api/tile-checks/chk_x/result", origin: s.origin, kind: "text/plain", auth: bearer, body: report, status: 403},
		{name: "report without a session", method: "PUT", path: "/api/tile-checks/chk_x/result", origin: s.origin, kind: "application/json", body: report, status: 401},
		{name: "report for no running check", method: "PUT", path: "/api/tile-checks/chk_x/result", origin: s.origin, kind: "application/json", auth: bearer, body: report, status: 404},
		{name: "report that is not JSON", method: "PUT", path: "/api/tile-checks/chk_x/result", origin: s.origin, kind: "application/json", auth: bearer, body: "{", status: 400},
		{name: "preflight without an origin", method: "POST", path: "/api/tile-checks/preflight", kind: "application/json", auth: bearer, body: preflight, status: 403},
		{name: "preflight without a digest", method: "POST", path: "/api/tile-checks/preflight", origin: s.origin, kind: "application/json", auth: bearer, body: `{"data":{}}`, status: 400},
		{name: "preflight of an unregistered type", method: "POST", path: "/api/tile-checks/preflight", origin: s.origin, kind: "application/json", auth: bearer, body: preflight, status: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := http.NewRequest(tt.method, s.Origin()+tt.path, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if tt.host != "" {
				r.Host = tt.host
			}
			for name, value := range map[string]string{"Origin": tt.origin, "Content-Type": tt.kind, "Authorization": tt.auth, "Sec-Fetch-Site": tt.site} {
				if value != "" {
					r.Header.Set(name, value)
				}
			}
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.status)
			}
		})
	}
	if err := s.Publish("tile_check", tilechecks.Event{ID: "chk_x", State: tilechecks.StateQueued}); err != nil {
		t.Fatalf("the tile_check event is refused: %v", err)
	}
}
