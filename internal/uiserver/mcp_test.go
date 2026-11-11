package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// mcpServer is testServer with the real MCP server behind /api/mcp.
func mcpServer(t *testing.T) (*Server, *agentcontext.Server) {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"path": r.URL.Path}) })
	mcp := agentcontext.New(agentcontext.Options{API: api, Version: "test"})
	s, err := New(Options{API: api, MCP: mcp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s, mcp
}

func TestMCPRouteTakesOnlyMCPTokens(t *testing.T) {
	s, mcp := mcpServer(t)
	token, _, revoke := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	exchange := request(t, s, "POST", "/api/auth/exchange", "", s.origin, "application/json", "", fmt.Sprintf(`{"token":%q}`, s.launch))
	cookie := exchange.Result().Cookies()[0]
	withCookie := func(method, path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, s.origin+path, strings.NewReader(ping))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.origin)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	namespaced := s.appPath() + "api/mcp"
	for _, tt := range []struct {
		name                       string
		method, path, origin, kind string
		auth                       string
		cookie                     bool
		status                     int
	}{
		{name: "MCP token, no Origin", method: "POST", path: "/api/mcp", kind: "application/json", auth: "Bearer " + token, status: 200},
		{name: "MCP token, own Origin", method: "POST", path: "/api/mcp", origin: s.origin, kind: "application/json", auth: "Bearer " + token, status: 200},
		{name: "MCP token, foreign Origin", method: "POST", path: "/api/mcp", origin: "https://evil.example", kind: "application/json", auth: "Bearer " + token, status: 403},
		{name: "MCP token, wrong content type", method: "POST", path: "/api/mcp", kind: "text/plain", auth: "Bearer " + token, status: 415},
		{name: "MCP token, GET", method: "GET", path: "/api/mcp", auth: "Bearer " + token, status: 405},
		{name: "no token", method: "POST", path: "/api/mcp", kind: "application/json", status: 401},
		{name: "launch token", method: "POST", path: "/api/mcp", origin: s.origin, kind: "application/json", auth: "Bearer " + s.launch, status: 401},
		{name: "cookie", method: "POST", path: "/api/mcp", cookie: true, status: 401},
		{name: "cookie in the namespace", method: "POST", path: namespaced, cookie: true, status: 404},
		{name: "launch token in the namespace", method: "POST", path: namespaced, cookie: true, auth: "Bearer " + s.launch, status: 404},
		{name: "MCP token in the namespace", method: "POST", path: namespaced, cookie: true, auth: "Bearer " + token, status: 404},
		{name: "sample namespace", method: "POST", path: s.appPath() + "sample/api/mcp", cookie: true, status: 404},
		{name: "MCP token on another route", method: "GET", path: "/api/repos", auth: "Bearer " + token, status: 401},
		{name: "MCP token on another route's mutation", method: "POST", path: "/api/repos", origin: s.origin, kind: "application/json", auth: "Bearer " + token, status: 401},
		{name: "MCP token on health", method: "GET", path: "/api/health", auth: "Bearer " + token, status: 401},
		{name: "MCP token on events", method: "GET", path: "/api/events", auth: "Bearer " + token, status: 401},
		{name: "MCP token in the namespace's API", method: "GET", path: s.appPath() + "api/repos", auth: "Bearer " + token, status: 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if tt.cookie {
				w = withCookie(tt.method, tt.path, tt.auth)
			} else {
				w = request(t, s, tt.method, tt.path, "", tt.origin, tt.kind, tt.auth, ping)
			}
			if w.Code != tt.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("security headers missing")
			}
			if tt.status == 200 && !strings.Contains(w.Body.String(), `"result":{}`) {
				t.Fatal(w.Body.String())
			}
		})
	}
	// The exact Host check applies to the MCP route too.
	if w := request(t, s, "POST", "/api/mcp", "localhost"+strings.TrimPrefix(s.origin, "http://127.0.0.1"), "", "application/json", "Bearer "+token, ping); w.Code != 403 {
		t.Fatal("wrong host", w.Code)
	}
	revoke()
	if w := request(t, s, "POST", "/api/mcp", "", "", "application/json", "Bearer "+token, ping); w.Code != 401 {
		t.Fatal("revoked token", w.Code)
	}
}

func TestMCPRouteUnservedWithoutServer(t *testing.T) {
	s := testServer(t)
	if w := request(t, s, "POST", "/api/mcp", "", "", "application/json", "Bearer "+s.launch, `{}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestMCPToolsReadTheAPI(t *testing.T) {
	s, mcp := mcpServer(t)
	if err := mcp.RegisterTool(agentcontext.Tool{Name: "repos", Description: "Lists repositories.", Call: func(ctx context.Context, c agentcontext.CallContext, _ json.RawMessage) (string, error) {
		response, err := c.Reader.Get(ctx, "/api/repos")
		return string(response.Body), err
	}}); err != nil {
		t.Fatal(err)
	}
	token, _, revoke := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	defer revoke()
	w := request(t, s, "POST", "/api/mcp", "", "", "application/json", "Bearer "+token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"repos"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `{\"path\":\"/api/repos\"}`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
