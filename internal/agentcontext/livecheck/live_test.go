//go:build live

// Package livecheck runs the real Claude Code and Codex CLIs against baum's
// MCP server, to check what the argument tests cannot: that each CLI starts
// "baum mcp", forwards BAUM_MCP_TOKEN to it, sees the tools through
// --strict-mcp-config / --ignore-user-config, and is allowed to call them in a
// non-interactive run. It needs the CLIs installed and signed in on the host,
// spends a few model calls, and is excluded from every normal test run.
//
// Run: cd oss && go test -tags live -run TestLive ./internal/agentcontext/livecheck/ -v
//
// Optional environment: BAUM_LIVE_BAUM (path of a built baum binary; default:
// built from ./cmd/baum), BAUM_LIVE_CLAUDE_MODEL and BAUM_LIVE_CODEX_MODEL (a
// cheap model for each CLI), BAUM_LIVE_SKIP_CLAUDE / BAUM_LIVE_SKIP_CODEX.
package livecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
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

// fixture starts the app server on a loopback port with an MCP server, writes
// a daemon state directory whose credential points at it, and points the
// environment's XDG_STATE_HOME there so "baum mcp" finds it rather than the
// real daemon's credential. baum's allowlist lets XDG_STATE_HOME reach the agent
// CLI; Codex then forwards it to the MCP server only because the backend names
// it in mcp_servers.baum.env_vars, and Claude Code is expected to give its stdio
// servers its own environment (what this check also verifies).
func fixture(t *testing.T) (*recorder, agent.MCPServer, string) {
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

	state := t.TempDir()
	daemonDir := filepath.Join(state, "baum", "daemon")
	if err := os.MkdirAll(daemonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The token half of the credential is never used by the bridge.
	credential, _ := json.Marshal(map[string]string{"url": app.Origin() + "/#token=" + strings.Repeat("d", 43)})
	if err := os.WriteFile(filepath.Join(daemonDir, "credential.json"), credential, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", state)

	token, id, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	if err := mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PageSettings}); err != nil {
		t.Fatal(err)
	}
	return mcp, agent.MCPServer{Command: baumBinary(t), Args: []string{"mcp"}, Token: token}, agentcontext.PageSettings
}

// baumBinary is BAUM_LIVE_BAUM or a build of ./cmd/baum.
func baumBinary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("BAUM_LIVE_BAUM"); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	out := filepath.Join(t.TempDir(), "baum")
	build := exec.Command("go", "build", "-o", out, "github.com/baum-ai/baum/cmd/baum")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building baum: %v\n%s", err, output)
	}
	return out
}
