package livesessions

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// titles is the list's title of each session, by agent and id.
func titles(t *testing.T, f *transcriptFixture) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	var body struct {
		Sessions []struct {
			Agent string `json:"agent"`
			ID    string `json:"session_id"`
			Title string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", w.Body.String(), err)
	}
	out := map[string]string{}
	for _, s := range body.Sessions {
		out[s.Agent+"/"+s.ID] = s.Title
	}
	return out
}

// appendLines adds lines to a transcript and moves its modification time on,
// as a file system with a coarse clock might not.
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	_ = file.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func aiTitle(title string) string {
	return fmt.Sprintf(`{"type":"ai-title","aiTitle":%q,"sessionId":"s1"}`, title)
}

func TestClaudeCodeTitleIsTheOneTheUserSetElseTheGeneratedOneElseTheFirstPrompt(t *testing.T) {
	f := newTranscriptFixture(t)
	f.tracker.Mask = func(s string) string { return strings.ReplaceAll(s, plantedDSN, "[masked]") }
	// Only the first prompt so far, masked as transcripts are.
	if got := titles(t, f)["claude-code/s1"]; got != "Connect to [masked] and mail "+plantedMail {
		t.Fatalf("title %q", got)
	}
	appendLines(t, f.path, aiTitle("Connect the app database"))
	// Not looked at again within TitleRecheckEvery.
	if got := titles(t, f)["claude-code/s1"]; !strings.HasPrefix(got, "Connect to") {
		t.Fatalf("title %q read again too soon", got)
	}
	f.clock.advance(TitleRecheckEvery)
	if got := titles(t, f)["claude-code/s1"]; got != "Connect the app database" {
		t.Fatalf("title %q", got)
	}
	appendLines(t, f.path,
		`{"type":"custom-title","customTitle":"DB work","sessionId":"s1"}`,
		aiTitle("A later generated title"))
	f.clock.advance(TitleRecheckEvery)
	if got := titles(t, f)["claude-code/s1"]; got != "DB work" {
		t.Fatalf("the title the user set lost: %q", got)
	}
}

func TestClaudeCodeTitleIsFoundBeforeALongTail(t *testing.T) {
	f := newTranscriptFixture(t)
	appendLines(t, f.path, aiTitle("Early title"))
	// Turns enough to push the title out of the tail read first.
	filler := strings.Repeat("x", 4096)
	for n := 2; n < 2+titleTail/4096+8; n++ {
		appendLines(t, f.path, strings.TrimSuffix(claudeTurn("s1", n, "more", filler), "\n"))
	}
	if got := titles(t, f)["claude-code/s1"]; got != "Early title" {
		t.Fatalf("title %q", got)
	}
}

func TestClaudeCodeFirstPromptSkipsWhatClaudeCodeWrote(t *testing.T) {
	f := newTranscriptFixture(t)
	write(t, f.path, strings.Join([]string{
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Caveat: the messages below were generated"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Fix the flaky\nretry test"}]}}`,
	}, "\n")+"\n")
	if got := titles(t, f)["claude-code/s1"]; got != "Fix the flaky retry test" {
		t.Fatalf("title %q", got)
	}
}

func TestEndedClaudeCodeSessionWithoutAPathIsTitledByItsID(t *testing.T) {
	f := newTranscriptFixture(t)
	appendLines(t, f.path, aiTitle("Found by id"))
	// A session read back from StateFile keeps no transcript path.
	f.tracker.mu.Lock()
	f.tracker.sessions[key{AgentClaudeCode, "s1"}].transcript = ""
	f.tracker.mu.Unlock()
	if got := titles(t, f)["claude-code/s1"]; got != "Found by id" {
		t.Fatalf("title %q", got)
	}
}

func TestCodexTitleIsTheThreadNameElseTheFirstPrompt(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000002"
	write(t, filepath.Join(f.home, ".codex", "sessions", "2026", "10", "20", "rollout-2026-10-20T09-00-00-"+id+".jsonl"),
		`{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo","cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-10-20T09:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"Rename the flag"}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	if got := titles(t, f)["codex/"+id]; got != "Rename the flag" {
		t.Fatalf("title %q", got)
	}
	index := filepath.Join(f.home, ".codex", "session_index.jsonl")
	write(t, index, `{"id":"`+id+`","thread_name":"Rename flag","updated_at":"2026-10-20T09:00:06Z"}
{"id":"another","thread_name":"Something else","updated_at":"2026-10-20T09:00:07Z"}
`)
	appendLines(t, index, `{"id":"`+id+`","thread_name":"Rename the CLI flag","updated_at":"2026-10-20T09:01:00Z"}`)
	f.clock.advance(TitleRecheckEvery)
	if got := titles(t, f)["codex/"+id]; got != "Rename the CLI flag" {
		t.Fatalf("title %q", got)
	}
}
