//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
)

const testSession = "6c2a4f1e-0b7d-4c55-9a7e-2f3b1d0c8e91"

// fakeClaude is a Claude Code that answers --version and otherwise records
// its arguments, working directory and environment beside itself, then
// answers as a session would. A prompt containing "wait" makes it start a
// child that sleeps and wait for it, writing the child's pid first, so a
// turn runs until it is stopped.
const fakeClaude = `#!/bin/sh
if [ "$1" = "--version" ]; then echo '2.1.300 (Claude Code)'; exit 0; fi
log=$(dirname "$0")
prompt=$(cat)
printf '%s\n' "$@" > "$log/arguments.txt"
pwd > "$log/pwd.txt"
env > "$log/environment.txt"
case "$*" in
*--resume*)
  if [ -f "$log/gone" ]; then
    echo '{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"errors":["No conversation found with session ID: ` + testSession + `"]}'
    exit 1
  fi;;
esac
echo '{"type":"system","subtype":"init","session_id":"` + testSession + `"}'
case "$prompt" in
*wait*)
  sleep 60 &
  echo $! > "$log/child.pid"
  wait
  exit 1;;
esac
cat <<'EVENTS'
{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"fixture","usage":{"input_tokens":3,"output_tokens":1}}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}
{"type":"assistant","message":{"id":"m1","model":"fixture","content":[{"type":"text","text":"Hello"}],"usage":{"input_tokens":3,"output_tokens":2}}}
{"type":"result","subtype":"success","result":"Hello","session_id":"` + testSession + `","usage":{"input_tokens":3,"output_tokens":2}}
EVENTS
`

type fixture struct {
	t       *testing.T
	service *Service
	opts    Options
	log     string
	events  chan Event
	limit   time.Duration
}

func newFixture(t *testing.T, change func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, log: t.TempDir(), events: make(chan Event, 256), limit: time.Minute}
	script := filepath.Join(f.log, "claude")
	if err := os.WriteFile(script, []byte(fakeClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	f.opts = Options{
		Database: filepath.Join(appdbtest.TempDir(t), "app.sqlite"),
		Root:     t.TempDir(),
		Registry: agentcontext.NewRegistry(),
		MCPURL:   func() string { return "http://127.0.0.1:43123/api/mcp" },
		Config: func(context.Context) (config.Config, error) {
			var cfg config.Config
			cfg.BackendPaths = map[config.Backend]string{config.BackendClaude: script}
			cfg.Budgets.RunTimeout = config.Duration(f.limit)
			return cfg, nil
		},
		Price: func(string, *agent.Accounting) (*float64, string) {
			cost := 0.25
			return &cost, ""
		},
		Publish: func(e Event) { f.events <- e },
	}
	if change != nil {
		change(&f.opts)
	}
	f.open()
	return f
}

func (f *fixture) open() {
	f.t.Helper()
	service, err := New(f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.service = service
	f.t.Cleanup(func() { _ = service.CloseStore() })
}

// liveOf returns the conversation's live state.
func (f *fixture) liveOf(id string) *live {
	f.service.mu.Lock()
	defer f.service.mu.Unlock()
	return f.service.live[id]
}

// finished waits for the end of the conversation's turn.
func (f *fixture) finished(id string) Event {
	f.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case e := <-f.events:
			if e.Conversation == id && e.Kind == EventFinished {
				return e
			}
		case <-timeout:
			f.t.Fatal("the turn did not finish")
		}
	}
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.log, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

// waitFor waits until the agent wrote name.
func (f *fixture) waitFor(name string) string {
	f.t.Helper()
	for range 600 {
		if body, err := os.ReadFile(filepath.Join(f.log, name)); err == nil && len(strings.TrimSpace(string(body))) > 0 {
			return strings.TrimSpace(string(body))
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatalf("the agent never wrote %s", name)
	return ""
}

// A conversation opened outside a pull request gets an empty directory of its
// own; its first turn runs the agent there with baum's MCP server, records the
// session the agent reported, and the next turn resumes it.
func TestConversationRunsAndResumes(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	page := agentcontext.PageContext{Page: agentcontext.PageInbox}
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode, Model: new("opus"), Page: &page})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeAuto || c.State != StateIdle || c.Repository != "" || c.CreatedWorktree {
		t.Fatalf("created %+v", c)
	}
	if c.Directory != filepath.Join(f.opts.Root, "sessions", c.ID) || strings.Contains(c.Directory, agent.RunDirPrefix) {
		t.Fatalf("directory = %s", c.Directory)
	}
	if entries, err := os.ReadDir(c.Directory); err != nil || len(entries) != 0 {
		t.Fatalf("the directory is not new and empty: %v %v", entries, err)
	}
	if _, err := f.service.Send(ctx, c.ID, "  hello there\nsecond line ", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "again", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("a second message while running: %v", err)
	}
	finished := f.finished(c.ID)
	if finished.State != StateIdle || finished.CostUSD == nil || *finished.CostUSD != 0.25 {
		t.Fatalf("finished %+v", finished)
	}
	got, err := f.service.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateIdle || got.Error != "" || got.Session != testSession || got.Title != "hello there" || got.CostUSD == nil || *got.CostUSD != 0.25 || got.CostPartial {
		t.Fatalf("after the turn %+v", got)
	}
	progress, ok := f.service.Progress(c.ID)
	if !ok || progress.Text != "Hello" || progress.Session != testSession || progress.State != StateIdle {
		t.Fatalf("progress %+v", progress)
	}
	if pwd := f.read("pwd.txt"); pwd != c.Directory {
		t.Fatalf("the agent ran in %s", pwd)
	}
	args := strings.Split(f.read("arguments.txt"), "\n")
	if !slices.Contains(args, "auto") || !slices.Contains(args, "opus") || slices.Contains(args, "--resume") {
		t.Fatalf("first turn arguments %q", args)
	}
	// The token the agent got is the conversation's, bound to its session.
	l := f.liveOf(c.ID)
	env := f.read("environment.txt")
	if !strings.Contains(env, agent.MCPTokenEnv+"="+l.token+"\n") {
		t.Fatal("the agent did not get the conversation's MCP token")
	}
	consumer, ok := f.opts.Registry.Authenticate(l.token)
	if !ok || consumer.Kind != agentcontext.Chat || consumer.Session != agentcontext.SessionKey(AgentClaudeCode, testSession) {
		t.Fatalf("consumer %+v %v", consumer, ok)
	}
	// The page it was opened from is the token's page.
	if page, ok := f.opts.Registry.Page(l.consumer); !ok || page.Page != agentcontext.PageInbox {
		t.Fatalf("the token's page is %+v %v", page, ok)
	}

	if _, err := f.service.Send(ctx, c.ID, "and now?", &agentcontext.PageContext{Page: agentcontext.PageJobs}); err != nil {
		t.Fatal(err)
	}
	f.finished(c.ID)
	args = strings.Split(f.read("arguments.txt"), "\n")
	if at := slices.Index(args, "--resume"); at < 0 || args[at+1] != testSession {
		t.Fatalf("follow-up arguments %q", args)
	}
	got, _ = f.service.Get(ctx, c.ID)
	if got.Title != "hello there" || *got.CostUSD != 0.5 || len(got.Pages) != 2 || got.Pages[1].Page != agentcontext.PageJobs {
		t.Fatalf("after the second turn %+v", got)
	}
}

// Conversations survive a restart, and one a stopped daemon left running
// comes back failed with the reason.
func TestConversationsSurviveARestart(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentCodex, Mode: ModeWorkspace, Model: new("gpt-5")})
	if err != nil {
		t.Fatal(err)
	}
	running := c
	running.ID, running.State, running.Session, running.Title = newID(), StateRunning, testSession, "a running one"
	running.Pages = []agentcontext.PageContext{{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}}
	if err := f.service.store.save(running); err != nil {
		t.Fatal(err)
	}
	if err := f.service.CloseStore(); err != nil {
		t.Fatal(err)
	}
	f.open()
	list, err := f.service.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	got, err := f.service.Get(ctx, c.ID)
	if err != nil || got.Agent != AgentCodex || got.Mode != ModeWorkspace || got.Model != "gpt-5" || got.State != StateIdle || got.Directory != c.Directory {
		t.Fatalf("idle one %+v %v", got, err)
	}
	got, err = f.service.Get(ctx, running.ID)
	if err != nil || got.State != StateFailed || got.Error != ReasonRestarted || got.Session != testSession || got.Title != "a running one" ||
		len(got.Pages) != 1 || got.Pages[0].PullRequest != 7 {
		t.Fatalf("running one %+v %v", got, err)
	}
	if _, err := f.service.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// alive reports whether the process runs: a zombie waiting for its parent to
// reap it has ended.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) == 0 || fields[0] != "Z"
}

func gone(t *testing.T, pid int) {
	t.Helper()
	for range 200 {
		if !alive(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d outlived its turn", pid)
}

// Stopping a turn kills the agent's whole process group, a child it started
// included, and leaves the conversation idle with the reason.
func TestStopKillsTheAgentsProcessGroup(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "wait for it", nil); err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(f.waitFor("child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	gone(t, child)
	got, _ := f.service.Get(ctx, c.ID)
	if got.State != StateIdle || got.Error != ReasonStopped || got.Session != testSession {
		t.Fatalf("after stop %+v", got)
	}
	if err := f.service.Stop(ctx, c.ID); err != nil {
		t.Errorf("stopping an idle conversation: %v", err)
	}
}

// A turn longer than the run time limit is stopped and fails naming it.
func TestATurnIsBoundedByTheRunTimeLimit(t *testing.T) {
	f := newFixture(t, nil)
	f.limit = 500 * time.Millisecond
	ctx := context.Background()
	c, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, c.ID, "wait for it", nil); err != nil {
		t.Fatal(err)
	}
	if e := f.finished(c.ID); e.State != StateFailed {
		t.Fatalf("finished %+v", e)
	}
	child, _ := strconv.Atoi(f.read("child.pid"))
	gone(t, child)
	got, _ := f.service.Get(ctx, c.ID)
	if got.State != StateFailed || !strings.Contains(got.Error, "run time limit") {
		t.Fatalf("after the limit %+v", got)
	}
}

// Each conversation holds one chat token from creation: the page set for all
// reaches it, closing revokes it and refuses further messages, and shutting
// down stops running turns and revokes the rest.
func TestTokenLifecycle(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	one, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	two, err := f.service.Create(ctx, CreateRequest{Agent: AgentCodex})
	if err != nil {
		t.Fatal(err)
	}
	first, second := f.liveOf(one.ID), f.liveOf(two.ID)
	if first == nil || second == nil || first.consumer == second.consumer {
		t.Fatal("each conversation needs its own token")
	}
	for _, l := range []*live{first, second} {
		if consumer, ok := f.opts.Registry.Consumer(l.consumer); !ok || consumer.Kind != agentcontext.Chat || consumer.Session != "" {
			t.Fatalf("consumer %+v %v", consumer, ok)
		}
	}
	page := agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 3}
	if err := f.service.SetPage(page); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*live{first, second} {
		if got, ok := f.opts.Registry.Page(l.consumer); !ok || got.PullRequest != 3 {
			t.Fatalf("page %+v %v", got, ok)
		}
	}
	if err := f.service.SetPage(agentcontext.PageContext{Page: "nowhere"}); err == nil {
		t.Error("an unknown page was accepted")
	}

	closed, err := f.service.Close(ctx, one.ID)
	if err != nil || closed.State != StateClosed {
		t.Fatalf("closed %+v %v", closed, err)
	}
	if _, ok := f.opts.Registry.Consumer(first.consumer); ok {
		t.Error("closing kept the token")
	}
	if _, err := f.service.Send(ctx, one.ID, "hello", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("a message to a closed conversation: %v", err)
	}

	three, err := f.service.Create(ctx, CreateRequest{Agent: AgentClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := f.opts.Registry.Page(f.liveOf(three.ID).consumer); !ok || got.PullRequest != 3 {
		t.Fatalf("a later token did not get the page: %+v %v", got, ok)
	}
	if _, err := f.service.Send(ctx, three.ID, "wait for it", nil); err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(f.waitFor("child.pid"))
	third := f.liveOf(three.ID)
	f.service.Shutdown()
	gone(t, child)
	got, _ := f.service.Get(ctx, three.ID)
	if got.State != StateFailed || got.Error != ReasonStopping {
		t.Fatalf("after shutdown %+v", got)
	}
	for _, l := range []*live{second, third} {
		if _, ok := f.opts.Registry.Consumer(l.consumer); ok {
			t.Error("shutdown kept a token")
		}
	}
	if _, err := f.service.Send(ctx, two.ID, "hello", nil); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("a message after shutdown: %v", err)
	}
}

// Text streams to the app at most every textInterval, and the app reads it
// from Progress.
func TestTextIsThrottled(t *testing.T) {
	var mu sync.Mutex
	var published []Event
	now := time.Unix(1000, 0)
	f := newFixture(t, func(o *Options) {
		o.Publish = func(e Event) {
			mu.Lock()
			published = append(published, e)
			mu.Unlock()
		}
		o.Now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}
	})
	l := &live{}
	f.service.textDelta("c", l, "a")
	f.service.textDelta("c", l, "b")
	f.service.transcript("c", l, agent.TranscriptEntry{Kind: "message", Text: "ab"})
	f.service.textDelta("c", l, "c")
	mu.Lock()
	count := len(published)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("published %d events at once, want 1", count)
	}
	if l.progress.Text != "ab" || l.progress.streaming != "c" {
		t.Fatalf("progress %+v", l.progress)
	}
	time.Sleep(2 * textInterval)
	mu.Lock()
	count = len(published)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("the held text was not published: %d events", count)
	}
}
