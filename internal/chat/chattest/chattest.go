// Package chattest builds a registered clone with a saved review whose agent
// session was kept, and a scripted agent, for tests of chats.
package chattest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewruns"
	"github.com/baum-ai/baum/internal/store"
)

// Options vary the fixture.
type Options struct {
	// Backend is the review's harness; empty is claude.
	Backend string
	// NoSession stores the review without a kept session.
	NoSession bool
}

// Fixture is a registered clone of acme/demo with review version 1 of pull
// request 7 saved, written by Backend, whose session SessionID was kept in
// ReviewDir under Root. ReviewDir's head/ holds README.md (one line) and
// src/app.go (five lines). The clone's pull request head holds README.md
// too, so a seeded session can cite it.
type Fixture struct {
	Registry  *repos.Registry
	Clone     repos.Clone
	Store     *store.Store
	Key       store.Key
	Root      string
	ReviewDir string
	SessionID string
	Head      string
	Agent     *Agent
	Service   *chat.Service
}

// SessionID is the n-th session id the fake agent hands out.
func SessionID(n int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n) }

// Answer is an answer document.
func Answer(markdown string, citations ...chat.Citation) []byte {
	if citations == nil {
		citations = []chat.Citation{}
	}
	raw, err := json.Marshal(chat.Answer{Markdown: markdown, Citations: citations})
	if err != nil {
		panic(err)
	}
	return raw
}

// Agent is a scripted Claude Code or Codex: Reply, when set, answers each
// request (n counts from 0); by default it answers "ok" citing README.md line
// 1, hands a forked or new session a fresh id and a resumed one its own.
type Agent struct {
	generatetest.Backend
	mu    sync.Mutex
	next  int
	Reply func(request agent.Request, n int) (agent.Result, error)
}

// Default is the default reply to request.
func (a *Agent) Default(request agent.Request) agent.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	session := request.ResumeSession
	if session == "" || request.ForkSession {
		a.next++
		session = SessionID(100 + a.next)
	}
	request.Emit(agent.TranscriptEntry{Kind: "tool", ID: "t1", Tool: "Read", Title: "Read README.md", Status: "running"})
	request.Emit(agent.TranscriptEntry{Kind: "message", Text: "Reading the code."})
	return agent.Result{Document: Answer("ok", chat.Citation{Path: "README.md", StartLine: 1, EndLine: 1}), SessionID: session, Calls: 1, Tokens: 10,
		Model: "claude-sonnet-4-5", Usage: agent.Usage{Input: 8, Output: 2}, UsageAttempts: []agent.UsageAttempt{{Model: "claude-sonnet-4-5", Usage: agent.Usage{Input: 8, Output: 2}, Calls: 1, ChargedTokens: 10}}}
}

// New builds the fixture. It points CLAUDE_CONFIG_DIR and CODEX_HOME at
// empty directories.
func New(t *testing.T, o Options) *Fixture {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	ctx := context.Background()
	backend := o.Backend
	if backend == "" {
		backend = agent.BackendClaude
	}
	repo := assembletest.NewRepo(t, assembletest.Options{})
	remote := exec.Command("git", "remote", "add", "origin", "https://github.com/acme/demo")
	remote.Dir = repo.Dir
	if out, err := remote.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	clone, err := registry.AddFor(ctx, repo.Dir, "acme/demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "review-runs")
	t.Cleanup(func() { chmodTree(root) })
	dir, err := reviewruns.Create(root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "head", "README.md"), "# demo\n")
	write(t, filepath.Join(dir, "head", "src", "app.go"), "package app\n\nfunc A() {}\n\nfunc B() {}\n")
	st, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: clone.Repository, Number: assembletest.Number}
	f := &Fixture{Registry: registry, Clone: clone, Store: st, Key: key, Root: root, ReviewDir: dir, SessionID: SessionID(1), Head: repo.Head}
	metadata := store.Metadata{Version: 1, Head: repo.Head, Backend: backend, Model: "claude-sonnet-4-5", GeneratedAt: "2026-10-01T00:00:00Z"}
	if !o.NoSession {
		metadata.Session = &store.ReviewSession{Backend: backend, ID: f.SessionID, Directory: dir}
	}
	document := fmt.Sprintf(`{"review":{"repository":"acme/demo","pull_request":%d,"version":1,"head":%q}}`, assembletest.Number, repo.Head)
	if err := st.Put(ctx, key, store.Version{}, store.Version{Metadata: metadata, Document: json.RawMessage(document)}); err != nil {
		t.Fatal(err)
	}
	f.Agent = &Agent{}
	f.Agent.BackendName = backend
	f.Agent.Version = "9.9.9"
	f.Agent.RunFunc = func(_ context.Context, request agent.Request, n int) (agent.Result, error) {
		if f.Agent.Reply != nil {
			return f.Agent.Reply(request, n)
		}
		return f.Agent.Default(request), nil
	}
	build := func(string) generate.CheckedBackend { return f.Agent }
	cfg := config.Default()
	cfg.Budgets.MaxCalls, cfg.Budgets.MaxTokens, cfg.Budgets.RunTimeout = 20, 100000, config.Duration(10*time.Minute)
	f.Service = &chat.Service{Registry: registry, Config: cfg, Root: root, RetryDelay: func(int) time.Duration { return 0 },
		Backends: generate.Backends{Claude: build, Codex: build, CursorAgent: build, Anthropic: build}}
	return f
}

// Ask stores a question as a turn of a new conversation (conversation
// empty) or of conversation, failing the test on an error.
func (f *Fixture) Ask(t *testing.T, conversation, question string) chat.Begun {
	t.Helper()
	begun, err := f.Service.Begin(context.Background(), chat.Ask{Clone: f.Clone.ID, PR: assembletest.Number, Version: 1, Conversation: conversation, Question: question}, fmt.Sprintf("job-%d", time.Now().UnixNano()), func(string) (string, string, bool) { return "", "", false })
	if err != nil {
		t.Fatal(err)
	}
	return begun
}

// Conversation reads a conversation back.
func (f *Fixture) Conversation(t *testing.T, id string) store.Conversation {
	t.Helper()
	c, err := f.Store.Conversation(context.Background(), f.Key, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// chmodTree makes the read-only directories of an unpacked checkout
// writable, so the test's temporary directory can be removed.
func chmodTree(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}
