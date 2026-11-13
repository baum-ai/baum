package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/focus"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubrepos"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxqueue"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/sessions/session"
)

// The operations a chat proposes and the person confirms, run against the
// daemon's real routes: the registry, review groups, the jobs queue (with an
// executor that runs nothing), the agent requests store and the live
// sessions handler (with senders, enders and a focuser that record what they
// were asked) are real. The routes that call GitHub (merge, update branch,
// rerun checks and a failed job, resolve a thread, refresh) are the real
// handlers over a recording GitHub fake; the clone route is the real one
// with a recording clone function.

// writeLog is the LocalAPI handler with every request that is not a GET
// recorded: what the operations ran.
type writeLog struct {
	next   http.Handler
	mu     sync.Mutex
	writes []string
	raw    []string
	// failSettings fails GET /api/settings, as a settings read that errs.
	failSettings atomic.Bool
}

func (l *writeLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/settings" && l.failSettings.Load() {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		l.mu.Lock()
		l.writes = append(l.writes, r.Method+" "+r.URL.Path)
		l.raw = append(l.raw, r.URL.RawPath)
		l.mu.Unlock()
	}
	l.next.ServeHTTP(w, r)
}

func (l *writeLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.writes
	l.writes, l.raw = nil, nil
	return out
}

func (l *writeLog) rawPaths() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.raw)
}

// record is a list of what a fake was asked, safe for concurrent use.
type record struct {
	mu    sync.Mutex
	calls []string
}

func (r *record) add(call string) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
}

func (r *record) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

func (r *record) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// opGitHub is GitHub as the merge, update and discovery handlers reach it.
type opGitHub struct {
	record
	mu       sync.Mutex
	mergeErr error
}

func (g *opGitHub) MergeOptions(context.Context, github.Repository, int) (github.MergeOptions, error) {
	return github.MergeOptions{Methods: []string{"MERGE", "SQUASH"}, Default: "SQUASH", State: "OPEN", HeadSHA: "abc"}, nil
}

func (g *opGitHub) MergePullRequest(_ context.Context, r github.Repository, number int, method, head string) error {
	g.mu.Lock()
	err := g.mergeErr
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.add(fmt.Sprintf("merge %s#%d %s %s", r, number, method, head))
	return nil
}

func (g *opGitHub) UpdateOptions(context.Context, github.Repository, int) (prupdate.UpdateOptions, error) {
	return prupdate.UpdateOptions{State: "OPEN", HeadSHA: "abc", HeadRef: "search", BaseRef: "main", Behind: true}, nil
}
func (g *opGitHub) UpdateBranch(_ context.Context, r github.Repository, number int, head string) error {
	g.add(fmt.Sprintf("update %s#%d %s", r, number, head))
	return nil
}
func (g *opGitHub) RerunOptions(context.Context, github.Repository, int) (prupdate.RerunOptions, error) {
	return prupdate.RerunOptions{State: "OPEN", HeadSHA: "abc", Runs: []prupdate.Run{{ID: 11, Name: "CI"}}}, nil
}
func (g *opGitHub) RerunFailedChecks(_ context.Context, r github.Repository, number int, head string) (prupdate.RerunResult, error) {
	g.add(fmt.Sprintf("rerun checks %s#%d %s", r, number, head))
	return prupdate.RerunResult{Rerun: []prupdate.Run{{ID: 11, Name: "CI"}}}, nil
}
func (g *opGitHub) ReviewThreads(context.Context, github.Repository, int, int) (prupdate.Threads, error) {
	return prupdate.Threads{Threads: []prupdate.Thread{
		{ID: "RT_1", Path: "search.go", Line: 3, Author: "alice", Excerpt: "Rename this.", Comments: 1, CanResolve: true},
		{ID: "RT_2", Path: "docs.md", Author: "bob", Excerpt: "Not yours.", Comments: 1},
	}}, nil
}
func (g *opGitHub) ResolveThread(_ context.Context, r github.Repository, number int, id string) error {
	g.add(fmt.Sprintf("resolve %s#%d %s", r, number, id))
	return nil
}
func (g *opGitHub) FailingChecks(context.Context, github.Repository, int, string) (prupdate.Checks, error) {
	return prupdate.Checks{HeadSHA: "abc", Checks: []prupdate.FailingCheck{{Name: "test", Conclusion: "failure", JobID: 21}}}, nil
}
func (g *opGitHub) RerunJob(_ context.Context, r github.Repository, number int, head string, job int64) error {
	g.add(fmt.Sprintf("rerun job %s#%d %s %d", r, number, head, job))
	return nil
}
func (g *opGitHub) AllChecks(context.Context, github.Repository, int, string) (prupdate.CheckList, error) {
	return prupdate.CheckList{}, nil
}
func (g *opGitHub) Conflicts(context.Context, github.Repository, int) (prupdate.ConflictFiles, error) {
	return prupdate.ConflictFiles{}, nil
}
func (g *opGitHub) Description(context.Context, github.Repository, int) (prupdate.Description, error) {
	return prupdate.Description{}, nil
}
func (g *opGitHub) Cached(context.Context, github.Repository) (discovery.Snapshot, error) {
	return discovery.Snapshot{}, nil
}
func (g *opGitHub) PollNow(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	g.add("poll " + r.String())
	return discovery.Snapshot{}, nil
}

// opExecutor prepares every job as the fixture's repository and runs
// nothing: a started job stays running until it is cancelled. A review of
// #8 needs the person's isolation confirmation, as one from a fork does.
type opExecutor struct{ clone string }

func (e opExecutor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	if item.Clone != e.clone {
		return jobs.Prepared{}, "", repos.ErrNotFound
	}
	return jobs.Prepared{PriceVersion: "2026-10-01", Item: item, Repository: "owner/repo", HeadSHA: "abc", Backend: "claude", Model: "claude-test",
		RequiresIsolationConfirmation: item.Kind == "" && item.PR == 8}, "2026-10-01", nil
}

func (e opExecutor) Execute(ctx context.Context, _ jobs.Prepared, _ func(jobs.Progress)) (*float64, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// opFocus brings every session forward and says it could.
type opFocus struct{ record }

func (f *opFocus) Predict(string, *hookevents.Terminal) string { return "session" }
func (f *opFocus) Focus(_ context.Context, agent, id string, _ *hookevents.Terminal) focus.Result {
	f.add("focus " + agent + ":" + id)
	return focus.Result{Precision: "session"}
}

type opFixture struct {
	t        *testing.T
	registry *repos.Registry
	// registryPath is the registry's database, for a test that damages it.
	registryPath string
	clone        repos.Clone
	home         string
	log          *writeLog
	mcp          *agentcontext.Server
	reads        *readFixture
	github       *opGitHub
	queue        *jobs.Queue
	requests     *agentrequests.Store
	groups       *reviewgroups.Store
	group        string
	// sent, ended and focused are what the live sessions' agents were
	// asked; cloned the clones started.
	sent, ended, cloned record
	focus               *opFocus
	token, id           string
	revoke              func()
	request             string
	// index is the review index the routes serve; setTitle changes it.
	indexMu sync.Mutex
	index   reviewindex.Snapshot
}

func newOpFixture(t *testing.T) *opFixture {
	t.Helper()
	ctx := context.Background()
	registryPath := filepath.Join(appdbtest.TempDir(t), "repos.sqlite")
	registry, clone := readRegistryAt(t, registryPath)
	if _, err := registry.Pick(ctx, "owner/repo", nil); err != nil {
		t.Fatal(err)
	}
	dir := appdbtest.TempDir(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &opFixture{t: t, registry: registry, registryPath: registryPath, clone: clone, home: home, github: &opGitHub{}, focus: &opFocus{}}

	// The review index: two pull requests of owner/repo, the first asked of
	// the person, and one of a repository with no clone.
	f.index = reviewindex.Snapshot{Entries: []reviewindex.Entry{
		{PullRequest: discovery.PullRequest{Repository: "owner/repo", Number: 7, HeadSHA: "abc", Title: "Add search", Author: "ada", ReviewRequestedFromMe: true}, CloneID: clone.ID, Status: "none"},
		{PullRequest: discovery.PullRequest{Repository: "owner/repo", Number: 8, HeadSHA: "abc", Title: "Fork change", Author: "eve", ReviewRequestedFromMe: true}, CloneID: clone.ID, Status: "none"},
		{PullRequest: discovery.PullRequest{Repository: "other/repo", Number: 9, HeadSHA: "def", Title: "Elsewhere", ReviewRequestedFromMe: true}, NeedsClone: true, Status: "none"},
	}}
	snapshot := func(context.Context) (reviewindex.Snapshot, error) {
		f.indexMu.Lock()
		defer f.indexMu.Unlock()
		return f.index, nil
	}

	f.groups, err = reviewgroups.Open(filepath.Join(dir, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.groups.Close() })
	g, err := f.groups.Create(ctx, "owner/repo", "Search", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	f.group = g.ID
	groupsAPI := &reviewgroups.API{Store: f.groups, Registry: registry, Index: snapshot, Changed: func() {}}
	t.Cleanup(groupsAPI.Close)

	jobsDir := t.TempDir()
	if err := os.Chmod(jobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.queue, err = jobs.Open(filepath.Join(jobsDir, "jobs.json"), opExecutor{clone: clone.ID}, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.queue.Close)

	// Two Claude Code sessions: s1 runs a turn; s2 finished its turn and
	// waits at its prompt.
	path := filepath.Join(home, ".claude", "projects", "-work-demo", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(claudeTurn("s1", 1, "Add search", "Searching.", false)), 0o600); err != nil {
		t.Fatal(err)
	}
	now := fixtureTime.Add(10 * time.Minute)
	tracker := &livesessions.Tracker{Now: func() time.Time { return now }, Linker: tableLinker{"/work/demo": {Repository: "owner/repo", Branch: "search"}}}
	for _, ev := range []struct{ id, name string }{{"s1", "user-prompt-submit"}, {"s2", "user-prompt-submit"}, {"s2", "stop"}} {
		e := hookevents.New(hookevents.KindAgent, "claude-code", ev.name, "/work/demo", "test")
		e.Time = fixtureTime
		e.Session = &hookevents.Session{ID: ev.id, Cwd: "/work/demo", TranscriptPath: path}
		e.Terminal = &hookevents.Terminal{TTY: "/dev/pts/3"}
		tracker.Apply(ctx, e)
		tracker.ResolvePending(ctx)
	}
	renderer := transcript.NewRenderer(home)
	transcripts := &livesessions.Transcripts{Tracker: tracker, Home: home, Now: func() time.Time { return now },
		Render: func(ctx context.Context, graph *session.Graph, meta transcript.Meta) ([]byte, error) {
			return renderer.Render(ctx, graph, meta)
		},
		Senders: map[string]livesessions.Sender{"claude-code": func(_ context.Context, id, text string) error {
			f.sent.add(id + ": " + text)
			return nil
		}},
		Enders: map[string]livesessions.Ender{"claude-code": func(_ context.Context, id string) error {
			f.ended.add(id)
			return nil
		}},
		Focus: f.focus,
	}

	// One pending permission request, of s1. The store masks a planted
	// secret, as it masks what agents write.
	f.requests = &agentrequests.Store{Sessions: tracker, Database: filepath.Join(dir, "requests.sqlite"),
		Mask: func(text string) string { return strings.ReplaceAll(text, "SECRET-TOKEN", "[masked]") }}
	f.request = f.ask(agentrequests.Ask{Agent: "claude-code", SessionID: "s1", Kind: agentrequests.KindPermission, Tool: "Bash",
		Input: []agentrequests.Row{{Label: "Command", Value: longCommand, Code: true}}, Suggestions: []string{"Always allow go test"}})

	inbox := &inboxqueue.Service{Index: snapshot, Jobs: func() []jobs.Job { return nil }, Groups: f.groups.List,
		Snoozes: func() (json.RawMessage, error) { return nil, nil }}
	clones := &githubrepos.Service{Registry: registry, Home: home, Token: func(context.Context) (string, error) { return "", nil },
		Clone: func(_ context.Context, opts gitlocal.CloneOptions) error {
			f.cloned.add(opts.Dir)
			return errors.New("no network in tests")
		}}
	api := uiserver.LocalAPI{Registry: registry, Config: config.Default(), Index: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index, _ := snapshot(r.Context())
		readJSONHandler(index).ServeHTTP(w, r)
	}), Inbox: inbox.Handler(),
		Merge:  uiserver.MergeHandler(registry, f.github, nil, nil),
		Update: uiserver.UpdateHandler(registry, f.github, nil, nil),
		PRs:    uiserver.DiscoveryHandler(registry, f.github, nil),
		Jobs:   f.queue.Handler(), Live: transcripts.Handler(), AgentRequests: f.requests.Handler(),
		Groups: groupsAPI.Handler(), GitHubRepositories: clones.Handler()}.Handler()
	t.Cleanup(api.Close)
	f.log = &writeLog{next: api}
	f.mcp = agentcontext.New(agentcontext.Options{API: f.log, Version: "test"})
	f.reads = &readFixture{mcp: f.mcp}
	f.token, f.id, f.revoke, err = f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat, Session: agentcontext.SessionKey("claude-code", "chat")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.revoke)
	return f
}

// longCommand is the pending request's input value: as long as the store
// keeps (4,000 characters, most of them two bytes), with a tail of its own.
var longCommand = "go test " + strings.Repeat("ü", 4000-8-len("TAIL-END")) + "TAIL-END"

// setTitle changes a pull request's title in the review index.
func (f *opFixture) setTitle(number int, title string) {
	f.indexMu.Lock()
	defer f.indexMu.Unlock()
	for i := range f.index.Entries {
		if f.index.Entries[i].PullRequest.Repository == "owner/repo" && f.index.Entries[i].PullRequest.Number == number {
			f.index.Entries[i].PullRequest.Title = title
		}
	}
}

func (f *opFixture) page(p agentcontext.PageContext) {
	f.t.Helper()
	if err := f.mcp.Registry.SetPage(f.id, p); err != nil {
		f.t.Fatal(err)
	}
}

// propose calls an operation's tool and returns its receipt, or its error's
// text.
func (f *opFixture) propose(tool string, args any) (map[string]any, string) {
	f.t.Helper()
	text, failed := f.reads.callTool(f.t, f.token, tool, args)
	if failed {
		return nil, text
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(text), &receipt); err != nil {
		f.t.Fatalf("%s answered %s", tool, text)
	}
	return receipt, ""
}

// ask has an agent ask a request, held until the test ends, and returns its
// id once it is pending.
func (f *opFixture) ask(a agentrequests.Ask) string {
	f.t.Helper()
	before, _ := f.requests.List()
	a.StartedAt = time.Now().Add(-time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.requests.Ask(ctx, a)
	}()
	f.t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		pending, _ := f.requests.List()
		for _, r := range pending {
			if !slices.ContainsFunc(before, func(b agentrequests.Request) bool { return b.ID == r.ID }) {
				return r.ID
			}
		}
	}
	f.t.Fatal("the request was not asked")
	return ""
}

// confirm confirms a proposal as the app would: with the revision of the
// summary it lists.
func (f *opFixture) confirm(ctx context.Context, id string) (agentcontext.OperationOutcome, error) {
	seen := ""
	for _, p := range f.mcp.Proposals.List(f.id) {
		if p.ID == id && p.Operation != nil {
			seen = p.Operation.Revision
		}
	}
	return f.mcp.Proposals.Confirm(ctx, f.id, id, seen)
}

func (f *opFixture) proposal(id string) agentcontext.Proposal {
	f.t.Helper()
	for _, p := range f.mcp.Proposals.List(f.id) {
		if p.ID == id {
			return p
		}
	}
	f.t.Fatalf("no proposal %s", id)
	return agentcontext.Proposal{}
}

func (f *opFixture) groupExists(id string) bool {
	_, err := f.groups.Get(context.Background(), id)
	return err == nil
}

// pullPage is owner/repo#7's page; prPage another pull request's.
var (
	pullPage     = agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}
	forkPullPage = agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 8}
)

// opCase is one operation run end to end: proposed on page with args,
// confirmed, and its effect checked.
type opCase struct {
	op   string
	page agentcontext.PageContext
	args map[string]any
	// argsOf, when set, gives the arguments at the time the case runs.
	argsOf func(f *opFixture) map[string]any
	action string
	// effect checks the route did what the button does.
	effect func(t *testing.T, f *opFixture)
}

func opCases(f *opFixture) []opCase {
	sessionPage := agentcontext.PageContext{Page: agentcontext.PageSession, Session: "claude-code:s2"}
	sessions := agentcontext.PageContext{Page: agentcontext.PageSessions}
	repositories := agentcontext.PageContext{Page: agentcontext.PageRepositories}
	ownRepository := agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: "registration"}
	calls := func(want ...string) func(t *testing.T, f *opFixture) {
		return func(t *testing.T, f *opFixture) {
			if got := f.github.take(); !slices.Equal(got, want) {
				t.Errorf("GitHub was asked %v, want %v", got, want)
			}
		}
	}
	return []opCase{
		{op: "merge", page: pullPage, args: map[string]any{}, action: "Merge owner/repo#7 on GitHub", effect: calls("merge owner/repo#7 SQUASH abc")},
		{op: "merge", page: pullPage, args: map[string]any{"method": "MERGE"}, action: "Merge owner/repo#7", effect: calls("merge owner/repo#7 MERGE abc")},
		{op: "update_branch", page: pullPage, args: map[string]any{}, action: "Update the branch of owner/repo#7", effect: calls("update owner/repo#7 abc")},
		{op: "rerun_checks", page: pullPage, args: map[string]any{}, action: "Re-run the failed checks of owner/repo#7", effect: calls("rerun checks owner/repo#7 abc")},
		{op: "rerun_failed_job", page: pullPage, args: map[string]any{"job_id": 21}, action: "Re-run one failed check of owner/repo#7", effect: calls("rerun job owner/repo#7 abc 21")},
		{op: "resolve_thread", page: pullPage, args: map[string]any{"thread": "RT_1"}, action: "Resolve a review thread of owner/repo#7", effect: calls("resolve owner/repo#7 RT_1")},
		{op: "fix", page: pullPage, args: map[string]any{"only": "checks"}, action: "Start a fix of the failing checks of owner/repo#7", effect: func(t *testing.T, f *opFixture) {
			if js := f.queue.OfPullRequest("owner/repo", 7); len(js) != 1 || js[0].Kind != "fix" || js[0].Only != "checks" || js[0].Clone != f.clone.ID {
				t.Errorf("jobs %+v", js)
			}
		}},
		{op: "review", page: pullPage, args: map[string]any{}, action: "Start a review of owner/repo#7", effect: func(t *testing.T, f *opFixture) {
			if js := f.queue.OfPullRequest("owner/repo", 7); !slices.ContainsFunc(js, func(j jobs.Job) bool { return j.Kind == "" && j.Clone == f.clone.ID }) {
				t.Errorf("jobs %+v", js)
			}
		}},
		{op: "refresh", page: agentcontext.PageContext{Page: agentcontext.PageInbox}, args: map[string]any{"repository": "Owner/Repo"}, action: "Read the pull requests of owner/repo", effect: calls("poll owner/repo")},
		{op: "answer_request", page: sessions, args: map[string]any{"id": f.request, "behavior": "allow"}, action: "Allow the tool use claude-code session s1 asks for", effect: func(t *testing.T, f *opFixture) {
			if pending, _ := f.requests.List(); len(pending) != 0 {
				t.Errorf("the request is still pending: %+v", pending)
			}
		}},
		{op: "send_message", page: sessionPage, args: map[string]any{"text": "Run vet too"}, action: "Send a message to claude-code session s2", effect: func(t *testing.T, f *opFixture) {
			if got := f.sent.take(); len(got) != 1 || !strings.HasPrefix(got[0], "s2: ") || !strings.Contains(got[0], "Run vet too") {
				t.Errorf("sent %v", got)
			}
		}},
		{op: "queue_message", page: sessions, args: map[string]any{"session": "claude-code:s1", "text": "Also lint"}, action: "Queue a message for claude-code session s1", effect: func(t *testing.T, f *opFixture) {
			if _, messages := f.requests.List(); len(messages) != 1 || messages[0].SessionID != "s1" || messages[0].Text != "Also lint" {
				t.Errorf("queued %+v", messages)
			}
		}},
		{op: "focus_session", page: sessions, args: map[string]any{"session": "claude-code:s2"}, action: "Bring claude-code session s2", effect: func(t *testing.T, f *opFixture) {
			if got := f.focus.take(); !slices.Equal(got, []string{"focus claude-code:s2"}) {
				t.Errorf("focused %v", got)
			}
		}},
		{op: "end_session", page: sessionPage, args: map[string]any{}, action: "End claude-code session s2", effect: func(t *testing.T, f *opFixture) {
			if got := f.ended.take(); !slices.Equal(got, []string{"s2"}) {
				t.Errorf("ended %v", got)
			}
		}},
		{op: "cancel_job", page: agentcontext.PageContext{Page: agentcontext.PageJobs}, argsOf: func(f *opFixture) map[string]any {
			for _, j := range f.queue.OfPullRequest("owner/repo", 7) {
				if j.Kind == "fix" {
					return map[string]any{"id": j.ID}
				}
			}
			return map[string]any{"id": "none"}
		}, action: "Cancel the fix job", effect: func(t *testing.T, f *opFixture) {
			// A running job's cancel ends its run, which records the state.
			running := func() bool {
				return slices.ContainsFunc(f.queue.OfPullRequest("owner/repo", 7), func(j jobs.Job) bool {
					return j.Kind == "fix" && (j.State == "queued" || j.State == "running")
				})
			}
			for deadline := time.Now().Add(5 * time.Second); running() && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			if running() {
				t.Error("the fix still runs")
			}
		}},
		{op: "create_group", page: agentcontext.PageContext{Page: agentcontext.PagePulls}, args: map[string]any{"name": "Pair", "repository": "owner/repo", "numbers": []int{8, 7}}, action: "Create a review group of 2 pull requests", effect: func(t *testing.T, f *opFixture) {
			list, _ := f.groups.List(context.Background())
			if !slices.ContainsFunc(list, func(g reviewgroups.Group) bool { return g.Name == "Pair" }) {
				t.Errorf("groups %+v", list)
			}
		}},
		{op: "delete_group", page: agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group}, args: map[string]any{}, action: "Delete a review group of owner/repo", effect: func(t *testing.T, f *opFixture) {
			if f.groupExists(f.group) {
				t.Error("the group was not deleted")
			}
		}},
		{op: "register_repository", page: repositories, args: map[string]any{"repository": "acme/web"}, action: "Add acme/web", effect: func(t *testing.T, f *opFixture) {
			list, _ := f.registry.Repositories(context.Background())
			if !slices.ContainsFunc(list, func(r repos.Repository) bool { return r.Repository == "acme/web" }) {
				t.Errorf("repositories %+v", list)
			}
		}},
		{op: "clone_repository", page: repositories, args: map[string]any{"repository": "acme/web"}, action: "Clone acme/web from GitHub", effect: func(t *testing.T, f *opFixture) {
			deadline := time.Now().Add(5 * time.Second)
			for f.cloned.count() == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if got := f.cloned.take(); len(got) != 1 || !strings.HasPrefix(got[0], f.home+string(filepath.Separator)) {
				t.Errorf("cloned into %v", got)
			}
		}},
		{op: "remove_clone", page: ownRepository, args: map[string]any{"clone": f.clone.ID}, action: "Unregister a clone of owner/repo", effect: func(t *testing.T, f *opFixture) {
			if clones, _ := f.registry.List(context.Background()); slices.ContainsFunc(clones, func(c repos.Clone) bool { return c.ID == f.clone.ID }) {
				t.Error("the clone is still registered")
			}
		}},
		{op: "remove_repository", page: ownRepository, args: map[string]any{}, action: "Stop watching owner/repo", effect: func(t *testing.T, f *opFixture) {
			list, _ := f.registry.Repositories(context.Background())
			if slices.ContainsFunc(list, func(r repos.Repository) bool { return r.Repository == "owner/repo" }) {
				t.Errorf("repositories %+v", list)
			}
		}},
	}
}

// routePattern matches a path against an operation's route, each {name} one
// segment.
func routePattern(route string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.MustCompile(`\\\{[a-z]+\\\}`).ReplaceAllString(regexp.QuoteMeta(route), `[^/]+`) + "$")
}

// Each operation, proposed on its page, runs nothing until it is
// confirmed; confirmed, it calls its own route once, which does what the
// app's button does.
func TestEachOperationRunsItsRouteOnConfirm(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	operations := f.mcp.Operations()
	tested := map[string]bool{}
	for _, tt := range opCases(f) {
		tested[tt.op] = true
		op := operations[tt.op]
		f.page(tt.page)
		args := tt.args
		if tt.argsOf != nil {
			args = tt.argsOf(f)
		}
		receipt, failed := f.propose("propose_"+tt.op, args)
		if failed != "" {
			t.Errorf("%s: %s", tt.op, failed)
			continue
		}
		// The agent learns the receipt: never the summary, the request, or
		// what the routes answered.
		if len(receipt) != 5 || receipt["kind"] != string(agentcontext.ProposalOperation) || receipt["page"] != tt.page.Page {
			t.Errorf("%s answered %v", tt.op, receipt)
		}
		if writes := f.log.take(); len(writes) != 0 {
			t.Errorf("%s: proposing wrote %v", tt.op, writes)
		}
		p := f.proposal(receipt["id"].(string))
		if !strings.Contains(p.Operation.Summary.Action, tt.action) || p.Operation.Summary.Paid != op.Paid || p.Operation.Summary.Irreversible != op.Irreversible {
			t.Errorf("%s: summary %+v", tt.op, p.Operation.Summary)
		}
		raw, _ := json.Marshal(receipt)
		shown := []string{p.Operation.Summary.Action}
		// The fields that could only come from the summary: quoted text of
		// some length (a short fact, such as a count, can occur in a random
		// id).
		for _, d := range p.Operation.Summary.Details {
			if d.Quoted && len(d.Value) >= 6 {
				shown = append(shown, d.Value)
			}
		}
		for _, detail := range shown {
			if strings.Contains(string(raw), detail) {
				t.Errorf("%s: the receipt holds the summary %q", tt.op, detail)
			}
		}
		outcome, err := f.confirm(ctx, p.ID)
		if err != nil || !outcome.OK() {
			t.Errorf("%s: %+v %v", tt.op, outcome, err)
			continue
		}
		writes := f.log.take()
		method, path, _ := strings.Cut(strings.Join(writes, ""), " ")
		if len(writes) != 1 || method != op.Method || !routePattern(op.Route).MatchString(path) {
			t.Errorf("%s ran %v, its route is %s %s", tt.op, writes, op.Method, op.Route)
		}
		tt.effect(t, f)
		if _, err := f.confirm(ctx, p.ID); !errors.Is(err, agentcontext.ErrUnknownProposal) || len(f.log.take()) != 0 {
			t.Errorf("%s ran twice: %v", tt.op, err)
		}
	}
	for name := range operations {
		if !tested[name] {
			t.Errorf("%s is not tested against its route", name)
		}
	}
}

// What the route refuses on Confirm comes back as its answer and changes
// nothing.
func TestTheRoutesOwnValidationApplies(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	refused := func(page agentcontext.PageContext, tool string, args any, status int) {
		t.Helper()
		f.page(page)
		receipt, failed := f.propose(tool, args)
		if failed != "" {
			t.Fatalf("%s: %s", tool, failed)
		}
		outcome, err := f.confirm(ctx, receipt["id"].(string))
		if err != nil || outcome.Status != status || outcome.Message == "" {
			t.Fatalf("%s: %+v %v", tool, outcome, err)
		}
		if len(f.log.take()) != 1 {
			t.Fatalf("%s did not run its route once", tool)
		}
	}
	// GitHub refuses the merge: the head moved.
	f.github.mu.Lock()
	f.github.mergeErr = &github.MergeError{Code: github.MergeHeadMoved, Message: "The pull request has new commits."}
	f.github.mu.Unlock()
	refused(pullPage, "propose_merge", map[string]any{}, http.StatusConflict)
	if got := f.github.take(); len(got) != 0 {
		t.Fatalf("merged: %v", got)
	}
	// A group that holds a pull request twice.
	refused(agentcontext.PageContext{Page: agentcontext.PagePulls}, "propose_create_group", map[string]any{"name": "Twice", "repository": "owner/repo", "numbers": []int{7, 7}}, http.StatusBadRequest)
	if list, _ := f.groups.List(ctx); len(list) != 1 {
		t.Fatalf("groups %+v", list)
	}
	// A review of a pull request that needs the person's isolation
	// confirmation, which an agent cannot give.
	refused(forkPullPage, "propose_review", map[string]any{}, http.StatusConflict)
	if js := f.queue.OfPullRequest("owner/repo", 8); len(js) != 0 {
		t.Fatalf("jobs %+v", js)
	}
	// A message of blanks.
	refused(agentcontext.PageContext{Page: agentcontext.PageSessions}, "propose_send_message", map[string]any{"session": "claude-code:s2", "text": "   "}, http.StatusBadRequest)
	if got := f.sent.take(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// The chat never answers its own session's requests, and a session's page
// offers only its own.
func TestAChatNeverAnswersItsOwnRequests(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat, Session: agentcontext.SessionKey("claude-code", "s1")})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	reads := &readFixture{mcp: f.mcp}
	for _, page := range []agentcontext.PageContext{{Page: agentcontext.PageSessions}, {Page: agentcontext.PageSession, Session: "claude-code:s1"}} {
		if err := f.mcp.Registry.SetPage(id, page); err != nil {
			t.Fatal(err)
		}
		text, failed := reads.callTool(t, token, "propose_answer_request", map[string]any{"id": f.request, "behavior": "allow"})
		if !failed || !strings.Contains(text, "its own session") {
			t.Fatalf("%s: a chat proposed answering its own request: %s", page.Page, text)
		}
	}
	// Another chat on s2's page: the request is s1's, not on the page.
	f.page(agentcontext.PageContext{Page: agentcontext.PageSession, Session: "claude-code:s2"})
	if _, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "deny"}); !strings.Contains(failed, "another session's") {
		t.Fatal("answered a request the page does not show:", failed)
	}
	// On the Sessions page it may propose; the request answered in the
	// meantime (by the person) is not answered again.
	f.page(agentcontext.PageContext{Page: agentcontext.PageSessions})
	receipt, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "deny"})
	if failed != "" {
		t.Fatal(failed)
	}
	if err := f.requests.Answer(f.request, agentrequests.Reply{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.confirm(ctx, receipt["id"].(string)); err == nil || len(f.log.take()) != 0 {
		t.Fatal("an answered request was answered again:", err)
	}
	if len(f.mcp.Proposals.List(id)) != 0 {
		t.Fatal("the refused chat holds a proposal")
	}
}

// Confirming after the person moved to another page, or after the token was
// revoked, runs nothing.
func TestConfirmAfterNavigationOrRevokeRunsNothing(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	f.page(pullPage)
	receipt, failed := f.propose("propose_merge", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	id := receipt["id"].(string)
	for _, page := range []agentcontext.PageContext{forkPullPage, {Page: agentcontext.PageInbox}} {
		f.page(page)
		if _, err := f.confirm(ctx, id); !errors.Is(err, agentcontext.ErrNotCurrentPage) {
			t.Fatal(err)
		}
	}
	f.page(pullPage)
	f.revoke()
	if _, err := f.confirm(ctx, id); err == nil {
		t.Fatal("a revoked chat's operation was confirmed")
	}
	if writes, merged := f.log.take(), f.github.take(); len(writes) != 0 || len(merged) != 0 {
		t.Fatalf("ran %v %v", writes, merged)
	}
}

// Every operation's request stays on its own route, whatever an agent puts
// in its arguments: each string argument is replaced with path traversal,
// encoded slashes, ids with a slash, a query and a fragment, and whatever
// is proposed is confirmed.
func TestOperationsCallOnlyTheirOwnRoute(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	operations := f.mcp.Operations()
	crafted := []string{"../../settings", "..", ".", "a/b", "a%2Fb", "a%2fb", "x?refresh=1", "x#y", `a\b`, "/api/settings/agent-env", "claude-code:s2/../../x", "owner/repo/../other", "%2e%2e", " "}
	for _, tt := range opCases(f) {
		op := operations[tt.op]
		pattern := routePattern(op.Route)
		variants := []map[string]any{}
		for key, value := range tt.args {
			if _, ok := value.(string); !ok {
				continue
			}
			for _, evil := range crafted {
				args := map[string]any{}
				for k, v := range tt.args {
					args[k] = v
				}
				args[key] = evil
				variants = append(variants, args)
			}
		}
		// The arguments a page's own target is named by elsewhere.
		for _, key := range []string{"session", "id", "repository", "clone"} {
			if _, ok := tt.args[key]; !ok {
				for _, evil := range crafted {
					variants = append(variants, map[string]any{key: evil})
				}
			}
		}
		for _, args := range variants {
			f.page(tt.page)
			receipt, failed := f.propose("propose_"+tt.op, args)
			if failed != "" {
				continue
			}
			_, _ = f.confirm(ctx, receipt["id"].(string))
		}
		for _, write := range f.log.take() {
			method, path, _ := strings.Cut(write, " ")
			if method != op.Method || !pattern.MatchString(path) {
				t.Errorf("%s sent %s, its route is %s %s", tt.op, write, op.Method, op.Route)
			}
		}
		for _, raw := range f.log.rawPaths() {
			if raw != "" {
				t.Errorf("%s sent an escaped path %s", tt.op, raw)
			}
		}
	}
}

// The operations' flags, as the person reads them before confirming.
func TestOperationFlags(t *testing.T) {
	f := newOpFixture(t)
	want := map[string][2]bool{ // paid, irreversible
		"merge": {false, true}, "update_branch": {}, "rerun_checks": {}, "rerun_failed_job": {}, "resolve_thread": {},
		"fix": {true, false}, "review": {true, false}, "refresh": {},
		"answer_request": {}, "send_message": {}, "queue_message": {}, "end_session": {false, true}, "focus_session": {},
		"cancel_job": {}, "create_group": {}, "delete_group": {false, true},
		"register_repository": {}, "remove_repository": {false, true}, "remove_clone": {false, true}, "clone_repository": {},
	}
	got := f.mcp.Operations()
	if len(got) != len(want) {
		t.Errorf("%d operations, want %d", len(got), len(want))
	}
	for name, flags := range want {
		op, ok := got[name]
		if !ok || op.Paid != flags[0] || op.Irreversible != flags[1] {
			t.Errorf("%s: paid %v irreversible %v, want %v", name, op.Paid, op.Irreversible, flags)
		}
	}
}
