package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestConversationsKeepTurnsPerPullRequest(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"Owner/Repo", 7}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	first := Conversation{ID: "a", Version: 1, Backend: "claude", Model: "m", Created: now, Updated: now}
	if err := s.CreateConversation(ctx, key, first, Turn{Seq: 1, Question: "q1", Focus: []byte(`{"kind":"overview"}`), JobID: "j1", State: TurnQueued, Created: now}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := s.CreateConversation(ctx, Key{"owner/repo", 7}, Conversation{ID: "b", Version: 2, Backend: "codex", Created: later, Updated: later}, Turn{Seq: 1, Question: "other", JobID: "j9", State: TurnQueued, Created: later}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateConversation(ctx, Key{"owner/repo", 8}, Conversation{ID: "c", Version: 1, Created: now, Updated: now}, Turn{Seq: 1, Question: "elsewhere", JobID: "j8", State: TurnQueued, Created: now}); err != nil {
		t.Fatal(err)
	}
	// A pending turn keeps the conversation busy unless its job is gone.
	if _, _, err := s.AppendTurn(ctx, key, "a", Turn{Question: "q2", JobID: "j2", State: TurnQueued, Created: later}, func(string) (string, string, bool) { return "", "", false }); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("busy: %v", err)
	}
	if started, err := s.StartTurn(ctx, key, "a", 1); err != nil || !started {
		t.Fatalf("start: %v %v", started, err)
	}
	cost := 0.5
	if ok, err := s.SettleTurn(ctx, key, "a", 1, Settlement{State: TurnCompleted, Answer: []byte(`{"answer_markdown":"x"}`), CostUSD: &cost, CostReason: "r", Finished: later}); err != nil || !ok {
		t.Fatalf("settle: %v %v", ok, err)
	}
	// Settled stays settled.
	if ok, err := s.SettleTurn(ctx, key, "a", 1, Settlement{State: TurnFailed, Finished: later}); err != nil || ok {
		t.Fatalf("settled twice: %v %v", ok, err)
	}
	_, seq, err := s.AppendTurn(ctx, key, "a", Turn{Question: "q2", JobID: "j2", State: TurnQueued, Created: later}, nil)
	if err != nil || seq != 2 {
		t.Fatalf("append: %d %v", seq, err)
	}
	if err := s.NoteTurn(ctx, key, "a", 2, "note"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConversationSession(ctx, key, "a", "sess", "/runs/x", true, later); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AppendTurn(ctx, key, "a", Turn{Question: "q3", JobID: "j3", State: TurnQueued, Created: later}, func(id string) (string, string, bool) { return TurnInterrupted, TurnInterrupted, id == "j2" }); err != nil {
		t.Fatalf("stale turn kept the conversation busy: %v", err)
	}
	list, err := s.Conversations(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("list: %+v", list)
	}
	a := list[1]
	if a.SessionID != "sess" || a.Directory != "/runs/x" || !a.Seeded || len(a.Turns) != 3 {
		t.Fatalf("a: %+v", a)
	}
	t1, t2, t3 := a.Turns[0], a.Turns[1], a.Turns[2]
	if t1.State != TurnCompleted || string(t1.Answer) != `{"answer_markdown":"x"}` || t1.CostUSD == nil || *t1.CostUSD != 0.5 || t1.Finished == nil || string(t1.Focus) != `{"kind":"overview"}` {
		t.Errorf("t1: %+v", t1)
	}
	if t2.State != TurnInterrupted || t2.Reason != TurnInterrupted || t2.Note != "note" || t2.Finished == nil {
		t.Errorf("t2: %+v", t2)
	}
	if t3.State != TurnQueued || t3.Seq != 3 || t3.Focus != nil || t3.CostUSD != nil {
		t.Errorf("t3: %+v", t3)
	}
	// Another pull request cannot reach the conversation.
	other := Key{"owner/repo", 8}
	if _, err := s.Conversation(ctx, other, "a"); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("other pr: %v", err)
	}
	if _, _, err := s.AppendTurn(ctx, other, "a", Turn{JobID: "x", State: TurnQueued}, nil); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("append from another pr: %v", err)
	}
	if _, err := s.SettleTurn(ctx, other, "a", 3, Settlement{State: TurnFailed}); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("settle from another pr: %v", err)
	}
}

// A store made before chats existed lists none when opened read-only, and
// gains the tables on its next write open.
func TestStoreWithoutChatTables(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	db, err := sql.Open("sqlite", s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE turns; DROP TABLE conversations;`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	read, err := Open("", s.Root(), false)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := read.Conversations(ctx, Key{"o/r", 1}); err != nil || len(list) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := read.Conversation(ctx, Key{"o/r", 1}, "a"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatal(err)
	}
	empty, err := Open("", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := empty.Conversations(ctx, Key{"o/r", 1}); err != nil || len(list) != 0 {
		t.Fatalf("no database: %+v %v", list, err)
	}
	write, err := Open("", s.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.CreateConversation(ctx, Key{"o/r", 1}, Conversation{ID: "a", Version: 1}, Turn{Seq: 1, JobID: "j", State: TurnQueued}); err != nil {
		t.Fatal(err)
	}
}
