package agentrequests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/livesessions"
)

type sessions map[string]livesessions.Session

func (s sessions) Lookup(agent, id string) (livesessions.Session, string, bool) {
	session, ok := s[agent+"/"+id]
	return session, "", ok
}

func newStore(t *testing.T) (*Store, *atomic.Int32) {
	t.Helper()
	var notified atomic.Int32
	return &Store{
		Mask:     func(s string) string { return strings.ReplaceAll(s, "sekrit", "[masked]") },
		Notify:   func() { notified.Add(1) },
		StateDir: t.TempDir(),
		Sessions: sessions{
			"claude-code/s1": {State: livesessions.StateRunning},
			"claude-code/s2": {State: livesessions.StateIdle, WaitingReason: livesessions.WaitTurnFinished},
			"claude-code/s3": {State: livesessions.StateEnded},
			"codex/c1":       {State: livesessions.StateIdle, WaitingReason: livesessions.WaitPermission},
		},
	}, &notified
}

func bash(command string) Ask {
	return Ask{
		Agent: AgentClaudeCode, SessionID: "s1", Kind: KindPermission, Tool: "Bash",
		Digest: Digest("Bash", json.RawMessage(`{"command":"`+command+`"}`)), StartedAt: time.Now().Add(-time.Second),
		Input:       []Row{{Label: "Command", Value: command + " --token sekrit\r\nsecond line", Code: true}},
		Suggestions: []string{"Always allow Bash(make:*) in this project"},
	}
}

// ask runs Ask in the background and waits until the request is pending.
func ask(t *testing.T, ctx context.Context, s *Store, a Ask) (<-chan Decision, string) {
	t.Helper()
	before := map[string]bool{}
	requests, _ := s.List()
	for _, r := range requests {
		before[r.ID] = true
	}
	out := make(chan Decision, 1)
	go func() {
		d, err := s.Ask(ctx, a)
		if err != nil {
			t.Error(err)
		}
		out <- d
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		requests, _ := s.List()
		for _, r := range requests {
			if !before[r.ID] {
				return out, r.ID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the request was never pending")
	return nil, ""
}

func decision(t *testing.T, ch <-chan Decision) Decision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
		return Decision{}
	}
}

func TestAnswerDecidesOnceAndShowsMaskedInput(t *testing.T) {
	s, notified := newStore(t)
	ch, id := ask(t, context.Background(), s, bash("make"))
	requests, _ := s.List()
	if len(requests) != 1 {
		t.Fatalf("requests %+v", requests)
	}
	r := requests[0]
	if r.Kind != KindPermission || r.Tool != "Bash" || r.SessionID != "s1" || r.Input[0].Value != "make --token [masked]\nsecond line" || !r.Input[0].Code ||
		len(r.Suggestions) != 1 || r.Suggestions[0].ID != "0" || r.Questions == nil {
		t.Fatalf("request %+v", r)
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow, Suggestion: "1"}); err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("unknown suggestion: %v", err)
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow, Suggestion: "0"}); err != nil {
		t.Fatal(err)
	}
	if d := decision(t, ch); d.Behavior != BehaviorAllow || d.Suggestion == nil || *d.Suggestion != 0 {
		t.Fatalf("decision %+v", d)
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorDeny}); !errors.Is(err, ErrGone) {
		t.Fatalf("second answer: %v", err)
	}
	if requests, _ := s.List(); len(requests) != 0 {
		t.Fatalf("still pending: %+v", requests)
	}
	if notified.Load() < 2 {
		t.Fatalf("notified %d times", notified.Load())
	}
}

func TestQuestionAnswersMapToOptions(t *testing.T) {
	s, _ := newStore(t)
	a := Ask{Agent: AgentClaudeCode, SessionID: "s1", Kind: KindQuestion, Tool: "AskUserQuestion", Questions: []Question{
		{Question: "Which approach for sekrit?", Header: "Approach", Options: []Option{{Label: "A"}, {Label: "B"}}},
		{Question: "Which parts?", MultiSelect: true, Options: []Option{{Label: "x"}, {Label: "y"}, {Label: "z"}}},
	}}
	ch, id := ask(t, context.Background(), s, a)
	requests, _ := s.List()
	if q := requests[0].Questions[0]; q.Question != "Which approach for [masked]?" || q.Options[1].Label != "B" {
		t.Fatalf("question %+v", q)
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow, Answers: map[string]string{"Which approach for [masked]?": "B"}}); err == nil {
		t.Fatal("an unanswered question was accepted")
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow, Answers: map[string]string{"Which approach for [masked]?": "B", "Who?": "me"}}); err == nil {
		t.Fatal("an unknown question was accepted")
	}
	if err := s.Answer(id, Reply{Behavior: BehaviorAllow, Answers: map[string]string{"Which approach for [masked]?": "something else", "Which parts?": "x, z"}}); err != nil {
		t.Fatal(err)
	}
	d := decision(t, ch)
	if d.Behavior != BehaviorAllow || len(d.Answers) != 2 || d.Answers[0].Text != "something else" || len(d.Answers[1].Options) != 2 || d.Answers[1].Options[0] != 0 || d.Answers[1].Options[1] != 2 {
		t.Fatalf("decision %+v", d)
	}
}

func TestWithdrawals(t *testing.T) {
	event := func(name, digest string) hookevents.Event {
		return hookevents.Event{Kind: hookevents.KindAgent, Agent: AgentClaudeCode, Name: name, Time: time.Now().UTC(), Session: &hookevents.Session{ID: "s1", ToolDigest: digest}}
	}
	t.Run("the same tool call finished", func(t *testing.T) {
		s, _ := newStore(t)
		first, _ := ask(t, context.Background(), s, bash("make"))
		other, otherID := ask(t, context.Background(), s, bash("ls"))
		s.Observe(event("post-tool-use", Digest("Bash", json.RawMessage(`{"command":"make"}`))))
		if d := decision(t, first); d.Behavior != "" {
			t.Fatalf("decision %+v", d)
		}
		if requests, _ := s.List(); len(requests) != 1 || requests[0].ID != otherID {
			t.Fatalf("an unrelated tool's request was withdrawn: %+v", requests)
		}
		// Another session's events change nothing.
		e := event("stop", "")
		e.Session.ID = "s9"
		s.Observe(e)
		if requests, _ := s.List(); len(requests) != 1 {
			t.Fatalf("requests %+v", requests)
		}
		s.Observe(event("stop", ""))
		if d := decision(t, other); d.Behavior != "" {
			t.Fatalf("decision %+v", d)
		}
	})
	for _, name := range []string{"user-prompt-submit", "stop", "session-end"} {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			ch, id := ask(t, context.Background(), s, bash("make"))
			// An event from before the request's hook started is old news.
			old := event(name, "")
			old.Time = time.Now().Add(-time.Hour)
			s.Observe(old)
			if requests, _ := s.List(); len(requests) != 1 {
				t.Fatalf("an older event withdrew the request")
			}
			s.Observe(event(name, ""))
			if d := decision(t, ch); d.Behavior != "" {
				t.Fatalf("decision %+v", d)
			}
			if err := s.Answer(id, Reply{Behavior: BehaviorAllow}); !errors.Is(err, ErrGone) {
				t.Fatalf("answer after withdrawal: %v", err)
			}
		})
	}
	t.Run("the hook went away", func(t *testing.T) {
		s, notified := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		ch, id := ask(t, ctx, s, bash("make"))
		before := notified.Load()
		cancel()
		if d := decision(t, ch); d.Behavior != "" {
			t.Fatalf("decision %+v", d)
		}
		if err := s.Answer(id, Reply{Behavior: BehaviorAllow}); !errors.Is(err, ErrGone) {
			t.Fatalf("answer after disconnect: %v", err)
		}
		if notified.Load() == before {
			t.Fatal("the app was not told")
		}
	})
	t.Run("the hook went away: interrupted, unless it timed out", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			ask    func() Ask
			cancel bool
			want   bool
		}{
			{"turned down in the terminal", func() Ask { return bash("make") }, true, true},
			{"timed out", func() Ask { a := bash("make"); a.StartedAt = time.Now().Add(-ClaudeHold); return a }, true, false},
			{"a subagent's", func() Ask { a := bash("make"); a.AgentID = "sub"; return a }, true, false},
			{"withdrawn", func() Ask { return bash("make") }, false, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				s, _ := newStore(t)
				var got []string
				var mu sync.Mutex
				s.Interrupted = func(agent, session string, _ time.Time) {
					mu.Lock()
					defer mu.Unlock()
					got = append(got, agent+"/"+session)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a := c.ask()
				ch, _ := ask(t, ctx, s, a)
				if c.cancel {
					cancel()
				} else {
					s.Observe(event("post-tool-use", a.Digest))
				}
				decision(t, ch)
				mu.Lock()
				defer mu.Unlock()
				if want := c.want; (len(got) == 1 && got[0] == "claude-code/s1") != want || (!want && len(got) != 0) {
					t.Fatalf("interrupted %v, want %v", got, want)
				}
			})
		}
	})
	t.Run("answered in the app", func(t *testing.T) {
		s, _ := newStore(t)
		var answered, interrupted []string
		var mu sync.Mutex
		s.Answered = func(agent, session string, _ time.Time) {
			mu.Lock()
			defer mu.Unlock()
			answered = append(answered, agent+"/"+session)
		}
		s.Interrupted = func(agent, session string, _ time.Time) {
			mu.Lock()
			defer mu.Unlock()
			interrupted = append(interrupted, agent+"/"+session)
		}
		ch, id := ask(t, context.Background(), s, bash("make"))
		if err := s.Answer(id, Reply{Behavior: BehaviorDeny}); err != nil {
			t.Fatal(err)
		}
		decision(t, ch)
		mu.Lock()
		defer mu.Unlock()
		if len(answered) != 1 || answered[0] != "claude-code/s1" || len(interrupted) != 0 {
			t.Fatalf("answered %v, interrupted %v", answered, interrupted)
		}
	})
	t.Run("answered in the terminal before the request arrived", func(t *testing.T) {
		s, _ := newStore(t)
		a := bash("make")
		s.Observe(event("post-tool-use", a.Digest))
		d, err := s.Ask(context.Background(), a)
		if err != nil || d.Behavior != "" {
			t.Fatalf("%+v %v", d, err)
		}
		if requests, _ := s.List(); len(requests) != 0 {
			t.Fatalf("requests %+v", requests)
		}
	})
}

func TestCodexRequestsNeedTheSettingAndTimeOut(t *testing.T) {
	s, _ := newStore(t)
	s.CodexWait = 50 * time.Millisecond
	a := Ask{Agent: AgentCodex, SessionID: "c1", Kind: KindPermission, Tool: "Bash", Input: []Row{{Label: "Command", Value: "ls"}}}
	start := time.Now()
	if d, err := s.Ask(context.Background(), a); err != nil || d.Behavior != "" || time.Since(start) > time.Second {
		t.Fatalf("with the setting off: %+v %v", d, err)
	}
	if _, err := s.SetSettings(Settings{CodexApprovals: true}); err != nil {
		t.Fatal(err)
	}
	// A new store reads the saved setting.
	if (&Store{StateDir: s.StateDir}).Settings() != (Settings{CodexApprovals: true}) {
		t.Fatal("the setting was not saved")
	}
	start = time.Now()
	if d, err := s.Ask(context.Background(), a); err != nil || d.Behavior != "" || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("timed out: %+v %v after %s", d, err, time.Since(start))
	}
	s.CodexWait = time.Minute
	ch, id := ask(t, context.Background(), s, a)
	if err := s.Answer(id, Reply{Behavior: BehaviorDeny}); err != nil {
		t.Fatal(err)
	}
	if d := decision(t, ch); d.Behavior != BehaviorDeny {
		t.Fatalf("decision %+v", d)
	}
	// Turning the setting off hands a waiting request back to Codex.
	ch, _ = ask(t, context.Background(), s, a)
	if _, err := s.SetSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	if d := decision(t, ch); d.Behavior != "" {
		t.Fatalf("decision %+v", d)
	}
}

func TestMessages(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"s2", "s3", "unknown"} {
		if err := s.Queue(AgentClaudeCode, id, "hi"); !errors.Is(err, ErrNotRunning) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if err := s.Queue(AgentClaudeCode, "s1", "  \n"); err == nil || errors.Is(err, ErrNotRunning) {
		t.Fatalf("blank: %v", err)
	}
	if err := s.Queue(AgentClaudeCode, "s1", strings.Repeat("x", MaxMessage+1)); err == nil {
		t.Fatal("a long message was accepted")
	}
	if err := s.Queue(AgentClaudeCode, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue(AgentClaudeCode, "s1", "use the sekrit"); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue(AgentCodex, "c1", "codex too"); err != nil {
		t.Fatal(err)
	}
	if _, messages := s.List(); len(messages) != 2 || messages[0].Text != "use the [masked]" {
		t.Fatalf("messages %+v", messages)
	}
	if m, ok := s.TakeMessage(AgentClaudeCode, "s1"); !ok || m.Text != "use the sekrit" {
		t.Fatalf("take %+v %v", m, ok)
	}
	if _, ok := s.TakeMessage(AgentClaudeCode, "s1"); ok {
		t.Fatal("a message was taken twice")
	}
	s.DeleteMessage(AgentCodex, "c1")
	s.DeleteMessage(AgentCodex, "c1")
	if _, messages := s.List(); len(messages) != 0 {
		t.Fatalf("messages %+v", messages)
	}
	if err := s.Queue(AgentClaudeCode, "s1", "bye"); err != nil {
		t.Fatal(err)
	}
	s.Observe(hookevents.Event{Kind: hookevents.KindAgent, Agent: AgentClaudeCode, Name: "session-end", Time: time.Now(), Session: &hookevents.Session{ID: "s1"}})
	if _, ok := s.TakeMessage(AgentClaudeCode, "s1"); ok {
		t.Fatal("an ended session kept its message")
	}
}

// A message belongs to the turn it was sent in: a new prompt drops one queued
// before it, a stop does not (its hook takes the message after recording the
// stop), and one queued after the prompt stays.
func TestMessagesDoNotOutliveTheirTurn(t *testing.T) {
	s, notified := newStore(t)
	event := func(name string, at time.Time) hookevents.Event {
		return hookevents.Event{Kind: hookevents.KindAgent, Agent: AgentClaudeCode, Name: name, Time: at, Session: &hookevents.Session{ID: "s1"}}
	}
	if err := s.Queue(AgentClaudeCode, "s1", "for this turn"); err != nil {
		t.Fatal(err)
	}
	s.Observe(event("stop", time.Now().Add(time.Second)))
	if _, messages := s.List(); len(messages) != 1 {
		t.Fatal("a stop dropped the message its hook takes")
	}
	s.Observe(event("user-prompt-submit", time.Now().Add(-time.Minute)))
	if _, messages := s.List(); len(messages) != 1 {
		t.Fatal("a prompt from before the message dropped it")
	}
	before := notified.Load()
	s.Observe(event("user-prompt-submit", time.Now().Add(time.Second)))
	if _, messages := s.List(); len(messages) != 0 || notified.Load() == before {
		t.Fatalf("the next prompt kept the message: %+v", messages)
	}
}

// A message whose answer could not be written to the Stop hook is queued
// again, unless a newer one replaced it.
func TestUndeliveredMessageIsRequeued(t *testing.T) {
	s, _ := newStore(t)
	take := func() daemon.Delivery {
		t.Helper()
		result, err := s.take(context.Background(), json.RawMessage(`{"agent":"claude-code","session_id":"s1"}`))
		delivery, ok := result.(daemon.Delivery)
		if err != nil || !ok || delivery.Result.(Taken).Text == "" {
			t.Fatalf("take: %#v %v", result, err)
		}
		return delivery
	}
	if err := s.Queue(AgentClaudeCode, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	take().Written(errors.New("broken pipe"))
	if m, ok := s.TakeMessage(AgentClaudeCode, "s1"); !ok || m.Text != "first" {
		t.Fatalf("not requeued: %+v", m)
	}
	if err := s.Queue(AgentClaudeCode, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	delivery := take()
	if err := s.Queue(AgentClaudeCode, "s1", "newer"); err != nil {
		t.Fatal(err)
	}
	delivery.Written(errors.New("broken pipe"))
	if m, _ := s.TakeMessage(AgentClaudeCode, "s1"); m.Text != "newer" {
		t.Fatalf("the requeue replaced a newer message: %+v", m)
	}
	if err := s.Queue(AgentClaudeCode, "s1", "delivered"); err != nil {
		t.Fatal(err)
	}
	take().Written(nil)
	if _, ok := s.TakeMessage(AgentClaudeCode, "s1"); ok {
		t.Fatal("a delivered message was queued again")
	}
	if result, err := s.take(context.Background(), json.RawMessage(`{"agent":"claude-code","session_id":"s1"}`)); err != nil || result != (Taken{}) {
		t.Fatalf("nothing queued: %#v %v", result, err)
	}
}

func TestRequestsThatCannotBeShownSafelyAreRefused(t *testing.T) {
	question := func(questions ...Question) Ask {
		return Ask{Agent: AgentClaudeCode, SessionID: "s1", Kind: KindQuestion, Tool: "AskUserQuestion", Questions: questions}
	}
	for name, tt := range map[string]struct {
		mask func(string) string
		ask  Ask
	}{
		"the mask failed": {func(string) string { return "" }, bash("make")},
		"the same question twice": {nil, question(
			Question{Question: "Which?", Options: []Option{{Label: "A"}}},
			Question{Question: "Which?\n", Options: []Option{{Label: "B"}}})},
		"the same option twice": {nil, question(Question{Question: "Which?", Options: []Option{{Label: "A"}, {Label: " A"}}})},
		"a label that joins":    {nil, question(Question{Question: "Which?", MultiSelect: true, Options: []Option{{Label: "x, y"}, {Label: "z"}}})},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			s.Mask = tt.mask
			start := time.Now()
			d, err := s.Ask(context.Background(), tt.ask)
			if err != nil || d.Behavior != "" || time.Since(start) > time.Second {
				t.Fatalf("%+v %v", d, err)
			}
			if requests, _ := s.List(); len(requests) != 0 {
				t.Fatalf("shown: %+v", requests)
			}
		})
	}
}

// A subagent's request outlives the main turn's boundaries, not its own stop
// or the session's end.
func TestSubagentRequests(t *testing.T) {
	event := func(name, agentID string) hookevents.Event {
		return hookevents.Event{Kind: hookevents.KindAgent, Agent: AgentClaudeCode, Name: name, Time: time.Now().UTC(), Session: &hookevents.Session{ID: "s1", AgentID: agentID}}
	}
	s, _ := newStore(t)
	a := bash("make")
	a.AgentID = "agent-1"
	ch, _ := ask(t, context.Background(), s, a)
	mainAgent, _ := ask(t, context.Background(), s, bash("ls"))
	s.Observe(event("stop", ""))
	s.Observe(event("user-prompt-submit", ""))
	if d := decision(t, mainAgent); d.Behavior != "" {
		t.Fatalf("main %+v", d)
	}
	s.Observe(event("subagent-stop", "agent-2"))
	if requests, _ := s.List(); len(requests) != 1 {
		t.Fatalf("requests %+v", requests)
	}
	s.Observe(event("subagent-stop", "agent-1"))
	if d := decision(t, ch); d.Behavior != "" {
		t.Fatalf("subagent %+v", d)
	}
	// Later hooks start after the marks so far.
	time.Sleep(time.Millisecond)
	a.StartedAt = time.Now()
	ch, _ = ask(t, context.Background(), s, a)
	s.Observe(event("session-end", ""))
	if d := decision(t, ch); d.Behavior != "" {
		t.Fatalf("subagent at the end %+v", d)
	}
	time.Sleep(time.Millisecond)
	a.StartedAt = time.Now()
	ch, _ = ask(t, context.Background(), s, a)
	s.Observe(hookevents.Event{Kind: hookevents.KindAgent, Agent: AgentClaudeCode, Name: "post-tool-use-failure", Time: time.Now().UTC(),
		Session: &hookevents.Session{ID: "s1", AgentID: "agent-1", ToolDigest: a.Digest}})
	if d := decision(t, ch); d.Behavior != "" {
		t.Fatalf("failed tool %+v", d)
	}
}

func TestHandler(t *testing.T) {
	s, _ := newStore(t)
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	ch, id := ask(t, context.Background(), s, bash("make"))
	w := do("GET", "/api/agent-requests", "")
	var listed struct {
		Requests []Request       `json:"requests"`
		Messages []Message       `json:"messages"`
		Settings map[string]bool `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != http.StatusOK || len(listed.Requests) != 1 || listed.Messages == nil || listed.Settings["codex_approvals"] {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, member := range []string{`"created_at"`, `"input"`, `"suggestions"`, `"questions":[]`, `"messages":[]`} {
		if !strings.Contains(w.Body.String(), member) {
			t.Fatalf("%s missing from %s", member, w.Body.String())
		}
	}
	if w := do("POST", "/api/agent-requests/"+id+"/answer", `{"behavior":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad behaviour: %d", w.Code)
	}
	if w := do("POST", "/api/agent-requests/"+id+"/answer", `{"behavior":"deny"}`); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("deny: %d %s", w.Code, w.Body.String())
	}
	if d := decision(t, ch); d.Behavior != BehaviorDeny {
		t.Fatalf("decision %+v", d)
	}
	w = do("POST", "/api/agent-requests/"+id+"/answer", `{"behavior":"allow"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"request_gone"`) {
		t.Fatalf("gone: %d %s", w.Code, w.Body.String())
	}
	if w := do("PUT", "/api/agent-requests/settings", `{"codex_approvals":true}`); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"codex_approvals":true}` {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, SettingsFile)); err != nil {
		t.Fatal(err)
	}
	if w := do("PUT", "/api/agent-requests/settings", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty settings: %d", w.Code)
	}
	if w := do("POST", "/api/agent-messages", `{"agent":"claude-code","session_id":"s2","text":"hi"}`); w.Code != http.StatusNotFound {
		t.Fatalf("not running: %d", w.Code)
	}
	if w := do("POST", "/api/agent-messages", `{"agent":"claude-code","session_id":"s1","text":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("blank: %d", w.Code)
	}
	if w := do("POST", "/api/agent-messages", `{"agent":"claude-code","session_id":"s1","text":"hi"}`); w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/agent-requests", ""); !strings.Contains(w.Body.String(), `"text":"hi"`) || !strings.Contains(w.Body.String(), `"queued_at"`) {
		t.Fatalf("listed: %s", w.Body.String())
	}
	for range 2 {
		if w := do("DELETE", "/api/agent-messages/claude-code/s1", ""); w.Code != http.StatusOK {
			t.Fatalf("delete: %d", w.Code)
		}
	}
	if _, ok := s.TakeMessage(AgentClaudeCode, "s1"); ok {
		t.Fatal("the message was not deleted")
	}
}

func TestDigest(t *testing.T) {
	a := Digest("Bash", json.RawMessage(`{"command":"ls","timeout":1.50}`))
	b := Digest("Bash", json.RawMessage(`{ "timeout": 1.50, "command": "ls" }`))
	if a == "" || a != b {
		t.Fatalf("%q %q", a, b)
	}
	if a == Digest("Edit", json.RawMessage(`{"command":"ls","timeout":1.50}`)) || a == Digest("Bash", json.RawMessage(`{"command":"ls","timeout":1.5}`)) {
		t.Fatal("different calls share a digest")
	}
	questions := `"questions":[{"question":"Which?","options":[{"label":"A"}]}]`
	if Digest("AskUserQuestion", json.RawMessage(`{`+questions+`}`)) != Digest("AskUserQuestion", json.RawMessage(`{`+questions+`,"answers":{"Which?":"A"}}`)) {
		t.Fatal("an answered question has another digest")
	}
	if Digest("Bash", json.RawMessage(`{`)) != "" {
		t.Fatal("invalid input has a digest")
	}
}
