package agentmatrix

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/internal/daemon"
)

type step struct {
	event   string // baum's event name, as in the plugin's command
	payload string // what the agent sends on standard input
	want    string // the live session's last_event afterwards
}

// replaySteps are representative hook payloads, in the shape each agent
// documents, for one session's life.
func replaySteps(agent, id, transcript, cwd string) []step {
	p := func(event string, extra string) string {
		return fmt.Sprintf(`{"session_id":%q,"transcript_path":%q,"cwd":%q,"hook_event_name":%q%s}`, id, transcript, cwd, event, extra)
	}
	if agent == "claude-code" {
		return []step{
			{"session-start", p("SessionStart", `,"source":"startup","model":"claude-sonnet"`), "start"},
			{"user-prompt-submit", p("UserPromptSubmit", `,"prompt":"Audit the parser"`), "prompt"},
			{"notification", p("Notification", `,"notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"`), "wait"},
			{"post-tool-use", p("PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"x"}`), "tool"},
			{"stop", p("Stop", `,"stop_hook_active":false`), "stop"},
			{"session-end", p("SessionEnd", `,"reason":"prompt_input_exit"`), "end"},
		}
	}
	return []step{
		{"session-start", p("SessionStart", `,"source":"startup","model":"gpt-5"`), "start"},
		{"user-prompt-submit", p("UserPromptSubmit", `,"turn_id":"t1","prompt":"Add an index"`), "prompt"},
		{"permission-request", p("PermissionRequest", `,"turn_id":"t1","tool_name":"Bash","tool_input":{"command":"rm -rf build"}`), "wait"},
		{"post-tool-use", p("PostToolUse", `,"turn_id":"t1","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":"x","tool_use_id":"c1"`), "tool"},
		{"stop", p("Stop", `,"turn_id":"t1","last_assistant_message":"Done"`), "stop"},
		{"session-end", p("SessionEnd", `,"reason":"exit"`), "end"},
	}
}

// checkReplay starts a baum daemon on an isolated home, sends it a session's
// hook events through the very commands the plugin installs, and checks the
// live session follows them and serves its transcript.
func (m *matrix) checkReplay() {
	h := m.newHome()
	h.setenv(m.t)
	var id, fixture, store string
	switch m.agent {
	case "claude-code":
		id = "11111111-2222-3333-4444-555555555555"
		fixture = filepath.Join(m.repo, "sessions", "claudetranscript", "testdata", id+".jsonl")
		store = filepath.Join(h.dir, ".claude", "projects", "-work-demo", id+".jsonl")
	default:
		id = "019e9999-1111-7000-8000-aaaabbbbcccc"
		fixture = filepath.Join(m.repo, "sessions", "codexsession", "testdata", "lifecycle", "basic_rollout.jsonl")
		store = filepath.Join(h.dir, ".codex", "sessions", "2026", "06", "20", "rollout-2026-06-20T08-00-00-"+id+".jsonl")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		m.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(store, data, 0o600); err != nil {
		m.t.Fatal(err)
	}
	cwd := filepath.Join(h.dir, "work", "demo")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		m.t.Fatal(err)
	}
	if out, _, err := h.run(cwd, time.Minute, "", "git", "init", "-q"); err != nil {
		m.t.Fatalf("git init: %v %s", err, out)
	}
	if out, _, err := h.run(cwd, time.Minute, "", "git", "remote", "add", "origin", "https://github.com/acme/demo"); err != nil {
		m.t.Fatalf("git remote: %v %s", err, out)
	}
	if out, _, err := h.run(h.dir, time.Minute, "", m.baum, "internal", "repos", "add", cwd); err != nil {
		m.record("replay", "fail", "registering the clone: %v: %s", err, oneLine(out))
		return
	}
	m.t.Cleanup(func() { _, _, _ = h.run(h.dir, time.Minute, "", m.baum, "daemon", "stop") })
	if out, _, err := h.run(h.dir, time.Minute, "", m.baum, "daemon", "ensure", "--json"); err != nil {
		m.record("replay", "fail", "daemon did not start: %v: %s", err, oneLine(out))
		return
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		m.t.Fatal(err)
	}
	cred, err := daemon.ReadCredential(paths)
	if err != nil {
		m.record("replay", "fail", "no daemon credential: %v", err)
		return
	}
	get := func(path string) ([]byte, int) {
		req, _ := http.NewRequest("GET", cred.Origin()+path, nil)
		req.Header.Set("Authorization", "Bearer "+cred.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return []byte(err.Error()), 0
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return body, resp.StatusCode
	}
	last := func() (string, string) {
		body, code := get("/api/live-sessions")
		var list struct {
			Sessions []struct {
				ID        string `json:"session_id"`
				Agent     string
				LastEvent string `json:"last_event"`
				State     string
			}
		}
		if code != 200 || json.Unmarshal(body, &list) != nil {
			return "", fmt.Sprintf("GET /api/live-sessions: %d %s", code, oneLine(string(body)))
		}
		for _, s := range list.Sessions {
			if s.ID == id {
				return s.LastEvent, s.Agent
			}
		}
		return "", "session not listed: " + oneLine(string(body))
	}
	spec, _ := agenthooks.Lookup(m.agent)
	var trace []string
	for _, st := range replaySteps(m.agent, id, store, cwd) {
		if st.event == "session-end" {
			// Let the transcript check below see the session while it lives.
			body, code := get(fmt.Sprintf("/api/live-sessions/%s/%s/transcript", m.agent, id))
			var tr struct {
				Available bool
				Code      string
			}
			if code != 200 || json.Unmarshal(body, &tr) != nil || !tr.Available {
				m.record("replay", "fail", "transcript not served: %d %s", code, oneLine(string(body)))
				return
			}
			trace = append(trace, fmt.Sprintf("transcript %d bytes", len(body)))
		}
		out, code, err := h.run(cwd, 30*time.Second, st.payload, "sh", "-c", agenthooks.UserDispatcher(spec, st.event))
		if err != nil || code != 0 {
			m.record("replay", "fail", "%s hook: exit %d: %v: %s", st.event, code, err, oneLine(out))
			return
		}
		var got, who string
		for range 50 {
			if got, who = last(); got == st.want {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if got != st.want {
			m.record("replay", "fail", "after %s the session is %q (%s), want %q", st.event, got, who, st.want)
			return
		}
		trace = append(trace, st.event+"->"+got)
	}
	m.record("replay", "pass", "%s", strings.Join(trace, ", "))
}
