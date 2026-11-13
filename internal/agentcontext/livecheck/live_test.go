//go:build live

// Package livecheck runs the real Claude Code and Codex CLIs against baum's
// MCP server, to check what the argument tests cannot: that each CLI reaches
// the daemon's /api/mcp over HTTP with the token from BAUM_MCP_TOKEN (Claude
// Code expanding it in the header, Codex through bearer_token_env_var), sees
// the tools through --strict-mcp-config / --ignore-user-config, is allowed to
// call them in a non-interactive run, never shows the token in a command line
// or writes it to its configuration, and finishes without baum's tools, rather
// than hanging or starting an OAuth flow, when the token was revoked. It needs
// the CLIs installed and signed in on the host, spends a few model calls, and
// is excluded from every normal test run.
//
// Run: cd oss && go test -tags live -run TestLive ./internal/agentcontext/livecheck/ -v
//
// Optional environment: BAUM_LIVE_CLAUDE_BIN and BAUM_LIVE_CODEX_BIN (an
// executable to test instead of the one on PATH, such as a floor release),
// BAUM_LIVE_CLAUDE_MODEL and BAUM_LIVE_CODEX_MODEL (a cheap model for each
// CLI), BAUM_LIVE_SKIP_CLAUDE / BAUM_LIVE_SKIP_CODEX.
package livecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/uiserver"
)

const question = "Call the baum current_page tool and reply with the page name only."

// The review run submits its answer as a document; the fix run answers in text.
const schema = `{"type":"object","properties":{"page":{"type":"string","description":"the page name the current_page tool reported"}},"required":["page"],"additionalProperties":false}`

// recorder is the MCP server the app server routes /api/mcp to, noting every
// tools/call it receives.
type recorder struct {
	*agentcontext.Server
	mu    sync.Mutex
	calls []string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	var message struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &message) == nil && message.Method == "tools/call" {
		r.mu.Lock()
		r.calls = append(r.calls, message.Params.Name)
		r.mu.Unlock()
	}
	r.Server.ServeHTTP(w, req)
}

func (r *recorder) called(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, call := range r.calls {
		if call == name {
			return true
		}
	}
	return false
}

// seen is a copy of the tools/call names received so far.
func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
}

// wire is a proxy in front of the app server that notes every request a CLI
// sends, whatever its path or credential, so a check sees requests the MCP
// server never gets: refused ones and any OAuth discovery.
type wire struct {
	mu       sync.Mutex
	requests []request
	token    string // the token the current attempt was given
}

// request is one request the proxy saw.
type request struct {
	Method, Path, RPC string
	Status            int
	// Auth is "token" when the Authorization header carried the attempt's
	// token, "none" without one, and the header as sent otherwise.
	Auth string
}

func (r request) String() string {
	return fmt.Sprintf("%s %s %s -> %d (auth %s)", r.Method, r.Path, r.RPC, r.Status, r.Auth)
}

func (w *wire) expect(token string) {
	w.mu.Lock()
	w.requests, w.token = nil, token
	w.mu.Unlock()
}

func (w *wire) seen() []request {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]request(nil), w.requests...)
}

// fixture starts the app server on a loopback port with an MCP server and
// the recording proxy in front of it, and returns the URL a run reaches.
func fixture(t *testing.T) (*recorder, *wire, string) {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) })
	mcp := &recorder{Server: agentcontext.New(agentcontext.Options{API: api, Version: "live"})}
	app, err := uiserver.New(uiserver.Options{API: api, MCP: mcp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})

	target, err := url.Parse(app.Origin())
	if err != nil {
		t.Fatal(err)
	}
	// SetURL also sets Host to the app server's, which it checks exactly.
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) }}
	w := &wire{}
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var message struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &message)
		status := &statusWriter{ResponseWriter: rw, status: http.StatusOK}
		proxy.ServeHTTP(status, r)
		w.mu.Lock()
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "":
			auth = "none"
		case w.token != "" && auth == "Bearer "+w.token:
			auth = "token"
		}
		w.requests = append(w.requests, request{Method: r.Method, Path: r.URL.Path, RPC: message.Method, Status: status.status, Auth: auth})
		w.mu.Unlock()
	}))
	front.Listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	front.Start()
	t.Cleanup(front.Close)
	return mcp, w, front.URL + "/api/mcp"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
