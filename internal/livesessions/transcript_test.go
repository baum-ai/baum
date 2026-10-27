package livesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions/session"
)

const (
	plantedMail = "ada.lovelace@example.com"
	plantedDSN  = "postgres://app:Tr0ub4dor-3xkq@db.internal:5432/app"
)

// claudeTurn is one prompt of a Claude Code transcript and the agent's
// reply, as the lines Claude Code appends.
func claudeTurn(session string, n int, prompt, reply string) string {
	at := t0.Add(time.Duration(n) * time.Minute)
	user := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":"/work/demo","gitBranch":"feature","type":"user","uuid":"u-%d","sessionId":%q,"timestamp":%q,"promptId":"p-%d","message":{"role":"user","content":%q}}`,
		n, session, at.Format(time.RFC3339Nano), n, prompt)
	assistant := fmt.Sprintf(`{"parentUuid":"u-%d","isSidechain":false,"cwd":"/work/demo","gitBranch":"feature","type":"assistant","uuid":"a-%d","sessionId":%q,"timestamp":%q,"requestId":"req-%d","message":{"id":"msg-%d","role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":%q}]}}`,
		n, n, session, at.Add(time.Second).Format(time.RFC3339Nano), n, n, reply)
	return user + "\n" + assistant + "\n"
}

type transcriptFixture struct {
	home, path string
	clock      *clock
	tracker    *Tracker
	renders    atomic.Int64
	live       *Transcripts
}

// newTranscriptFixture tracks the Claude Code session s1, whose hooks name
// its transcript in the store under a temporary home, which holds one turn.
func newTranscriptFixture(t *testing.T) *transcriptFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &transcriptFixture{home: home, clock: &clock{t0}}
	f.path = filepath.Join(home, ".claude", "projects", "-work-demo", "s1.jsonl")
	write(t, f.path, claudeTurn("s1", 1, "Connect to "+plantedDSN+" and mail "+plantedMail, "Connected."))
	f.tracker = newTracker(f.clock, newLinker())
	f.track(t, "s1", f.path)
	renderer := transcript.NewRenderer(home)
	f.live = &Transcripts{Tracker: f.tracker, Home: home, Now: f.clock.Now,
		Render: func(ctx context.Context, graph *session.Graph, meta transcript.Meta) ([]byte, error) {
			f.renders.Add(1)
			return renderer.Render(ctx, graph, meta)
		}}
	return f
}

// track records a prompt of the Claude Code session id whose hooks name path.
func (f *transcriptFixture) track(t *testing.T, id, path string) {
	t.Helper()
	e := event(id, "user-prompt-submit", f.clock.now, "/work/demo")
	e.Session.TranscriptPath = path
	if !apply(f.tracker, e) {
		t.Fatalf("session %s not tracked", id)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// appendTurn adds a turn to the transcript and moves its modification time
// on, as a file system with a coarse clock might not.
func appendTurn(t *testing.T, path string, n int, prompt string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(claudeTurn("s1", n, prompt, "Done.")); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	later := time.Now().Add(time.Duration(n) * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

type answerBody struct {
	Available  bool   `json:"available"`
	Version    string `json:"version"`
	Live       bool   `json:"live"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Transcript *struct {
		Session struct {
			Prompts []struct {
				Text string `json:"text"`
			} `json:"prompts"`
		} `json:"session"`
	} `json:"transcript"`
}

func get(t *testing.T, h http.Handler, path string) (int, answerBody, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var body answerBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", w.Body.String(), err)
		}
	}
	return w.Code, body, w.Body.String()
}

func TestTranscriptServesATrackedSessionRedacted(t *testing.T) {
	f := newTranscriptFixture(t)
	h := f.live.Handler()
	code, body, raw := get(t, h, "/api/live-sessions/claude-code/s1/transcript")
	if code != http.StatusOK || !body.Available || !body.Live || body.Version == "" || body.Transcript == nil {
		t.Fatalf("%d %s", code, raw)
	}
	prompts := body.Transcript.Session.Prompts
	if len(prompts) != 1 || !strings.HasPrefix(prompts[0].Text, "Connect to ") {
		t.Fatalf("prompts = %+v", prompts)
	}
	for _, secret := range []string{"Tr0ub4dor-3xkq", plantedMail, f.home} {
		if strings.Contains(raw, secret) {
			t.Errorf("the answer carries %q: %s", secret, raw)
		}
	}
	// The list is still served beside it.
	if code, _, raw := get(t, h, "/api/live-sessions"); code != http.StatusOK || !strings.Contains(raw, `"session_id":"s1"`) {
		t.Fatalf("list: %d %s", code, raw)
	}
	// A window that has this version is told so, without the transcript.
	code, again, raw := get(t, h, "/api/live-sessions/claude-code/s1/transcript?version="+body.Version)
	if code != http.StatusOK || again.Version != body.Version || again.Transcript != nil || strings.Contains(raw, "transcript") {
		t.Fatalf("same version: %d %s", code, raw)
	}
}

func TestTranscriptIsReadOnlyFromTheAgentsOwnStore(t *testing.T) {
	f := newTranscriptFixture(t)
	outside := filepath.Join(f.home, "elsewhere", "s2.jsonl")
	write(t, outside, claudeTurn("s2", 1, "private", "reply"))
	link := filepath.Join(f.home, ".claude", "projects", "-work-demo", "s3.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	directory := filepath.Join(f.home, ".claude", "projects", "-work-demo", "s4.jsonl")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(f.home, ".claude", "projects", "-work-demo", "notes.txt")
	write(t, notes, "not a transcript")
	for id, path := range map[string]string{
		"outside":   outside,
		"symlink":   link,
		"directory": directory,
		"named":     notes,
		"relative":  ".claude/projects/-work-demo/s1.jsonl",
		"escape":    filepath.Join(f.home, ".claude", "projects", "..", "..", "elsewhere", "s2.jsonl"),
		"missing":   filepath.Join(f.home, ".claude", "projects", "-work-demo", "gone.jsonl"),
		"no-path":   "",
	} {
		f.track(t, id, path)
		_, err := f.live.Get(context.Background(), "claude-code", id)
		var missing *Unavailable
		if !errors.As(err, &missing) || missing.Reason == "" {
			t.Errorf("%s: err = %v", id, err)
		}
		code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/claude-code/"+id+"/transcript")
		if code != http.StatusOK || body.Available || body.Code != "transcript_unavailable" || strings.Contains(raw, "private") || strings.Contains(raw, f.home) {
			t.Errorf("%s: %d %s", id, code, raw)
		}
	}
	if f.renders.Load() != 0 {
		t.Fatalf("%d transcripts were rendered", f.renders.Load())
	}
	// A session the tracker does not list is not found, whatever the disk has.
	if _, err := f.live.Get(context.Background(), "claude-code", "s1.jsonl"); !errors.Is(err, ErrNotTracked) {
		t.Fatalf("untracked: %v", err)
	}
	if code, _, _ := get(t, f.live.Handler(), "/api/live-sessions/codex/s1/transcript"); code != http.StatusNotFound {
		t.Fatalf("another agent's session: %d", code)
	}
}

func TestTranscriptIsRebuiltOnlyWhenItsFileChanges(t *testing.T) {
	f := newTranscriptFixture(t)
	ctx := context.Background()
	first, err := f.live.Get(ctx, "claude-code", "s1")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.advance(RecheckEvery)
	same, err := f.live.Get(ctx, "claude-code", "s1")
	if err != nil || same.Version != first.Version || f.renders.Load() != 1 {
		t.Fatalf("unchanged file: version %s → %s, %d renders, %v", first.Version, same.Version, f.renders.Load(), err)
	}
	appendTurn(t, f.path, 2, "Now add the tests")
	f.clock.advance(RecheckEvery)
	grown, err := f.live.Get(ctx, "claude-code", "s1")
	if err != nil || grown.Version == first.Version || f.renders.Load() != 2 || !strings.Contains(string(grown.Body), "Now add the tests") {
		t.Fatalf("grown file: version %s → %s, %d renders, %v", first.Version, grown.Version, f.renders.Load(), err)
	}
}

func TestTranscriptIsLookedAtAtMostOncePerInterval(t *testing.T) {
	f := newTranscriptFixture(t)
	ctx := context.Background()
	// Requests arriving together share one build.
	var wg sync.WaitGroup
	versions := make([]string, 8)
	for i := range versions {
		wg.Go(func() {
			answer, err := f.live.Get(ctx, "claude-code", "s1")
			if err != nil {
				t.Error(err)
			}
			versions[i] = answer.Version
		})
	}
	wg.Wait()
	if f.renders.Load() != 1 {
		t.Fatalf("%d renders for requests arriving together", f.renders.Load())
	}
	for _, v := range versions {
		if v != versions[0] {
			t.Fatalf("versions %v", versions)
		}
	}
	// A change within the interval is not looked at until it has passed.
	appendTurn(t, f.path, 2, "Now add the tests")
	f.clock.advance(RecheckEvery / 2)
	if answer, _ := f.live.Get(ctx, "claude-code", "s1"); answer.Version != versions[0] || f.renders.Load() != 1 {
		t.Fatalf("looked at again within the interval: %d renders", f.renders.Load())
	}
	f.clock.advance(RecheckEvery / 2)
	if answer, _ := f.live.Get(ctx, "claude-code", "s1"); answer.Version == versions[0] || f.renders.Load() != 2 {
		t.Fatalf("not looked at after the interval: %d renders", f.renders.Load())
	}
}

func TestTranscriptOfAnEndedSessionIsNotLive(t *testing.T) {
	f := newTranscriptFixture(t)
	f.clock.advance(time.Minute)
	end := event("s1", "session-end", f.clock.now, "/work/demo")
	end.Session.TranscriptPath = f.path
	if !apply(f.tracker, end) {
		t.Fatal("the end was not applied")
	}
	code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/claude-code/s1/transcript")
	if code != http.StatusOK || !body.Available || body.Live || body.Transcript == nil {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestTranscriptCacheHoldsOnlyListedSessions(t *testing.T) {
	f := newTranscriptFixture(t)
	ctx := context.Background()
	for i := range MaxCached + 2 {
		id := fmt.Sprintf("extra-%d", i)
		path := filepath.Join(f.home, ".claude", "projects", "-work-demo", id+".jsonl")
		write(t, path, claudeTurn(id, 1, "hello", "hi"))
		f.track(t, id, path)
		if _, err := f.live.Get(ctx, "claude-code", id); err != nil {
			t.Fatal(err)
		}
		f.clock.advance(time.Millisecond)
	}
	if n := f.live.cachedLen(); n != MaxCached {
		t.Fatalf("%d transcripts held, want %d", n, MaxCached)
	}
	// Every session but s1 goes silent past DropAfter and is dropped.
	f.clock.advance(DropAfter - time.Minute)
	f.track(t, "s1", f.path)
	f.clock.advance(2 * time.Minute)
	f.tracker.Sweep()
	if _, err := f.live.Get(ctx, "claude-code", "s1"); err != nil {
		t.Fatal(err)
	}
	if n := f.live.cachedLen(); n != 1 {
		t.Fatalf("%d transcripts held after the others were dropped", n)
	}
}

func TestCodexTranscriptIsFoundByIDWithoutAPath(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000001"
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
	answer, err := f.live.Get(context.Background(), "codex", id)
	if err != nil || !strings.Contains(string(answer.Body), "Rename the flag") {
		t.Fatalf("%v %s", err, answer.Body)
	}
}
