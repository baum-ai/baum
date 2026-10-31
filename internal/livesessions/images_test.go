package livesessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

// pixel is a 1×1 PNG.
var pixel, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")

// imageAnswer is the images a transcript answer lists, by turn.
func imageAnswer(t *testing.T, h http.Handler, path string) map[string][]string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var body struct {
		Images map[string][]string `json:"images"`
	}
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Images
}

// servedImage asks for each image the answer lists and checks it is the
// pixel, as a PNG.
func servedImage(t *testing.T, h http.Handler, base string, images map[string][]string) {
	t.Helper()
	if len(images) != 1 {
		t.Fatalf("images %v", images)
	}
	for turn, types := range images {
		if len(types) != 1 || types[0] != "image/png" {
			t.Fatalf("types %v", types)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", base+"/images/"+turn+"/0", nil))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(pixel) {
			t.Fatalf("image: %d %q", w.Code, w.Header().Get("Content-Type"))
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", base+"/images/"+turn+"/1", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("a second image: %d", w.Code)
		}
	}
}

func TestClaudeCodePastedImagesAreServed(t *testing.T) {
	f := newTranscriptFixture(t)
	data := base64.StdEncoding.EncodeToString(pixel)
	appendLines(t, f.path,
		fmt.Sprintf(`{"type":"user","uuid":"u-img","sessionId":"s1","timestamp":"2026-10-27T18:05:00.000Z","promptId":"p-img","message":{"role":"user","content":[{"type":"text","text":"Here [Image #1]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}}`, data),
		// Where Claude Code saved it: no prompt of the person's.
		`{"type":"user","uuid":"u-meta","isMeta":true,"sessionId":"s1","timestamp":"2026-10-27T18:05:00.001Z","promptId":"p-img","message":{"role":"user","content":[{"type":"text","text":"[Image: source: /tmp/1.png]"}]}}`)
	images := imageAnswer(t, f.live.Handler(), "/api/live-sessions/claude-code/s1/transcript")
	servedImage(t, f.live.Handler(), "/api/live-sessions/claude-code/s1/transcript", images)
	answer, err := f.live.Get(context.Background(), "claude-code", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if body := string(answer.Body); strings.Contains(body, "[Image: source:") {
		t.Fatalf("the saved image's line is a prompt: %s", body)
	}
}

func TestCodexPastedImagesAreServed(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000002"
	const turn = "019f0000-0000-7000-8000-0000000000aa"
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixel)
	// As Codex records a prompt with a pasted image: the model's input holds
	// the image, the prompt's own record only where it was saved.
	write(t, filepath.Join(f.home, ".codex", "sessions", "2026", "10", "30", "rollout-2026-10-30T15-42-13-"+id+".jsonl"),
		`{"timestamp":"2026-10-30T15:42:21.557Z","ordinal":0,"type":"session_meta","payload":{"session_id":"`+id+`","id":"`+id+`","cwd":"/work/demo","cli_version":"0.166.0","model_provider":"openai","history_mode":"paginated"}}
{"timestamp":"2026-10-30T15:42:37.900Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"`+turn+`","root_turn_id":"`+turn+`","started_at":1793374957}}
{"timestamp":"2026-10-30T15:42:37.966Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/codex-clipboard.png\">"},{"type":"input_image","image_url":"`+url+`","detail":"high"},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"Here [Image #1]"}],"internal_chat_message_metadata_passthrough":{"turn_id":"`+turn+`"}}}
{"timestamp":"2026-10-30T15:42:37.970Z","ordinal":3,"type":"event_msg","payload":{"type":"item_completed","thread_id":"`+id+`","turn_id":"`+turn+`","item":{"type":"UserMessage","id":"item-1","content":[{"type":"local_image","path":"/tmp/codex-clipboard.png"},{"type":"text","text":"Here [Image #1]"}]}}}
{"timestamp":"2026-10-30T15:42:41.000Z","ordinal":4,"type":"event_msg","payload":{"type":"task_complete","turn_id":"`+turn+`","root_turn_id":"`+turn+`","last_agent_message":"I see it.","started_at":1793374957,"completed_at":1793374961}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	base := "/api/live-sessions/codex/" + id + "/transcript"
	servedImage(t, f.live.Handler(), base, imageAnswer(t, f.live.Handler(), base))
}
