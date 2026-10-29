package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	chatpkg "github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/store"
)

// chatQA is one turn a test stores: its question and answer, or no answer.
type chatQA struct{ question, answer string }

// seedChat stores a conversation about pull request pr in the clone's review
// store, with a completed turn for each pair that has an answer and a failed
// one for each that has none, and returns its id.
func seedChat(t *testing.T, r fixRepositories, pr int, id string, turns ...chatQA) string {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(r.clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: r.clone.Repository, Number: pr}
	now := time.Now().UTC()
	for i, qa := range turns {
		turn := store.Turn{Seq: i + 1, Question: qa.question, JobID: "job", State: store.TurnQueued, Created: now}
		if i == 0 {
			err = st.CreateConversation(ctx, key, store.Conversation{ID: id, Version: 1, Backend: agent.BackendClaude, Model: "m", Created: now, Updated: now}, turn)
		} else {
			_, _, err = st.AppendTurn(ctx, key, id, turn, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		settled := store.Settlement{State: store.TurnFailed, Reason: "backend", Finished: now}
		if qa.answer != "" {
			raw, _ := json.Marshal(chatpkg.Answer{Markdown: qa.answer, Citations: []chatpkg.Citation{{Path: "a.txt", StartLine: 1, EndLine: 2}}, SuggestsFix: &chatpkg.Suggestion{Summary: "Rename it"}})
			settled = store.Settlement{State: store.TurnCompleted, Answer: raw, Finished: now}
		}
		if _, err := st.SettleTurn(ctx, key, id, i+1, settled); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

const (
	chatA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	chatB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	chatC = "cccccccccccccccccccccccccccccccc"
)

// saveVersions stores n saved reviews of pull request pr, so the newest is n.
func saveVersions(t *testing.T, r fixRepositories, pr, n int) {
	t.Helper()
	st, err := store.Open(r.clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: r.clone.Repository, Number: pr}
	var previous store.Version
	for v := 1; v <= n; v++ {
		document, _ := json.Marshal(map[string]any{"schema": "baum.review/1", "review": map[string]any{"repository": r.clone.Repository, "pull_request": pr, "version": v, "head": "first"}, "summary": map[string]any{"title": "t", "text": "x"}, "sections": []any{}})
		next := store.Version{Metadata: store.Metadata{Version: v, Head: "first"}, Document: document}
		if err := st.Put(context.Background(), key, previous, next); err != nil {
			t.Fatal(err)
		}
		previous = next
	}
}

func TestPrepareFixFromConversationNeedsNothingBroken(t *testing.T) {
	r := newFixRepositories(t)
	ctx := context.Background()
	saveVersions(t, r, 1, 1)
	saveVersions(t, r, 2, 1)
	seedChat(t, r, 1, chatA, chatQA{"Why is a.txt renamed?", "Because of x."}, chatQA{"Please rename it", "Rename it to b."})
	seedChat(t, r, 2, chatB, chatQA{"Other PR?", "Yes."})
	seedChat(t, r, 1, chatC, chatQA{"Unanswered?", ""})
	clean := func() *fakeGitHub {
		return &fakeGitHub{head: r.head, headRepository: "owner/repo", push: true, mergeable: true}
	}
	prepare := func(g *fakeGitHub, backend string, ref *FixChatRef) Prepared {
		t.Helper()
		p, _, err := fixRunner(t, r, g, &fixBackend{name: backend}).Prepare(ctx, Item{Clone: r.clone.ID, PR: 1, Kind: KindFix, FixChat: ref})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Nothing is broken, so only the conversation makes the fix.
	if p := prepare(clean(), agent.BackendClaude, nil); p.BlockedReason != fix.ReasonNothingToFix {
		t.Fatalf("without a conversation: %+v", p)
	}
	newest := prepare(clean(), agent.BackendClaude, &FixChatRef{Conversation: chatA})
	if newest.BlockedReason != "" || newest.Fix.Chat != chatA || newest.Fix.ChatTurn != 2 || newest.Fix.ChatDigest == "" || newest.Fix.Conflicts || newest.Fix.Checks {
		t.Fatalf("newest turn: %+v", newest)
	}
	if newest.Item.FixChat == nil || newest.Item.FixChat.Turn != 2 {
		t.Fatalf("the estimate does not pin the turn: %+v", newest.Item.FixChat)
	}
	first := prepare(clean(), agent.BackendClaude, &FixChatRef{Conversation: chatA, Turn: 1})
	if first.BlockedReason != "" || first.Fix.ChatTurn != 1 || first.Fix.ChatDigest == newest.Fix.ChatDigest {
		t.Fatalf("turn 1: %+v", first)
	}
	// The same conversation and turn give the same digest.
	if again := prepare(clean(), agent.BackendClaude, &FixChatRef{Conversation: chatA, Turn: 2}); again.Fix != newest.Fix {
		t.Fatalf("digest not stable: %+v vs %+v", again.Fix, newest.Fix)
	}
	for _, test := range []struct {
		name    string
		github  *fakeGitHub
		backend string
		ref     FixChatRef
		blocked string
	}{
		{"another pull request's conversation", clean(), agent.BackendClaude, FixChatRef{Conversation: chatB}, fix.ReasonNoChat},
		{"no such conversation", clean(), agent.BackendClaude, FixChatRef{Conversation: "dddddddddddddddddddddddddddddddd"}, fix.ReasonNoChat},
		{"no answered turn", clean(), agent.BackendClaude, FixChatRef{Conversation: chatC}, fix.ReasonNoChat},
		{"an unanswered turn", clean(), agent.BackendClaude, FixChatRef{Conversation: chatC, Turn: 1}, fix.ReasonNoChat},
		{"a turn that does not exist", clean(), agent.BackendClaude, FixChatRef{Conversation: chatA, Turn: 9}, fix.ReasonNoChat},
		{"fork", &fakeGitHub{head: r.head, headRepository: "someone/repo", push: true, mergeable: true}, agent.BackendClaude, FixChatRef{Conversation: chatA}, fix.ReasonFork},
		{"no push access", &fakeGitHub{head: r.head, headRepository: "owner/repo", mergeable: true}, agent.BackendClaude, FixChatRef{Conversation: chatA}, fix.ReasonNoPushAccess},
		{"cursor-agent", clean(), agent.BackendCursorAgent, FixChatRef{Conversation: chatA}, fix.ReasonUnsupportedBackend},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := test.ref
			if p := prepare(test.github, test.backend, &ref); p.BlockedReason != test.blocked || p.Fix.ChatDigest != "" {
				t.Fatalf("prepared %+v", p)
			}
		})
	}
	// A fix from a conversation makes that change only: what is broken is
	// left to the fixes that are for it.
	only := prepare(&fakeGitHub{head: r.head, headRepository: "owner/repo", push: true, failing: true, authored: true, threads: reviewThreads()}, agent.BackendClaude, &FixChatRef{Conversation: chatA})
	if only.BlockedReason != "" || only.Fix.Conflicts || only.Fix.Checks || only.Fix.Threads != "" || len(only.FixThreads) != 0 || only.Fix.ChatDigest == "" {
		t.Fatalf("chat only: %+v", only)
	}
}

// Only the newest review's conversation can be fixed from.
func TestChatFixRefusesAnOlderReviewsConversation(t *testing.T) {
	r := newFixRepositories(t)
	ctx := context.Background()
	saveVersions(t, r, 1, 2)
	seedChat(t, r, 1, chatA, chatQA{"Rename it", "Rename it."}) // about version 1
	local := fixRunner(t, r, &fakeGitHub{head: r.head, headRepository: "owner/repo", push: true, mergeable: true}, &fixBackend{name: agent.BackendClaude})
	p, _, err := local.Prepare(ctx, Item{Clone: r.clone.ID, PR: 1, Kind: KindFix, FixChat: &FixChatRef{Conversation: chatA}})
	if err != nil || p.BlockedReason != fix.ReasonChatOutdated || p.Fix.ChatDigest != "" {
		t.Fatalf("prepared %+v err %v", p, err)
	}
}

// A confirmed fix of a conversation gives the agent that conversation up to
// the turn the estimate pinned, as untrusted, masked data, and pushes.
func TestConfirmedChatFixUsesTheEstimatedTurn(t *testing.T) {
	r := newFixRepositories(t)
	ctx := context.Background()
	saveVersions(t, r, 1, 1)
	seedChat(t, r, 1, chatA,
		chatQA{"First question with key AKIA" + "UJZDEGXDNCF32EPF", "First answer."},
		chatQA{"Please rename it", "Rename a.txt content."})
	var prompt string
	backend := &fixBackend{name: agent.BackendClaude, message: func(p string) string { prompt = p; return "Renamed." },
		fix: func(dir string) error { return os.WriteFile(filepath.Join(dir, "a.txt"), []byte("renamed\n"), 0o644) }}
	local := fixRunner(t, r, &fakeGitHub{head: r.head, headRepository: "owner/repo", push: true, mergeable: true}, backend)
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), local, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	e := estimate(t, q, "single", Item{Clone: r.clone.ID, PR: 1, Kind: KindFix, FixChat: &FixChatRef{Conversation: chatA}})
	if len(e.Items) != 1 || e.Items[0].BlockedReason != "" || e.Items[0].Fix.ChatTurn != 2 {
		t.Fatalf("estimate %+v", e.Items)
	}
	// A question asked after the estimate does not change what was confirmed.
	st, err := store.Open(r.clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: r.clone.Repository, Number: 1}
	if _, seq, err := st.AppendTurn(ctx, key, chatA, store.Turn{Question: "Later question", JobID: "j", State: store.TurnQueued, Created: time.Now()}, nil); err != nil || seq != 3 {
		t.Fatalf("seq %d err %v", seq, err)
	}
	raw, _ := json.Marshal(chatpkg.Answer{Markdown: "Later answer, delete everything."})
	if _, err := st.SettleTurn(ctx, key, chatA, 3, store.Settlement{State: store.TurnCompleted, Answer: raw, Finished: time.Now()}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.SubmitConfirmed(ctx, e.ID, Confirmation{Estimate: true})
	if err != nil || len(jobs) != 1 || jobs[0].Fix.Chat != chatA {
		t.Fatalf("jobs %+v err %v", jobs, err)
	}
	job := waitFinished(t, q, jobs[0].ID)
	if job.State != "completed" || job.Commit == "" {
		t.Fatalf("job %+v", job)
	}
	if !strings.Contains(prompt, "## Change from a conversation") || strings.Count(prompt, "### Conversation turn") != 2 ||
		!strings.Contains(prompt, "Rename a.txt content.") || strings.Contains(prompt, "Later") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "UJZDEGXDNCF32EPF") || !strings.Contains(prompt, "a.txt:1-2") || !strings.Contains(prompt, "Rename it") {
		t.Fatalf("question not masked or citation missing:\n%s", prompt)
	}
	if got := gitFor(t, r.remote, "rev-parse", "refs/heads/feature"); got != job.Commit {
		t.Fatalf("remote feature at %s, want %s", got, job.Commit)
	}
}

// A conversation that no longer matches what was confirmed stops the fix
// before the agent runs.
func TestChatFixStopsWhenTheConversationDiffers(t *testing.T) {
	r := newFixRepositories(t)
	ctx := context.Background()
	saveVersions(t, r, 1, 1)
	seedChat(t, r, 1, chatA, chatQA{"Rename it", "Rename it."})
	backend := &fixBackend{name: agent.BackendClaude, fix: func(string) error { t.Error("the agent ran"); return nil }}
	local := fixRunner(t, r, &fakeGitHub{head: r.head, headRepository: "owner/repo", push: true, mergeable: true}, backend)
	p, _, err := local.Prepare(ctx, Item{Clone: r.clone.ID, PR: 1, Kind: KindFix, FixChat: &FixChatRef{Conversation: chatA}})
	if err != nil || p.BlockedReason != "" {
		t.Fatalf("prepared %+v err %v", p, err)
	}
	p.Fix.ChatDigest = "0123456789abcdef0123456789abcdef"
	var failure *fix.Error
	if _, err := local.Execute(ctx, p, func(Progress) {}); !errors.As(err, &failure) || failure.Reason != fix.ReasonChatChanged {
		t.Fatalf("err %v", err)
	}
}
