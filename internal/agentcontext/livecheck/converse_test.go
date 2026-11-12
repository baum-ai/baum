//go:build live

package livecheck

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
)

// turn runs one Converse turn and logs what a person would see.
func turn(t *testing.T, c agent.Converser, dir, prompt, resume string, access agent.Access, mcp agent.MCPServer) (agent.Result, []string) {
	t.Helper()
	var mu sync.Mutex
	var started []string
	var deltas int
	req := agent.Request{Directory: dir, Prompt: prompt, ResumeSession: resume, MCP: &mcp,
		SessionStarted: func(id string) { mu.Lock(); started = append(started, id); mu.Unlock() },
		TextDelta:      func(string) { mu.Lock(); deltas++; mu.Unlock() },
	}
	ctx, cancel := timeout()
	defer cancel()
	result, err := c.Converse(ctx, req, access)
	t.Logf("access=%s resume=%q err=%v session=%q started=%v deltas=%d", access, resume, err, result.SessionID, started, deltas)
	t.Logf("answer: %s", result.Document)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	return result, started
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func conversePair(t *testing.T, c agent.Converser) {
	server, mcp, page := fixture(t)

	t.Run("full", func(t *testing.T) {
		server.reset()
		dir := t.TempDir()
		r1, started := turn(t, c, dir,
			"Call the baum current_page tool. Then run this exact shell command: echo panel > probe.txt . Reply with the page name only.",
			"", agent.AccessFull, mcp)
		if !server.called("current_page") {
			t.Errorf("baum current_page not called; saw %v", server.seen())
		}
		if !strings.Contains(strings.ToLower(string(r1.Document)), page) {
			t.Errorf("answer does not name page %q", page)
		}
		if !exists(dir, "probe.txt") {
			t.Errorf("full access: shell command did not create probe.txt")
		}
		if len(started) == 0 || started[0] != r1.SessionID {
			t.Errorf("SessionStarted %v vs result session %q", started, r1.SessionID)
		}
		r2, _ := turn(t, c, dir, "What exact file name did you create in the previous turn? Reply with the file name only.", r1.SessionID, agent.AccessFull, mcp)
		if !strings.Contains(string(r2.Document), "probe.txt") {
			t.Errorf("resume lost context: %q", r2.Document)
		}
		if r2.SessionID != r1.SessionID {
			t.Logf("note: resumed session id %q differs from %q", r2.SessionID, r1.SessionID)
		}
	})

	t.Run("workspace with a conflicting project baum server", func(t *testing.T) {
		server.reset()
		dir := t.TempDir()
		// A project-scope MCP server also named baum, approved, that cannot start.
		_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0o700)
		_ = os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(`{"enableAllProjectMcpServers": true}`), 0o600)
		_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"baum":{"command":"/bin/false"}}}`), 0o600)
		_, _ = turn(t, c, dir,
			"Call the baum current_page tool. Then create a file named edited.txt containing the word ok, using your file editing tool (not the shell). Reply with the page name only.",
			"", agent.AccessWorkspace, mcp)
		if !server.called("current_page") {
			t.Errorf("baum current_page not called (conflicting server won?); saw %v", server.seen())
		}
		if !exists(dir, "edited.txt") {
			t.Errorf("workspace: edit did not create edited.txt")
		}
	})
}

func TestLiveConverseClaude(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CLAUDE") != "" {
		t.Skip("BAUM_LIVE_SKIP_CLAUDE is set")
	}
	conversePair(t, &claude.Backend{Model: os.Getenv("BAUM_LIVE_CLAUDE_MODEL")})
}

func TestLiveConverseCodex(t *testing.T) {
	if os.Getenv("BAUM_LIVE_SKIP_CODEX") != "" {
		t.Skip("BAUM_LIVE_SKIP_CODEX is set")
	}
	conversePair(t, &codex.Backend{Model: os.Getenv("BAUM_LIVE_CODEX_MODEL")})
}
