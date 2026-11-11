package uiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/inboxqueue"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/sessions/session"
)

// pullFixture is the daemon's LocalAPI over a registered clone of
// owner/repo, with fakes behind the routes that read GitHub, the jobs, the
// groups and the review index, and an MCP server reading through it.
type pullFixture struct {
	api     *LocalHandler
	mcp     *agentcontext.Server
	clone   repos.Clone
	updater *fakeUpdater
	merger  *fakeMerger
}

const pullHead = "aaaaaaaa"

func newPullFixture(t *testing.T) *pullFixture {
	t.Helper()
	registry, clone := factsRegistry(t)
	facts := &fakeFacts{document: []byte(`{"schema":"baum.facts/1","review":{"repository":"owner/repo","pull_request":7,"head":"` + pullHead + `","base":"bbbbbbbb","merge_base":"bbbbbbbb"},"index":{"hunks":[{"id":"h_1","path":"main.go","change":"modified","additions":4,"deletions":1},{"id":"h_2","path":"main.go","change":"modified","additions":1,"deletions":0}]}}`)}
	updater, merger := &fakeUpdater{}, &fakeMerger{}
	unresolved := 2
	index := reviewindex.Snapshot{Entries: []reviewindex.Entry{
		{PullRequest: discovery.PullRequest{Repository: "owner/repo", Number: 7, HeadSHA: pullHead, Title: "Add search", Author: "ada", Unresolved: &unresolved}, CloneID: clone.ID, Status: "none"},
		{PullRequest: discovery.PullRequest{Repository: "other/repo", Number: 7, HeadSHA: "cccccccc", Title: "Another repository"}, CloneID: "elsewhere", Status: "none"},
	}, Problems: []reviewindex.Problem{{CloneID: "elsewhere", Reason: "private reason"}}}
	queue := inboxqueue.Queue{Count: 2, Items: []inboxqueue.Item{
		{Key: "owner/repo#7", Kind: inboxqueue.Review, Inboxes: []string{"mine"}},
		{Key: "other/repo#7", Kind: inboxqueue.Fix, Fixing: true, Inboxes: []string{"mine"}},
	}, Inboxes: []inboxqueue.Inbox{{ID: "mine", Query: "private query", Count: 2}}}
	api := LocalAPI{
		Registry: registry, Config: config.Default(), Facts: facts,
		Update: UpdateHandler(registry, updater, nil, nil),
		Merge:  MergeHandler(registry, merger, nil, nil),
		Index:  jsonHandler(index),
		Inbox:  jsonHandler(queue),
		Jobs:   fakeJobs(clone.ID),
		Groups: fakeGroups(),
	}.Handler()
	t.Cleanup(api.Close)
	graph := &session.Graph{
		Sessions:   []session.Session{{ID: "root", Title: ptr("Add search"), Outcome: session.OutcomeCommitted}},
		UserInputs: []session.UserInput{{ID: "p1", SessionID: "root", Ordinal: 1, Text: "Add the search box", Kind: session.KindPrompt, Origin: session.OriginHuman}},
	}
	api.documents.resolve = func(context.Context, locallink.Request) (locallink.Result, error) {
		return locallink.Result{
			Available: true,
			Sessions:  []locallink.Session{{ID: "s1", Source: "claude_code", Vendor: "claude_code", Branch: "search"}},
			Commits: []locallink.Commit{{OID: "cccccccccccccccccccccccccccccccccccccccc", Subject: "Add search", Links: []locallink.Link{
				{Session: "s1", Method: "commit_ref", Confidence: 0.6},
			}}},
			Graphs: map[string]*session.Graph{"s1": graph},
		}, nil
	}
	mcp := agentcontext.New(agentcontext.Options{API: api, Version: "test", Clone: func(ctx context.Context, id string) (string, error) {
		return RegisteredRepository(ctx, registry, id)
	}})
	return &pullFixture{api: api, mcp: mcp, clone: clone, updater: updater, merger: merger}
}

func ptr[T any](v T) *T { return &v }

func jsonHandler(v any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, v)
	})
}

// fakeJobs answers as the queue's handler does: a pull request's jobs by
// repository and pr, one job by id, and a fix job's transcript. Writes
// are method patterns, as the real handler's are.
func fakeJobs(cloneID string) http.Handler {
	fix := jobs.Job{ID: "j-fix", State: "done", Phase: "done", Summary: "Renamed the helper", HasTranscript: true,
		Prepared: jobs.Prepared{Repository: "owner/repo", HeadSHA: pullHead, Item: jobs.Item{Clone: cloneID, PR: 7, Kind: jobs.KindFix}}}
	review := jobs.Job{ID: "j-review", State: "done",
		Prepared: jobs.Prepared{Repository: "owner/repo", HeadSHA: pullHead, Item: jobs.Item{Clone: cloneID, PR: 7}}}
	foreign := jobs.Job{ID: "j-foreign", State: "done", HasTranscript: true,
		Prepared: jobs.Prepared{Repository: "other/repo", HeadSHA: "cccccccc", Item: jobs.Item{Clone: "elsewhere", PR: 7, Kind: jobs.KindFix}}}
	byID := map[string]jobs.Job{fix.ID: fix, review.ID: review, foreign.ID: foreign}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var list []jobs.Job
		for _, job := range []jobs.Job{fix, review, foreign} {
			if job.Repository == q.Get("repository") && q.Get("pr") == "7" {
				list = append(list, job)
			}
		}
		writeJSON(w, map[string]any{"available": true, "jobs": list})
	})
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, ok := byID[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, job)
	})
	mux.HandleFunc("GET /api/jobs/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		job, ok := byID[r.PathValue("id")]
		if !ok || !job.HasTranscript {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"entries": []map[string]any{{"seq": 1, "kind": "text", "text": "I renamed the helper in " + job.ID}}, "next": 1, "more": false, "done": true})
	})
	mux.HandleFunc("POST /api/jobs/{id}/cancel", func(http.ResponseWriter, *http.Request) { panic("cancelled by a read") })
	return mux
}

// fakeGroups answers as the groups' handler does.
func fakeGroups() http.Handler {
	own := map[string]any{"id": "g-own", "repository": "owner/repo", "name": "Search", "members": []int{7, 8}, "stack": false, "landed": []reviewgroups.LandedMember{}, "outdated": true}
	foreign := map[string]any{"id": "g-foreign", "repository": "other/repo", "name": "Private", "members": []int{1, 2}, "landed": []reviewgroups.LandedMember{}, "outdated": false}
	byID := map[string]map[string]any{"g-own": own, "g-foreign": foreign}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/groups/{id}", func(w http.ResponseWriter, r *http.Request) {
		group, ok := byID[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, group)
	})
	mux.HandleFunc("GET /api/groups/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"version": 1, "group": r.PathValue("id")}})
	})
	mux.HandleFunc("DELETE /api/groups/{id}", func(http.ResponseWriter, *http.Request) { panic("deleted by a read") })
	return mux
}

func (f *pullFixture) route(t *testing.T, path string) json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	return json.RawMessage(bytes.TrimSpace(w.Body.Bytes()))
}

var fence = regexp.MustCompile(`(?s)^BEGIN UNTRUSTED DATA ([0-9a-f]+)\n(.*)\nEND UNTRUSTED DATA ([0-9a-f]+)\n`)

// callTool calls an MCP tool and returns the data between its fence.
func (f *pullFixture) callTool(t *testing.T, token, name string, args any) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	var reply struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool `json:"isError"`
		}
		Error *struct{ Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Error != nil || len(reply.Result.Content) != 1 {
		t.Fatalf("%s: %v %s", name, err, w.Body.String())
	}
	m := fence.FindStringSubmatch(reply.Result.Content[0].Text)
	if m == nil || m[1] != m[3] {
		t.Fatalf("%s: not fenced: %.200q", name, reply.Result.Content[0].Text)
	}
	return m[2], reply.Result.IsError
}

// field is one top-level field of a tool's JSON, as JSON.
func field(t *testing.T, text, name string) json.RawMessage {
	t.Helper()
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &all); err != nil {
		t.Fatalf("not an object: %v %.200s", err, text)
	}
	return all[name]
}

// sameJSON fails unless a and b are the same JSON value.
func sameJSON(t *testing.T, what string, a, b json.RawMessage) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%s: %v: %s", what, err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%s: %v: %s", what, err, b)
	}
	if !reflect.DeepEqual(x, y) {
		t.Errorf("%s differs:\n tool  %s\n route %s", what, a, b)
	}
}

// The pull request and group tools return what the routes the app calls
// return, for a chat on the pull request's page and an action on its pull
// request alike.
func TestPullRequestToolsReturnWhatTheAppRoutesReturn(t *testing.T) {
	f := newPullFixture(t)
	chatToken, chatID, revokeChat, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeChat()
	if err := f.mcp.Registry.SetPage(chatID, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}); err != nil {
		t.Fatal(err)
	}
	actionToken, _, revokeAction, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: "owner/repo", PullRequest: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeAction()
	selector := "clone=" + f.clone.ID + "&pr=7"
	for name, token := range map[string]string{"chat": chatToken, "action": actionToken} {
		t.Run(name, func(t *testing.T) {
			// The header, from the review index, the description and the
			// inbox, of this repository's pull request only.
			text, failed := f.callTool(t, token, "pull_request", nil)
			if failed || strings.Contains(text, "Another repository") || strings.Contains(text, "private") || strings.Contains(text, f.clone.ID) {
				t.Fatalf("pull_request: %v %s", failed, text)
			}
			var pr struct {
				Title      string `json:"title"`
				Author     string `json:"author"`
				HeadSHA    string `json:"head_sha"`
				Unresolved int    `json:"unresolved"`
			}
			if err := json.Unmarshal(field(t, text, "pull_request"), &pr); err != nil || pr.Title != "Add search" || pr.Author != "ada" || pr.HeadSHA != pullHead || pr.Unresolved != 2 {
				t.Errorf("pull request %+v: %v", pr, err)
			}
			sameJSON(t, "description", field(t, text, "description"), f.route(t, "/api/prs/description?repository=owner/repo&pr=7"))
			if got := string(field(t, text, "inbox")); got != `{"archived":false,"fixing":false,"kind":"review","snoozed":false}` {
				t.Errorf("inbox %s", got)
			}

			// The files from the facts, and each patch as the facts route gives it.
			text, failed = f.callTool(t, token, "diff", nil)
			if failed {
				t.Fatalf("diff: %s", text)
			}
			var files []struct {
				Path                 string
				Additions, Deletions int
				Hunks                int
				Patch                json.RawMessage
			}
			if err := json.Unmarshal(field(t, text, "files"), &files); err != nil || len(files) != 1 || files[0].Path != "main.go" || files[0].Additions != 5 || files[0].Deletions != 1 || files[0].Hunks != 2 {
				t.Fatalf("files %s: %v", text, err)
			}
			var patch struct{ Patch json.RawMessage }
			if err := json.Unmarshal(f.route(t, "/api/facts/patch?"+selector+"&head="+pullHead+"&path=main.go"), &patch); err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "patch", files[0].Patch, patch.Patch)
			if string(field(t, text, "source")) != `"facts"` || string(field(t, text, "head")) != `"`+pullHead+`"` {
				t.Errorf("source %s", text)
			}
			if text, failed := f.callTool(t, token, "diff", map[string]any{"path": "missing.go"}); !failed || !strings.Contains(text, "not a changed file") {
				t.Errorf("a file the pull request does not change: %v %s", failed, text)
			}

			for tool, route := range map[string]string{
				"commits":  "/api/facts/commits?" + selector + "&head=" + pullHead,
				"sessions": "/api/facts/sessions?" + selector + "&head=" + pullHead,
			} {
				text, failed := f.callTool(t, token, tool, nil)
				if failed {
					t.Fatalf("%s: %s", tool, text)
				}
				sameJSON(t, tool, json.RawMessage(text), f.route(t, route))
			}
			text, failed = f.callTool(t, token, "session_transcript", map[string]any{"id": "s1"})
			if failed {
				t.Fatalf("session_transcript: %s", text)
			}
			sameJSON(t, "session transcript", json.RawMessage(text), f.route(t, "/api/facts/sessions/s1?"+selector+"&head="+pullHead))
			if text, failed := f.callTool(t, token, "session_transcript", map[string]any{"id": "s-unlinked"}); !failed {
				t.Errorf("a session the pull request does not link: %s", text)
			}

			text, _ = f.callTool(t, token, "checks", nil)
			sameJSON(t, "checks", field(t, text, "checks"), field(t, string(f.route(t, "/api/prs/checks?repository=owner/repo&pr=7&head_sha="+pullHead)), "checks"))
			sameJSON(t, "failing checks", field(t, text, "failing"), field(t, string(f.route(t, "/api/prs/failing-checks?repository=owner/repo&pr=7&head_sha="+pullHead)), "checks"))

			text, failed = f.callTool(t, token, "review_threads", nil)
			if failed {
				t.Fatalf("review_threads: %s", text)
			}
			sameJSON(t, "threads", json.RawMessage(text), f.route(t, "/api/prs/review-threads?repository=owner/repo&pr=7&unresolved=2"))

			text, _ = f.callTool(t, token, "merge_state", nil)
			sameJSON(t, "merge options", field(t, text, "merge"), f.route(t, "/api/prs/merge-options?repository=owner/repo&pr=7"))
			sameJSON(t, "update options", field(t, text, "update"), f.route(t, "/api/prs/update-branch-options?repository=owner/repo&pr=7"))
			sameJSON(t, "rerun options", field(t, text, "rerun_checks"), f.route(t, "/api/prs/rerun-checks-options?repository=owner/repo&pr=7"))

			text, failed = f.callTool(t, token, "jobs", nil)
			if failed || strings.Contains(text, "j-foreign") || !strings.Contains(text, `"summary":"Renamed the helper"`) || !strings.Contains(text, `"id":"j-review"`) {
				t.Errorf("jobs: %v %s", failed, text)
			}

			text, failed = f.callTool(t, token, "job_transcript", map[string]any{"id": "j-fix"})
			if failed {
				t.Fatalf("job_transcript: %s", text)
			}
			sameJSON(t, "job transcript", json.RawMessage(text), f.route(t, "/api/jobs/j-fix/transcript"))
			if text, failed := f.callTool(t, token, "job_transcript", map[string]any{"id": "j-review"}); failed || !strings.Contains(text, "only fix jobs") {
				t.Errorf("a review job: %v %s", failed, text)
			}
			if text, failed := f.callTool(t, token, "job_transcript", map[string]any{"id": "j-foreign"}); !failed || strings.Contains(text, "renamed") {
				t.Errorf("another repository's job: %v %s", failed, text)
			}

			text, failed = f.callTool(t, token, "group", map[string]any{"id": "g-own"})
			if failed {
				t.Fatalf("group: %s", text)
			}
			sameJSON(t, "group", json.RawMessage(text), f.route(t, "/api/groups/g-own"))
			text, failed = f.callTool(t, token, "group_versions", map[string]any{"id": "g-own"})
			if failed {
				t.Fatalf("group_versions: %s", text)
			}
			sameJSON(t, "group versions", json.RawMessage(text), f.route(t, "/api/groups/g-own/versions"))
			for _, tool := range []string{"group", "group_versions"} {
				if text, failed := f.callTool(t, token, tool, map[string]any{"id": "g-foreign"}); !failed || strings.Contains(text, "Private") || strings.Contains(text, "version") {
					t.Errorf("%s of another repository's group: %v %s", tool, failed, text)
				}
			}
		})
	}
	// An action is refused every other repository, by tool; a chat is not.
	for _, tool := range []string{"pull_request", "diff", "commits", "checks", "review_threads", "merge_state", "jobs", "sessions"} {
		if text, failed := f.callTool(t, actionToken, tool, map[string]any{"repository": "other/repo", "pr": 7}); !failed || strings.Contains(text, "Another repository") {
			t.Errorf("an action's %s of another repository: %v %s", tool, failed, text)
		}
	}
	if text, failed := f.callTool(t, chatToken, "pull_request", map[string]any{"repository": "other/repo", "pr": 7}); failed || !strings.Contains(text, "Another repository") {
		t.Errorf("a chat's pull_request of another repository: %v %s", failed, text)
	}
	// Nothing was written by reading.
	if len(f.merger.merged) != 0 {
		t.Errorf("merged %v", f.merger.merged)
	}
	for _, call := range f.updater.calls {
		if strings.HasPrefix(call, "resolve") || strings.HasPrefix(call, "rerun") || strings.HasPrefix(call, "update ") {
			t.Errorf("a read called %s", call)
		}
	}
}

var writeRoute = regexp.MustCompile(`"(POST|PUT|PATCH|DELETE) (/api/[^"]*)"`)

// No tool can reach a route that writes: every write route the daemon
// registers beside the routes the tools read is registered for its own
// method, so the reader's GET never reaches it, and the daemon answers a GET
// to it with 405 or 404 with nothing done.
func TestNoToolReadsAWriteRoute(t *testing.T) {
	f := newPullFixture(t)
	reads := f.mcp.ToolReads()
	var prefixes []string
	for tool, tp := range reads {
		if len(tp) == 0 && tool != "focus" && tool != "current_page" && tool != "read_more" {
			t.Errorf("%s declares no route", tool)
		}
		prefixes = append(prefixes, tp...)
	}
	under := func(path string) bool {
		for _, prefix := range prefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		return false
	}
	var sources []string
	for _, pattern := range []string{"*.go", "../jobs/*.go", "../reviewgroups/*.go", "../repos/*.go", "../inboxqueue/*.go", "../reviewindex/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, matches...)
	}
	found := 0
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range writeRoute.FindAllStringSubmatch(string(body), -1) {
			path := regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllString(m[2], "x")
			if !under(path) {
				continue
			}
			found++
			w := httptest.NewRecorder()
			f.api.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
			// A GET of a path with a GET handler of its own is a read, which is
			// the point of declaring the prefix.
			if w.Code < 400 {
				continue
			}
			if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
				t.Errorf("GET %s (a %s route in %s): %d", path, m[1], source, w.Code)
			}
		}
	}
	if found == 0 {
		t.Fatal("no write route found under the tools' prefixes: the scan is broken")
	}
	if len(f.merger.merged) != 0 {
		t.Errorf("merged %v", f.merger.merged)
	}
	for _, call := range f.updater.calls {
		if !strings.HasPrefix(call, "threads") && !strings.HasPrefix(call, "checks") && !strings.HasPrefix(call, "all checks") && !strings.HasPrefix(call, "description") {
			t.Errorf("a GET called %s", call)
		}
	}
}
