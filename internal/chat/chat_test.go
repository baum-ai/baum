package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// A pull request's stored conversations are listed newest first, with their
// answers; a pending turn whose job ended reads as it ended.
func TestConversationsListsTheStoredOnes(t *testing.T) {
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
	answer, _ := json.Marshal(chat.Answer{Markdown: "Because.", Citations: []chat.Citation{{Path: "a.go", StartLine: 1, EndLine: 2}}})
	for i, id := range []string{"answered", "pending"} {
		created := at.Add(time.Duration(i) * time.Hour)
		c := store.Conversation{ID: id, Version: 1, Backend: "claude", Model: "m", Created: created, Updated: created}
		if err := st.CreateConversation(ctx, key, c, store.Turn{Seq: 1, Question: "Why " + id + "?", JobID: "job-" + id, State: store.TurnQueued, Created: created}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SettleTurn(ctx, key, "answered", 1, store.Settlement{State: store.TurnCompleted, Answer: answer, Finished: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	service := &chat.Service{Registry: registry}
	ended := func(job string) (string, string, bool) {
		return store.TurnInterrupted, store.TurnInterrupted, job == "job-pending"
	}
	list, err := service.Conversations(ctx, clone.ID, assembletest.Number, ended)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "pending" || list[1].ID != "answered" {
		t.Fatalf("conversations: %+v", list)
	}
	if turn := list[0].Turns[0]; turn.State != store.TurnInterrupted || turn.Reason != store.TurnInterrupted {
		t.Errorf("pending turn: %+v", turn)
	}
	var got chat.Answer
	if turn := list[1].Turns[0]; turn.State != store.TurnCompleted || json.Unmarshal(turn.Answer, &got) != nil || got.Markdown != "Because." || len(got.Citations) != 1 {
		t.Errorf("answered turn: %+v", turn)
	}
	if _, err := service.Conversations(ctx, clone.ID, 0, ended); !errors.Is(err, chat.ErrInvalid) {
		t.Errorf("no pr: %v", err)
	}
	if _, err := service.Conversations(ctx, "nope", 7, ended); !errors.Is(err, chat.ErrRepositoryNotFound) {
		t.Errorf("unregistered: %v", err)
	}
}
