package livesessions

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// served is each session's state and wait as the list serves them, by agent
// and id.
func served(t *testing.T, f *transcriptFixture) map[string]Session {
	t.Helper()
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	var body struct {
		Sessions []Session `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", w.Body.String(), err)
	}
	out := map[string]Session{}
	for _, s := range body.Sessions {
		out[s.Agent+"/"+s.ID] = s
	}
	return out
}

// toolResult is a user line of Claude Code answering the tool call id, its
// output padded to size.
func toolResult(id string, size int) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-10-27T18:02:00.000Z","sessionId":"s1","message":{"role":"user","content":[{"tool_use_id":%q,"type":"tool_result","content":%q}]}}`, id, strings.Repeat("x", size))
}

func TestASilentSessionRunsWhileAToolCallHasNoResultAndThenWentQuiet(t *testing.T) {
	f := newTranscriptFixture(t)
	appendLines(t, f.path, toolUse("2026-10-27T18:01:00.000Z", "Agent", `{"description":"Survey the tests"}`))
	// A subagent working for longer than IdleAfter reports nothing.
	f.clock.advance(IdleAfter + time.Minute)
	if s := served(t, f)["claude-code/s1"]; s.State != StateRunning || s.WaitingReason != "" {
		t.Fatalf("while the subagent runs: %+v", s)
	}
	// Its result is longer than a line read whole: its id still answers it.
	appendLines(t, f.path, toolResult("t", maxLine+1024))
	f.clock.advance(TitleRecheckEvery)
	if s := served(t, f)["claude-code/s1"]; s.State != StateIdle || s.WaitingReason != WaitQuiet || !s.WaitingSince.Equal(t0) {
		t.Fatalf("once it answered and nothing came: %+v", s)
	}
}

func TestAPromptEndsTheToolCallsStillOpen(t *testing.T) {
	f := newTranscriptFixture(t)
	appendLines(t, f.path,
		toolUse("2026-10-27T18:01:00.000Z", "Bash", `{"command":"just e2e"}`),
		// Interrupted without a result, then typed again and interrupted
		// before any reply.
		`{"type":"user","timestamp":"2026-10-27T18:02:00.000Z","sessionId":"s1","message":{"role":"user","content":"The cards are too long"}}`,
	)
	f.clock.advance(IdleAfter + time.Minute)
	if s := served(t, f)["claude-code/s1"]; s.WaitingReason != WaitQuiet {
		t.Fatalf("served %+v", s)
	}
}

func TestACodexCallRunsUntilItsOutput(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000004"
	path := filepath.Join(f.home, ".codex", "sessions", "2026", "10", "20", "rollout-2026-10-20T09-00-00-"+id+".jsonl")
	write(t, path, `{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo"}}
{"timestamp":"2026-10-20T09:00:10.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"go test ./...\"]}","call_id":"c1"}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	f.clock.advance(IdleAfter + time.Minute)
	if s := served(t, f)["codex/"+id]; s.State != StateRunning {
		t.Fatalf("while the command runs: %+v", s)
	}
	appendLines(t, path, `{"timestamp":"2026-10-20T09:20:00.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`)
	f.clock.advance(TitleRecheckEvery)
	if s := served(t, f)["codex/"+id]; s.State != StateIdle || s.WaitingReason != WaitQuiet {
		t.Fatalf("after its output: %+v", s)
	}
}

func TestSettleLeavesAWaitAndAWorkingSession(t *testing.T) {
	waiting := Session{State: StateIdle, WaitingReason: WaitTurnFinished, WaitingSince: t0}
	if got := settle(waiting, nil); got != waiting {
		t.Fatalf("waiting: %+v", got)
	}
	working := Session{State: StateRunning, LastActivityAt: t0}
	if got := settle(working, nil); got != working {
		t.Fatalf("working: %+v", got)
	}
	silent := Session{State: StateIdle, LastActivityAt: t0}
	if got := settle(silent, &Activity{}); got.WaitingReason != WaitQuiet || !got.WaitingSince.Equal(t0) || got.State != StateIdle {
		t.Fatalf("silent: %+v", got)
	}
}

func TestTheListMovesWithARunningSessionsTranscript(t *testing.T) {
	f := newTranscriptFixture(t)
	var last []byte
	if f.live.moved(&last) {
		t.Fatal("the first look moved")
	}
	// The agent works on without a hook event.
	appendLines(t, f.path, toolUse("2026-10-27T18:01:00.000Z", "Bash", `{"command":"go test ./..."}`))
	f.clock.advance(TitleRecheckEvery)
	if !f.live.moved(&last) {
		t.Fatal("a new step did not move the list")
	}
	f.clock.advance(TitleRecheckEvery)
	if f.live.moved(&last) {
		t.Fatal("the list moved with nothing new")
	}
}

func TestACodexSessionIsFoundByItsRolloutsNameBesideCompressedOnes(t *testing.T) {
	home := t.TempDir()
	const id = "019f0000-0000-7000-8000-000000000005"
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "20")
	// Codex compresses old rollouts; baum does not read them, and they must
	// not stop the lookup.
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "02", "11", "rollout-2026-02-11T10-42-31-019c0000-0000-7000-8000-000000000001.jsonl.zst"), "zstd")
	// A rollout named for the id whose first line names another thread is
	// not it.
	write(t, filepath.Join(day, "rollout-2026-10-20T08-00-00-"+id+".jsonl"),
		`{"timestamp":"2026-10-20T08:00:00.000Z","type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000006","cwd":"/work/demo","source":"cli"}}`+"\n")
	want := filepath.Join(day, "rollout-2026-10-20T09-00-00-"+id+".jsonl")
	write(t, want, `{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo","source":"vscode"}}`+"\n")
	got, err := findRollout(filepath.Join(home, ".codex"), id)
	if err != nil || got != want {
		t.Fatalf("found %q, %v", got, err)
	}
	if _, err := findRollout(filepath.Join(home, ".codex"), "../x"); err == nil {
		t.Fatal("an id that is no thread's was found")
	}
}

func TestASessionWithNoConversationIsNotListed(t *testing.T) {
	f := newTranscriptFixture(t)
	// Opened to resume another and closed: no transcript was ever written.
	path := filepath.Join(f.home, ".claude", "projects", "-work-demo", "launcher.jsonl")
	start := event("launcher", "session-start", f.clock.now, "/work/demo")
	start.Session.TranscriptPath = path
	apply(f.tracker, start)
	if _, ok := served(t, f)["claude-code/launcher"]; ok {
		t.Fatal("a session with no conversation is listed")
	}
	if _, ok := served(t, f)["claude-code/s1"]; !ok {
		t.Fatal("the session with one is not")
	}
	// Its first prompt makes it one.
	write(t, path, claudeTurn("launcher", 1, "Tidy the buttons", ""))
	f.clock.advance(TitleRecheckEvery)
	if s, ok := served(t, f)["claude-code/launcher"]; !ok || s.State != StateRunning {
		t.Fatalf("after its first prompt: %+v, %v", s, ok)
	}
}

func TestTheLatestPromptIsTheLastMessageTheUserTyped(t *testing.T) {
	f := newTranscriptFixture(t)
	f.tracker.Mask = func(s string) string { return strings.ReplaceAll(s, plantedDSN, "[masked]") }
	appendLines(t, f.path,
		`{"type":"user","timestamp":"2026-10-27T18:02:00.000Z","sessionId":"s1","message":{"role":"user","content":"Use `+plantedDSN+`\nfor the\ttests"}}`,
		// Neither a command nor an interruption was typed.
		`{"type":"user","timestamp":"2026-10-27T18:03:00.000Z","sessionId":"s1","message":{"role":"user","content":"<command-name>/compact</command-name>"}}`,
		`{"type":"user","timestamp":"2026-10-27T18:04:00.000Z","sessionId":"s1","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		// Nor is a message from another session.
		`{"type":"user","isMeta":true,"timestamp":"2026-10-27T18:05:00.000Z","sessionId":"s1","message":{"role":"user","content":"Another session says hi"}}`,
	)
	if a := activities(t, f)["claude-code/s1"]; a == nil || a.LastPrompt != "Use [masked] for the tests" {
		t.Fatalf("activity %+v", a)
	}
}

func TestACodexSessionsLatestPromptInEitherFormat(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000008"
	path := filepath.Join(f.home, ".codex", "sessions", "2026", "10", "28", "rollout-2026-10-28T18-25-14-"+id+".jsonl")
	write(t, path, `{"timestamp":"2026-10-28T18:25:14.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo","source":"cli"}}
{"timestamp":"2026-10-28T18:25:15.000Z","type":"event_msg","payload":{"type":"user_message","message":"Rename the flag"}}
{"timestamp":"2026-10-28T18:26:20.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u2","content":[{"type":"text","text":"Now update the docs"}]}}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	if a := activities(t, f)["codex/"+id]; a == nil || a.LastPrompt != "Now update the docs" {
		t.Fatalf("activity %+v", a)
	}
	if got := titles(t, f)["codex/"+id]; got != "Rename the flag" {
		t.Fatalf("title %q", got)
	}
}

func TestAnAnswerOrAMessageSentMidTurnIsTheLatestPrompt(t *testing.T) {
	f := newTranscriptFixture(t)
	// The answer to the agent's question, as a tool result.
	appendLines(t, f.path,
		toolUse("2026-10-29T18:00:00.000Z", "AskUserQuestion", `{"questions":[]}`),
		`{"type":"user","timestamp":"2026-10-29T18:01:00.000Z","sessionId":"s1","message":{"role":"user","content":[{"tool_use_id":"t","type":"tool_result","content":"The user answered"}]},"toolUseResult":{"questions":[],"answers":{"What next?":"Add the type something option"}}}`,
	)
	if a := activities(t, f)["claude-code/s1"]; a == nil || a.LastPrompt != "Add the type something option" {
		t.Fatalf("after the answer: %+v", a)
	}
	// A message sent mid-turn, its screenshot past maxLine; the tool call
	// running goes on.
	appendLines(t, f.path,
		toolUse("2026-10-29T18:02:00.000Z", "Bash", `{"command":"just test"}`),
		`{"type":"attachment","timestamp":"2026-10-29T18:03:00.000Z","sessionId":"s1","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"My prompt is stale [Image #22]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+strings.Repeat("A", maxLine)+`"}}]}}`,
	)
	f.clock.advance(TitleRecheckEvery)
	a := activities(t, f)["claude-code/s1"]
	if a == nil || a.LastPrompt != "My prompt is stale [Image #22]" {
		t.Fatalf("after the message: %+v", a)
	}
	f.clock.advance(IdleAfter)
	if s := served(t, f)["claude-code/s1"]; s.State != StateRunning {
		t.Fatalf("the tool call ended with the message: %+v", s)
	}
}

func TestAWorkingSessionIsReadAgainWithinASecond(t *testing.T) {
	f := newTranscriptFixture(t)
	activities(t, f)
	// Working: what it says next shows after a second.
	appendLines(t, f.path, `{"type":"assistant","timestamp":"2026-10-27T18:01:00.000Z","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"Running the tests."}]}}`)
	f.clock.advance(WorkingRecheckEvery)
	if a := activities(t, f)["claude-code/s1"]; a == nil || a.LastMessage != "Running the tests." {
		t.Fatalf("working: %+v", a)
	}
	// Waiting on its user: read again at the slower pace.
	stop(f)
	activities(t, f)
	appendLines(t, f.path, `{"type":"assistant","timestamp":"2026-10-27T18:02:00.000Z","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`)
	f.clock.advance(WorkingRecheckEvery)
	if a := activities(t, f)["claude-code/s1"]; a.LastMessage != "Running the tests." {
		t.Fatalf("waiting, read early: %+v", a)
	}
	f.clock.advance(TitleRecheckEvery)
	if a := activities(t, f)["claude-code/s1"]; a.LastMessage != "Done." {
		t.Fatalf("waiting, not read: %+v", a)
	}
}

func TestACodexPromptTheAppFramedOnceIsTheUsersText(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000009"
	write(t, filepath.Join(f.home, ".codex", "sessions", "2026", "10", "29", "rollout-2026-10-29T18-00-00-"+id+".jsonl"),
		`{"timestamp":"2026-10-29T18:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo","source":"cli"}}
{"timestamp":"2026-10-29T18:01:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"`+fromBaum+` tell me more"}]}}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	if a := activities(t, f)["codex/"+id]; a == nil || a.LastPrompt != "tell me more" {
		t.Fatalf("activity %+v", a)
	}
}
