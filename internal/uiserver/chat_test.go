package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type chatAPI struct {
	t       *testing.T
	clone   repos.Clone
	handler *LocalHandler
}

// newChatAPI serves the stored conversations of a registered clone of
// acme/demo whose pull request 7 has one answered conversation, kept with a
// session and directory that must never be shown.
func newChatAPI(t *testing.T) chatAPI {
	t.Helper()
	ctx := context.Background()
	repo := assembletest.NewRepo(t, assembletest.Options{})
	remote := exec.Command("git", "remote", "add", "origin", "https://github.com/acme/demo")
	remote.Dir = repo.Dir
	if out, err := remote.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	clone, err := registry.AddFor(ctx, repo.Dir, "acme/demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: clone.Repository, Number: assembletest.Number}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	c := store.Conversation{ID: "c1", Version: 1, Backend: "claude", Model: "m", SessionID: "secret-session", Directory: "/kept/secret-run", Created: at, Updated: at}
	if err := st.CreateConversation(ctx, key, c, store.Turn{Seq: 1, Question: "Why?", Focus: json.RawMessage(`{"kind":"section","id":"s1"}`), JobID: "job-1", State: store.TurnQueued, Created: at}); err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(chat.Answer{Markdown: "Because.", Citations: []chat.Citation{{Path: "README.md", StartLine: 1, EndLine: 1}}})
	if _, err := st.SettleTurn(ctx, key, "c1", 1, store.Settlement{State: store.TurnCompleted, Answer: answer, Finished: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	state := appdbtest.TempDir(t)
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.Open(filepath.Join(state, "jobs.json"), &jobs.Local{Registry: registry}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	queue.SetChat(&chat.Service{Registry: registry})
	h := LocalAPI{Registry: registry, Chat: ChatHandler(registry, queue)}.Handler()
	t.Cleanup(h.Close)
	return chatAPI{t, clone, h}
}

func (a chatAPI) do(method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader("{}")))
	return w
}

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Code, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Message == "" {
		t.Fatalf("not a coded error: %d %s", w.Code, w.Body)
	}
	return body.Code
}

func TestChatAPIListsTheStoredConversations(t *testing.T) {
	a := newChatAPI(t)
	list := a.do("GET", fmt.Sprintf("/api/prs/chat?clone=%s&pr=7", a.clone.ID))
	if list.Code != 200 {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	for _, private := range []string{"session", "directory", "secret-run"} {
		if strings.Contains(list.Body.String(), private) {
			t.Errorf("the listing shows %q: %s", private, list.Body)
		}
	}
	var body map[string][]map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil || len(body["conversations"]) != 1 {
		t.Fatalf("%s (%v)", list.Body, err)
	}
	conversation := body["conversations"][0]
	for _, key := range []string{"id", "version", "backend", "model", "seeded", "created", "updated", "turns"} {
		if _, ok := conversation[key]; !ok {
			t.Errorf("conversation lacks %s: %v", key, conversation)
		}
	}
	// The job of the turn is gone from the journal, but the turn was
	// settled with its answer and keeps it.
	if !strings.Contains(list.Body.String(), `"answer_markdown":"Because."`) || !strings.Contains(list.Body.String(), `"state":"completed"`) {
		t.Errorf("listed turn: %s", list.Body)
	}
}

func TestChatAPIRefusals(t *testing.T) {
	a := newChatAPI(t)
	for name, c := range map[string]struct {
		path   string
		status int
		code   string
	}{
		"no pr":        {"/api/prs/chat?clone=" + a.clone.ID, 400, "invalid_request"},
		"bad pr":       {"/api/prs/chat?clone=" + a.clone.ID + "&pr=x", 400, "invalid_request"},
		"unregistered": {"/api/prs/chat?clone=nope&pr=7", 404, "not_found"},
	} {
		if w := a.do("GET", c.path); w.Code != c.status || code(t, w) != c.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// Nothing asks new questions: no route takes a POST.
	if w := a.do("POST", "/api/prs/chat"); w.Code < 400 {
		t.Errorf("post: %d %s", w.Code, w.Body)
	}
}

func TestChatAPIUnavailable(t *testing.T) {
	h := LocalAPI{}.Handler()
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/prs/chat?clone=x&pr=7", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"chat_unavailable"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
}

func TestChatEventsAreNotPublished(t *testing.T) {
	s := testServer(t)
	if err := s.Publish("chat", struct{}{}); err == nil {
		t.Error("a chat event was published")
	}
}
