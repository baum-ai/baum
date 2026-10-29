//go:build !windows

package livesessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func postMessage(f *transcriptFixture, agent, id, body string) int {
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/live-sessions/"+agent+"/"+id+"/message", strings.NewReader(body)))
	return w.Code
}

func TestOnlyASessionAtItsPromptIsMessaged(t *testing.T) {
	f := newTranscriptFixture(t)
	var sent []string
	f.live.Senders = map[string]Sender{AgentClaudeCode: func(_ context.Context, id, text string) error {
		sent = append(sent, id+": "+text)
		return nil
	}}
	// Working: refused.
	if postMessage(f, "claude-code", "s1", `{"text":"Ship it"}`) != http.StatusConflict || len(sent) != 0 {
		t.Fatalf("a working session was messaged: %v", sent)
	}
	stop(f)
	if postMessage(f, "claude-code", "s1", `{"text":"  "}`) != http.StatusBadRequest {
		t.Fatal("an empty message was not refused")
	}
	if code := postMessage(f, "claude-code", "s1", `{"text":"Ship it"}`); code != http.StatusAccepted {
		t.Fatalf("answered %d", code)
	}
	if len(sent) != 1 || sent[0] != "s1: "+framed("Ship it") {
		t.Fatalf("sent %q", sent)
	}
	// Asking a permission: answered, not messaged.
	f.clock.advance(time.Second)
	permission := notification("s1", "permission_prompt", "Claude needs your permission to use Bash", f.clock.now)
	permission.Session.TranscriptPath = f.path
	apply(f.tracker, permission)
	if postMessage(f, "claude-code", "s1", `{"text":"Ship it"}`) != http.StatusConflict {
		t.Fatal("a session asking a permission was messaged")
	}
}

func TestMessagingASessionWhoseAgentIsGoneRecordsItEnded(t *testing.T) {
	f := newTranscriptFixture(t)
	f.live.Senders = map[string]Sender{AgentClaudeCode: func(context.Context, string, string) error { return ErrAgentGone }}
	stop(f)
	if code := postMessage(f, "claude-code", "s1", `{"text":"Ship it"}`); code != http.StatusGone {
		t.Fatalf("answered %d", code)
	}
	if s := served(t, f)["claude-code/s1"]; s.State != StateEnded {
		t.Fatalf("served %+v", s)
	}
}

func TestClaudeInboxWritesOneLineToTheSessionsSocket(t *testing.T) {
	home := t.TempDir()
	// A short path: a Unix socket's is bounded.
	dir, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	got := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
	}()
	const id = "9cb7a4e2-0000-4000-8000-000000000001"
	sessions := filepath.Join(home, ".claude", "sessions")
	// One left by a process that died, then the session's own.
	write(t, filepath.Join(sessions, "999999999.json"), fmt.Sprintf(`{"pid":999999999,"sessionId":%q,"status":"idle","messagingSocketPath":"/nowhere"}`, id))
	write(t, filepath.Join(sessions, fmt.Sprintf("%d.json", os.Getpid())),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle","messagingSocketPath":%q,"kind":"interactive"}`, os.Getpid(), id, socket))
	if err := (ClaudeInbox{Home: home}).Send(context.Background(), id, "Ship it"); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Type    string `json:"type"`
		From    string `json:"from"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(<-got), &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "user" || message.From != "baum" || message.Message.Role != "user" || message.Message.Content != "Ship it" {
		t.Fatalf("sent %+v", message)
	}
	if err := (ClaudeInbox{Home: home}).Send(context.Background(), "9cb7a4e2-0000-4000-8000-000000000002", "x"); !errors.Is(err, ErrAgentGone) {
		t.Fatalf("a session with no process: %v", err)
	}
}

func TestCodexQueueRunsCodexQueueForTheThread(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	codex := filepath.Join(dir, "codex")
	write(t, codex, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+args+"\n")
	if err := os.Chmod(codex, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "01a183c2-580a-7600-84b0-9de9c4f3f7ff"
	if err := (CodexQueue{Codex: codex}).Send(context.Background(), id, "Line one\n--flag-like"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if want := "queue\n--thread\n" + id + "\n--message\nLine one\n--flag-like\n"; string(data) != want {
		t.Fatalf("ran with %q", data)
	}
}

func TestAMessageTheAppSentIsTheLatestPrompt(t *testing.T) {
	f := newTranscriptFixture(t)
	content, _ := json.Marshal(`<cross-session-message from="baum">` + framed("Ship it") + `</cross-session-message>`)
	appendLines(t, f.path,
		`{"type":"user","isMeta":true,"origin":{"kind":"peer","from":"baum","verifiedPeerPid":1},"timestamp":"2026-10-27T18:05:00.000Z","sessionId":"s1","message":{"role":"user","content":`+string(content)+`}}`)
	if a := activities(t, f)["claude-code/s1"]; a == nil || a.LastPrompt != "Ship it" {
		t.Fatalf("activity %+v", a)
	}
}
