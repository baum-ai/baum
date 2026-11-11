//go:build live

package livecheck

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
)

// attempt is one run against the real CLI. A failed subtest prints the agent's
// steps and answer and the calls the server saw, so one paid run says what
// went wrong.
type attempt struct {
	t       *testing.T
	server  *recorder
	request agent.Request
	mu      sync.Mutex
	steps   []string
}

func newAttempt(t *testing.T, server *recorder, mcp agent.MCPServer) *attempt {
	t.Helper()
	server.reset()
	a := &attempt{t: t, server: server}
	// Task keeps Claude from framing the run as a pull request review.
	a.request = agent.Request{Directory: t.TempDir(), Prompt: question, Schema: []byte(schema), Task: true, MaxCalls: 6, MaxTokens: 200000, MCP: &mcp,
		Transcript: func(e agent.TranscriptEntry) {
			encoded, _ := json.Marshal(e)
			a.mu.Lock()
			a.steps = append(a.steps, string(encoded))
			a.mu.Unlock()
		}}
	return a
}

// finish checks the answer and the server's calls, and logs the evidence when
// the subtest failed.
func (a *attempt) finish(result agent.Result, err error, page string) {
	a.t.Helper()
	a.t.Cleanup(func() {
		if !a.t.Failed() {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.t.Logf("raw answer: %s", result.Document)
		a.t.Logf("tools/call names the server saw: %v", a.server.seen())
		for _, step := range a.steps {
			a.t.Logf("step: %s", step)
		}
	})
	if err != nil {
		a.t.Errorf("run: %v", err)
		return
	}
	if !strings.Contains(strings.ToLower(string(result.Document)), strings.ToLower(page)) {
		a.t.Errorf("the answer %q does not name the page %q", result.Document, page)
	}
	if !a.server.called("current_page") {
		a.t.Errorf("the server saw no tools/call for current_page; it saw %v", a.server.seen())
	}
}

func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

func TestLiveClaude(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CLAUDE") != "" {
		t.Skip("BAUM_LIVE_SKIP_CLAUDE is set")
	}
	server, mcp, page := fixture(t)
	backend := &claude.Backend{Model: os.Getenv("BAUM_LIVE_CLAUDE_MODEL")}
	// Run uses the review argument builder and Fix the fix one: together one
	// paid run covers both.
	t.Run("review", func(t *testing.T) {
		a := newAttempt(t, server, mcp)
		ctx, cancel := timeout()
		defer cancel()
		result, err := backend.Run(ctx, a.request)
		a.finish(result, err, page)
	})
	t.Run("fix", func(t *testing.T) {
		a := newAttempt(t, server, mcp)
		a.request.Schema = nil
		a.request.Prompt = question + " Do not change any file."
		ctx, cancel := timeout()
		defer cancel()
		result, err := backend.Fix(ctx, a.request)
		a.finish(result, err, page)
	})
}

func TestLiveCodex(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CODEX") != "" {
		t.Skip("BAUM_LIVE_SKIP_CODEX is set")
	}
	server, mcp, page := fixture(t)
	backend := &codex.Backend{Model: os.Getenv("BAUM_LIVE_CODEX_MODEL")}
	t.Run("read-only sandbox", func(t *testing.T) {
		a := newAttempt(t, server, mcp)
		ctx, cancel := timeout()
		defer cancel()
		result, err := backend.Run(ctx, a.request)
		a.finish(result, err, page)
	})
	t.Run("workspace-write sandbox", func(t *testing.T) {
		a := newAttempt(t, server, mcp)
		// A fix runs in the workspace-write sandbox and answers in text.
		a.request.Schema = nil
		a.request.Prompt = question + " Do not change any file."
		ctx, cancel := timeout()
		defer cancel()
		result, err := backend.Fix(ctx, a.request)
		a.finish(result, err, page)
	})
}
