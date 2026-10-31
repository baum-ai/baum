package chat_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/chat/chattest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/store"
)

// When only citations were wrong and the repair breaks the schema, the first
// answer stands without its invalid citations.
func TestRepairThatBreaksTheSchemaKeepsTheFirstAnswer(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	f.Agent.Reply = func(r agent.Request, n int) (agent.Result, error) {
		result := f.Agent.Default(r)
		if n == 0 {
			result.Document = chattest.Answer("first", chat.Citation{Path: "README.md", StartLine: 1, EndLine: 1}, chat.Citation{Path: "missing.go", StartLine: 1, EndLine: 1})
		} else {
			result.Document = []byte(`{"answer_markdown":"broken"}`)
		}
		return result, nil
	}
	begun := f.Ask(t, "", "q")
	if _, err := run(t, f, begun, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	answer := answerOf(t, f.Conversation(t, begun.Conversation).Turns[0])
	if answer.Markdown != "first" || len(answer.Citations) != 1 || answer.Citations[0].Path != "README.md" {
		t.Errorf("answer: %+v", answer)
	}
}

// A session id the CLI reports that is not a UUID is not kept, and a stored
// one that is not valid is treated as gone.
func TestInvalidSessionIDs(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	f.Agent.Reply = func(r agent.Request, n int) (agent.Result, error) {
		result := f.Agent.Default(r)
		if n == 0 {
			result.SessionID = "--not-a-session"
		}
		return result, nil
	}
	first := f.Ask(t, "", "q1")
	if _, err := run(t, f, first, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	c := f.Conversation(t, first.Conversation)
	if c.SessionID != "" || c.Turns[0].Note != chat.NoSessionNote {
		t.Fatalf("conversation: %+v", c)
	}
	// A stored id that is not valid seeds a new session.
	if err := f.Store.SetConversationSession(context.Background(), f.Key, first.Conversation, "bogus", f.ReviewDir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := f.Ask(t, first.Conversation, "q2")
	if _, err := run(t, f, second, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	r := f.Agent.Requests[1]
	seededRequest(t, f, r)
	if note := f.Conversation(t, first.Conversation).Turns[1].Note; !strings.Contains(note, "session id is not valid") {
		t.Errorf("note: %q", note)
	}
}

// An answer whose agent reported no session is kept with a note, and the
// next question seeds a session with the earlier turns instead of forking
// the review again.
func TestNoSessionReportedSeedsTheNextTurn(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	f.Agent.Reply = func(r agent.Request, n int) (agent.Result, error) {
		result := f.Agent.Default(r)
		if n == 0 {
			result.SessionID = ""
		}
		return result, nil
	}
	first := f.Ask(t, "", "q1")
	if _, err := run(t, f, first, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	c := f.Conversation(t, first.Conversation)
	if c.SessionID != "" || c.Turns[0].State != store.TurnCompleted || c.Turns[0].Note != chat.NoSessionNote {
		t.Fatalf("conversation: %+v", c)
	}
	second := f.Ask(t, first.Conversation, "q2")
	if _, err := run(t, f, second, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	seededRequest(t, f, f.Agent.Requests[1])
	if note := f.Conversation(t, first.Conversation).Turns[1].Note; !strings.Contains(note, "no agent session to continue") {
		t.Errorf("note: %q", note)
	}
}

func TestPassTimeoutAllowsFullRunLimit(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	f.Service.Config.Budgets.PassTimeout = config.Duration(200 * time.Millisecond)
	f.Service.Config.Budgets.RunTimeout = config.Duration(300 * time.Millisecond)
	if chat.PassTimeout(f.Service.Config) != 300*time.Millisecond {
		t.Fatalf("pass timeout %v", chat.PassTimeout(f.Service.Config))
	}
}

// A job's settlement brings its cost to a turn a next question already
// settled from the ended job.
func TestSettleFillsTheCostOfATurnAlreadySettled(t *testing.T) {
	f := chattest.New(t, chattest.Options{})
	ctx := context.Background()
	first := f.Ask(t, "", "q1")
	ended := func(string) (string, string, bool) { return store.TurnFailed, "budget", true }
	if _, err := f.Service.Begin(ctx, chat.Ask{Clone: f.Clone.ID, PR: first.PR, Version: 1, Conversation: first.Conversation, Question: "q2"}, "job-2", ended); err != nil {
		t.Fatal(err)
	}
	cost := 0.75
	if err := f.Service.Settle(ctx, first.Ref, store.TurnFailed, "budget", &cost, "priced"); err != nil {
		t.Fatal(err)
	}
	turn := f.Conversation(t, first.Conversation).Turns[0]
	if turn.State != store.TurnFailed || turn.Reason != "budget" || turn.CostUSD == nil || *turn.CostUSD != 0.75 || turn.CostReason != "priced" {
		t.Errorf("turn: %+v", turn)
	}
	other := 9.0
	if err := f.Service.Settle(ctx, first.Ref, store.TurnFailed, "budget", &other, "again"); err != nil {
		t.Fatal(err)
	}
	if turn := f.Conversation(t, first.Conversation).Turns[0]; *turn.CostUSD != 0.75 {
		t.Errorf("a recorded cost was replaced: %+v", turn)
	}
}
