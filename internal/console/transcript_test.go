//go:build unix

package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/transcript"
)

// A conversation's transcript is served by the session its agent reported,
// from the agent's own session store, whether or not the live sessions'
// tracker lists it: nothing the request carries names the file, and a
// conversation without a session, or whose transcript is gone, says so.
func TestConversationTranscript(t *testing.T) {
	home := t.TempDir()
	// Its clock moves past the recheck interval when the file appears.
	clock := time.Now()
	transcripts := &livesessions.Transcripts{Home: home, Render: transcript.NewRenderer("").Render, Now: func() time.Time { return clock }}
	var asked []string
	r := newRoutes(t)
	r.service.opts.Transcript = func(w http.ResponseWriter, req *http.Request, agent, id string) {
		asked = append(asked, agent+":"+id)
		answer, err := transcripts.Read(req.Context(), agent, id, "", "")
		transcripts.WriteAnswer(w, req, answer, err)
	}
	created := r.call(t, "POST", "/api/console/conversations", map[string]any{}, http.StatusCreated)
	id, _ := created["id"].(string)
	path := "/api/console/conversations/" + id + "/transcript"

	// No session yet: nothing to read, and the reader is not asked.
	if answer := r.call(t, "GET", path, nil, http.StatusOK); answer["available"] != false || !strings.Contains(answer["message"].(string), "not started") {
		t.Fatalf("before its session: %v", answer)
	}
	if len(asked) != 0 {
		t.Fatalf("read %v before the session was known", asked)
	}

	r.call(t, "POST", "/api/console/conversations/"+id+"/messages", map[string]any{"text": "hello"}, http.StatusAccepted)
	r.finished(id)
	// The session's file is not in the store: said so, not served.
	if answer := r.call(t, "GET", path, nil, http.StatusOK); answer["available"] != false || answer["code"] != "transcript_unavailable" {
		t.Fatalf("without its file: %v", answer)
	}
	// Once it is, it is served, with no tracker at all; a path in the
	// request is not the file read.
	file := filepath.Join(home, ".claude", "projects", "-work", testSession+".jsonl")
	lines := []map[string]any{
		{"type": "user", "uuid": "u1", "sessionId": testSession, "timestamp": "2026-11-12T10:00:00Z", "message": map[string]any{"role": "user", "content": "hello"}},
		{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "sessionId": testSession, "timestamp": "2026-11-12T10:00:05Z",
			"message": map[string]any{"id": "m1", "role": "assistant", "model": "claude-opus-5", "content": []any{map[string]any{"type": "text", "text": "Hello"}}}},
	}
	var body strings.Builder
	for _, line := range lines {
		raw, _ := json.Marshal(line)
		body.Write(raw)
		body.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * livesessions.RecheckEvery)
	answer := r.call(t, "GET", path+"?path=/etc/passwd", nil, http.StatusOK)
	version, _ := answer["version"].(string)
	if answer["available"] != true || version == "" || answer["transcript"] == nil {
		t.Fatalf("served %v", answer)
	}
	// A read naming the version it has gets no transcript again.
	if again := r.call(t, "GET", path+"?version="+version, nil, http.StatusOK); again["transcript"] != nil || again["version"] != version {
		t.Fatalf("unchanged: %v", again)
	}
	for _, a := range asked {
		if a != AgentClaudeCode+":"+testSession {
			t.Fatalf("read another session: %v", asked)
		}
	}

	// An unknown conversation is not found.
	request := httptest.NewRequest("GET", "/api/console/conversations/0123456789abcdef0123456789abcdef/transcript", nil)
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "conversation_not_found") {
		t.Fatalf("unknown conversation: %d %s", response.Code, response.Body.String())
	}
}
