//go:build live

package livecheck

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentcontext"
)

type run func(context.Context, agent.Request) (agent.Result, error)

// attempt is one run against the real CLI with a token of its own. A failed
// subtest prints the agent's steps and answer and the requests the proxy saw,
// so one paid run says what went wrong.
type attempt struct {
	t       *testing.T
	server  *recorder
	wire    *wire
	token   string
	request agent.Request
	mu      sync.Mutex
	steps   []string
}

// newAttempt mints a token for a chat on the settings page; revoked revokes
// it before the run.
func newAttempt(t *testing.T, server *recorder, w *wire, endpoint string, revoked bool) *attempt {
	t.Helper()
	token, id, revoke, err := server.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PageSettings}); err != nil {
		t.Fatal(err)
	}
	if revoked {
		revoke()
	} else {
		t.Cleanup(revoke)
	}
	server.reset()
	w.expect(token)
	a := &attempt{t: t, server: server, wire: w, token: token}
	// Task keeps Claude from framing the run as a pull request review.
	a.request = agent.Request{Directory: t.TempDir(), Prompt: question, Schema: []byte(schema), Task: true, MaxCalls: 6, MaxTokens: 200000,
		MCP: &agent.MCPServer{URL: endpoint, Token: token},
		Transcript: func(e agent.TranscriptEntry) {
			encoded, _ := json.Marshal(e)
			a.mu.Lock()
			a.steps = append(a.steps, string(encoded))
			a.mu.Unlock()
		}}
	return a
}

// text makes the attempt a fix: no schema, and an answer in text.
func (a *attempt) text() {
	a.request.Schema = nil
	a.request.Prompt = question + " Do not change any file."
}

// do runs the attempt, watching every command line for the token, and checks
// what holds for any run: the token stayed out of command lines and of the
// CLIs' files, and nothing but the MCP route was requested (no OAuth
// discovery).
func (a *attempt) do(fn run) (agent.Result, error) {
	a.t.Helper()
	started := time.Now()
	watching := watchCommandLines(a.token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := fn(ctx, a.request)
	inArgv := watching()
	a.t.Cleanup(func() {
		if !a.t.Failed() {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.t.Logf("raw answer: %s (error %v)", result.Document, err)
		a.t.Logf("tools/call names the server saw: %v", a.server.seen())
		for _, r := range a.wire.seen() {
			a.t.Logf("request: %s", r)
		}
		for _, step := range a.steps {
			a.t.Logf("step: %s", step)
		}
	})
	if ctx.Err() != nil {
		a.t.Errorf("the run did not finish within its timeout")
	}
	if len(inArgv) > 0 {
		a.t.Errorf("the token was in a command line: %q", inArgv)
	}
	if files := filesHolding(a.token, started); len(files) > 0 {
		a.t.Errorf("the token was written to %v", files)
	}
	for _, r := range a.wire.seen() {
		if r.Path != "/api/mcp" {
			a.t.Errorf("the CLI requested %s", r)
		}
	}
	return result, err
}

// works checks a run with a live token: it answered with the page, called the
// tool, and sent the token with every request.
func (a *attempt) works(fn run) {
	a.t.Helper()
	result, err := a.do(fn)
	if err != nil {
		a.t.Errorf("run: %v", err)
		return
	}
	if !strings.Contains(strings.ToLower(string(result.Document)), agentcontext.PageSettings) {
		a.t.Errorf("the answer %q does not name the page %q", result.Document, agentcontext.PageSettings)
	}
	if !a.server.called("current_page") {
		a.t.Errorf("the server saw no tools/call for current_page; it saw %v", a.server.seen())
	}
	for _, r := range a.wire.seen() {
		if r.Auth != "token" {
			a.t.Errorf("a request did not carry the token: %s", r)
		}
	}
}

// refused checks a run whose token was revoked: it finished (checked by do)
// and called nothing.
func (a *attempt) refused(fn run) {
	a.t.Helper()
	a.request.Prompt += " If the baum tool is unavailable, reply UNAVAILABLE."
	if _, err := a.do(fn); err != nil {
		a.t.Logf("run: %v", err)
	}
	if calls := a.server.seen(); len(calls) > 0 {
		a.t.Errorf("a revoked token called %v", calls)
	}
	if len(a.wire.seen()) == 0 {
		a.t.Error("the CLI never tried the MCP server")
	}
}

// watchCommandLines notes, until the returned function is called, every
// process command line that holds token.
func watchCommandLines(token string) func() []string {
	var mu sync.Mutex
	var found []string
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			entries, _ := os.ReadDir("/proc")
			for _, entry := range entries {
				line, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
				if err == nil && strings.Contains(string(line), token) {
					mu.Lock()
					found = append(found, strings.ReplaceAll(string(line), "\x00", " "))
					mu.Unlock()
				}
			}
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return func() []string {
		close(done)
		<-stopped
		mu.Lock()
		defer mu.Unlock()
		return found
	}
}

// filesHolding lists the files in the CLIs' homes, changed since since, that
// hold token.
func filesHolding(token string, since time.Time) []string {
	home, _ := os.UserHomeDir()
	var found []string
	for _, root := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), filepath.Join(home, ".codex"),
		os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CODEX_HOME")} {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().Before(since) || info.Size() > 64<<20 {
				return nil
			}
			if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), token) {
				found = append(found, path)
			}
			return nil
		})
	}
	return found
}

func TestLiveClaude(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CLAUDE") != "" {
		t.Skip("BAUM_LIVE_SKIP_CLAUDE is set")
	}
	server, w, endpoint := fixture(t)
	backend := &claude.Backend{Binary: os.Getenv("BAUM_LIVE_CLAUDE_BIN"), Model: os.Getenv("BAUM_LIVE_CLAUDE_MODEL")}
	// Run uses the review argument builder and Fix the fix one: together one
	// paid run covers both.
	t.Run("review", func(t *testing.T) { newAttempt(t, server, w, endpoint, false).works(backend.Run) })
	t.Run("fix", func(t *testing.T) {
		a := newAttempt(t, server, w, endpoint, false)
		a.text()
		a.works(backend.Fix)
	})
	t.Run("revoked token", func(t *testing.T) { newAttempt(t, server, w, endpoint, true).refused(backend.Run) })
}

func TestLiveCodex(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CODEX") != "" {
		t.Skip("BAUM_LIVE_SKIP_CODEX is set")
	}
	server, w, endpoint := fixture(t)
	backend := &codex.Backend{Binary: os.Getenv("BAUM_LIVE_CODEX_BIN"), Model: os.Getenv("BAUM_LIVE_CODEX_MODEL")}
	t.Run("read-only sandbox", func(t *testing.T) { newAttempt(t, server, w, endpoint, false).works(backend.Run) })
	t.Run("workspace-write sandbox", func(t *testing.T) {
		// A fix runs in the workspace-write sandbox and answers in text.
		a := newAttempt(t, server, w, endpoint, false)
		a.text()
		a.works(backend.Fix)
	})
	t.Run("revoked token", func(t *testing.T) { newAttempt(t, server, w, endpoint, true).refused(backend.Run) })
}
