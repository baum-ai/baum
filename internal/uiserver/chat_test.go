package uiserver

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/chat/chattest"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/store"
)

type chatAPI struct {
	t       *testing.T
	fixture *chattest.Fixture
	queue   *jobs.Queue
	handler *LocalHandler
}

func newChatAPI(t *testing.T, o chattest.Options) chatAPI {
	t.Helper()
	f := chattest.New(t, o)
	state := appdbtest.TempDir(t)
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.Open(filepath.Join(state, "jobs.json"), &jobs.Local{Registry: f.Registry, Chat: f.Service}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	queue.SetChat(f.Service)
	h := LocalAPI{Registry: f.Registry, Chat: ChatHandler(f.Registry, queue)}.Handler()
	t.Cleanup(h.Close)
	return chatAPI{t, f, queue, h}
}

func (a chatAPI) do(method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
	return w
}

func (a chatAPI) post(body string) *httptest.ResponseRecorder {
	return a.do("POST", "/api/prs/chat", body)
}

func (a chatAPI) list() []store.Conversation {
	a.t.Helper()
	w := a.do("GET", fmt.Sprintf("/api/prs/chat?clone=%s&pr=7", a.fixture.Clone.ID), "")
	var body struct {
		Conversations []store.Conversation `json:"conversations"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		a.t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	return body.Conversations
}

func (a chatAPI) waitSettled(conversation string, turn int) store.Turn {
	a.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range a.list() {
			if c.ID == conversation && len(c.Turns) >= turn && !store.Pending(c.Turns[turn-1].State) {
				return c.Turns[turn-1]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatal("the turn did not settle")
	return store.Turn{}
}

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Code, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Message == "" {
		t.Fatalf("not a coded error: %d %s", w.Code, w.Body)
	}
	return body.Code
}

func TestChatAPIAsksAndLists(t *testing.T) {
	a := newChatAPI(t, chattest.Options{})
	secret := "AKIA" + "UJZDEGXDNCF32EPF"
	release := make(chan struct{})
	a.fixture.Agent.Reply = func(r agent.Request, n int) (agent.Result, error) {
		<-release
		result := a.fixture.Agent.Default(r)
		result.Document = chattest.Answer("key "+secret, chattestCitation())
		return result, nil
	}
	clone := a.fixture.Clone.ID
	w := a.post(fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":"Why?","focus":{"kind":"section","id":"s1","title":"Limits","anchors":[{"path":"src/app.go","start_line":1}]}}`, clone))
	if w.Code != 202 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	var queued struct {
		Conversation string   `json:"conversation_id"`
		Turn         int      `json:"turn"`
		Job          jobs.Job `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || queued.Conversation == "" || queued.Turn != 1 || queued.Job.Kind != jobs.KindChat || queued.Job.PR != 7 {
		t.Fatalf("queued: %s (%v)", w.Body, err)
	}
	// A second question waits for the first.
	busy := a.post(fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"conversation_id":%q,"question":"And?"}`, clone, queued.Conversation))
	if busy.Code != 409 || code(t, busy) != "busy" {
		t.Errorf("busy: %d %s", busy.Code, busy.Body)
	}
	close(release)
	turn := a.waitSettled(queued.Conversation, 1)
	if turn.State != store.TurnCompleted || turn.JobID != queued.Job.ID || len(turn.Focus) == 0 {
		t.Fatalf("turn: %+v", turn)
	}
	list := a.do("GET", fmt.Sprintf("/api/prs/chat?clone=%s&pr=7", clone), "")
	if strings.Contains(list.Body.String(), secret) || !strings.Contains(list.Body.String(), `"answer_markdown":"key `) {
		t.Errorf("listed answer: %s", list.Body)
	}
	for _, private := range []string{"session", "directory", a.fixture.ReviewDir} {
		if strings.Contains(list.Body.String(), private) {
			t.Errorf("the listing shows %q: %s", private, list.Body)
		}
	}
	var body map[string][]map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	conversation := body["conversations"][0]
	for _, key := range []string{"id", "version", "backend", "model", "seeded", "created", "updated", "turns"} {
		if _, ok := conversation[key]; !ok {
			t.Errorf("conversation lacks %s: %v", key, conversation)
		}
	}
	next := a.post(fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"conversation_id":%q,"question":"And?"}`, clone, queued.Conversation))
	if next.Code != 202 || !strings.Contains(next.Body.String(), `"turn":2`) {
		t.Errorf("next: %d %s", next.Code, next.Body)
	}
	a.waitSettled(queued.Conversation, 2)
}

func chattestCitation() chat.Citation {
	return chat.Citation{Path: "README.md", StartLine: 1, EndLine: 1}
}

func TestChatAPIRefusals(t *testing.T) {
	a := newChatAPI(t, chattest.Options{})
	clone := a.fixture.Clone.ID
	ask := func(extra string) string {
		return fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":"q"%s}`, clone, extra)
	}
	for name, c := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"not json":             {`nope`, 400, "invalid_request"},
		"unknown field":        {ask(`,"extra":1`), 400, "invalid_request"},
		"unknown focus field":  {ask(`,"focus":{"kind":"stop","extra":1}`), 400, "invalid_request"},
		"bad focus kind":       {ask(`,"focus":{"kind":"file"}`), 400, "invalid_request"},
		"missing version":      {fmt.Sprintf(`{"clone":%q,"pr":7,"question":"q"}`, clone), 400, "invalid_request"},
		"empty question":       {fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":""}`, clone), 400, "invalid_request"},
		"long question":        {fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":%q}`, clone, strings.Repeat("x", 4001)), 400, "invalid_request"},
		"unregistered":         {`{"clone":"nope","pr":7,"version":1,"question":"q"}`, 404, "not_found"},
		"no review":            {fmt.Sprintf(`{"clone":%q,"pr":7,"version":9,"question":"q"}`, clone), 404, "no_review"},
		"unknown conversation": {ask(`,"conversation_id":"` + strings.Repeat("0", 32) + `"`), 404, "not_found"},
	} {
		w := a.post(c.body)
		if w.Code != c.status || code(t, w) != c.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for name, path := range map[string]string{"no pr": "/api/prs/chat?clone=" + clone, "unregistered": "/api/prs/chat?clone=nope&pr=7"} {
		if w := a.do("GET", path, ""); w.Code/100 != 4 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if len(a.queue.List()) != 0 {
		t.Errorf("refused questions queued jobs: %+v", a.queue.List())
	}

	cursor := newChatAPI(t, chattest.Options{Backend: agent.BackendCursorAgent, NoSession: true})
	if w := cursor.post(fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":"q"}`, cursor.fixture.Clone.ID)); w.Code != 422 || code(t, w) != "unsupported_backend" {
		t.Errorf("cursor: %d %s", w.Code, w.Body)
	}

	moved := newChatAPI(t, chattest.Options{})
	if err := os.Rename(moved.fixture.Clone.Path, moved.fixture.Clone.Path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if w := moved.post(fmt.Sprintf(`{"clone":%q,"pr":7,"version":1,"question":"q"}`, moved.fixture.Clone.ID)); w.Code != 409 || code(t, w) != "needs_clone" {
		t.Errorf("needs clone: %d %s", w.Code, w.Body)
	}
}

func TestChatAPIUnavailable(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	h := LocalAPI{Registry: f.Registry}.Handler()
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/prs/chat?clone=x&pr=7", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"chat_unavailable"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://local/api/prs/chat", strings.NewReader(`{}`)))
	if w.Code != 503 {
		t.Errorf("post: %d", w.Code)
	}
}

func TestChatEventsArePublished(t *testing.T) {
	s := testServer(t)
	if err := s.Publish("chat", map[string]any{"conversation": "c", "repository": "acme/demo", "pr": 7, "turn": 1, "state": "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish("chatter", struct{}{}); err == nil {
		t.Error("an unknown kind was published")
	}
}
