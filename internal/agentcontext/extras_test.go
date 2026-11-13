package agentcontext

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// extrasDaemon answers the routes of the pull request and group extras on top
// of fakeDaemon's: a file, a commit and its patch, the decisions, the
// person's kept draft review and walkthrough progress, and a group's patch and
// file. Another repository's answers carry a planted text, so a read that
// should have been refused shows.
type extrasDaemon struct{ *fakeDaemon }

func (d extrasDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q, p := r.URL.Query(), r.URL.Path
	reply := func(format string, args ...any) {
		d.mu.Lock()
		d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, format, args...)
	}
	switch {
	case r.Method != http.MethodGet:
	case p == "/api/file":
		reply(`{"path":%q,"content":"file of %s %s","truncated":false}`, q.Get("path"), q.Get("clone"), q.Get("pr"))
		return
	case p == "/api/commit" || p == "/api/facts/commit":
		reply(`{"oid":%q,"body":"commit via %s of %s %s","files":[{"path":"a.go","status":"modified","additions":1,"deletions":0}]}`, q.Get("oid"), p, q.Get("clone"), q.Get("pr"))
		return
	case p == "/api/commit-patch" || p == "/api/facts/commit-patch":
		reply(`{"patch":"@@ commit patch %s %s via %s of %s %s"}`, q.Get("oid"), q.Get("path"), p, q.Get("clone"), q.Get("pr"))
		return
	case p == "/api/decisions":
		reply(`{"sessions":[{"agent":"claude","session_id":"s1","coverage":"extracted","capturing":false,"decisions":[{"decision_id":"d1","summary":"decisions of %s %s"}]}],"review_generated_at":null}`, q.Get("repository"), q.Get("pr"))
		return
	case p == "/api/ui-state/review":
		key := q.Get("key")
		switch key {
		case "acme/web#12":
			reply(`{"revision":3,"value":{"comments":[{"id":"c1","target":{"kind":"file","id":"a.go","title":"a.go"},"body":"My draft comment","place":{"path":"a.go","line":4,"side":"RIGHT"},"head":"h12","created":1700000000000}],"note":"My note","sent":[{"url":"https://github.com/acme/web/pull/12#r1","event":"COMMENT","at":1700000001000,"head":"h11","note":"First","comments":[{"title":"a.go","where":"a.go:1","body":"Sent one"}]}]}}`)
		case "evil/secrets#1", "acme/web#13":
			reply(`{"revision":1,"value":{"comments":[{"id":"x","target":{"kind":"general","id":"","title":"The pull request"},"body":"FOREIGN DRAFT of %s","place":null,"head":"h","created":1}],"note":"","sent":[]}}`, key)
		default:
			reply(`{"revision":0,"value":{"comments":[],"note":""}}`)
		}
		return
	case p == "/api/ui-state/walkthrough":
		key := q.Get("key")
		if key == "acme/web#33@v1" || key == "acme/web#13@v2" {
			reply(`{"revision":2,"value":{"seen":["stop_1"],"notes":{"stop_1":"This looks off"},"note":"Overall"}}`)
		} else {
			reply(`{"revision":0,"value":{"seen":[],"notes":{},"note":""}}`)
		}
		return
	case strings.HasPrefix(p, "/api/groups/") && (strings.HasSuffix(p, "/patch") || strings.HasSuffix(p, "/file")):
		id := strings.Split(p, "/")[3]
		// The real handler answers any group's; the group record is checked
		// first (g-evil is another repository's).
		if _, known := map[string]bool{"g-web": true, "g-evil": true}[id]; known {
			reply(`{"route":%q,"group":%q,"version":%q,"path":%q,"hunk":%q}`, p[strings.LastIndex(p, "/")+1:], id, q.Get("version"), q.Get("path"), q.Get("hunk"))
			return
		}
	}
	d.fakeDaemon.ServeHTTP(w, r)
}

func extrasServer(t *testing.T) (*Server, extrasDaemon) {
	t.Helper()
	d := extrasDaemon{&fakeDaemon{}}
	clones := map[string]string{"c-web": "acme/web", "c-evil": "evil/secrets"}
	return New(Options{API: d, Version: "test", Clone: func(_ context.Context, id string) (string, error) {
		if repository, ok := clones[id]; ok {
			return repository, nil
		}
		return "", errors.New("unknown clone")
	}}), d
}

func TestPullRequestExtraToolsReadThePageRoutes(t *testing.T) {
	s, d := extrasServer(t)
	token, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		tool   string
		args   any
		want   []string
		failed string
	}{
		// A file needs a saved review (pull request 13 has one).
		{tool: "file", args: map[string]any{"pr": 13, "path": "main.go"}, want: []string{`"path":"main.go"`, `"content":"file of c-web 13"`}},
		{tool: "file", args: map[string]any{"path": "main.go"}, failed: "no saved review"},
		// A commit and its patch: the facts' before a review, the review's after.
		{tool: "commit", args: map[string]any{"oid": "abcdef1"}, want: []string{`commit via /api/facts/commit of c-web 12`, `"path":"a.go"`}},
		{tool: "commit", args: map[string]any{"pr": 13, "oid": "abcdef1"}, want: []string{`commit via /api/commit of c-web 13`}},
		{tool: "commit_patch", args: map[string]any{"oid": "abcdef1", "path": "a.go"}, want: []string{`commit patch abcdef1 a.go via /api/facts/commit-patch of c-web 12`}},
		{tool: "commit_patch", args: map[string]any{"pr": 13, "oid": "abcdef1", "path": "a.go"}, want: []string{`via /api/commit-patch of c-web 13`}},
		{tool: "commit", args: map[string]any{"oid": "not a commit"}, failed: "arguments"},
		{tool: "pull_request_decisions", want: []string{`decisions of acme/web 12`, `"coverage":"extracted"`}},
		{tool: "update_branch_options", want: []string{`/api/prs/update-branch-options`}},
		{tool: "rerun_options", want: []string{`/api/prs/rerun-checks-options`}},
		// The person's own draft, projected: no ids.
		{tool: "pending_review", want: []string{`"body":"My draft comment"`, `"note":"My note"`, `"place":{"path":"a.go","line":4,"side":"RIGHT"}`, `"head":"h12"`, `"body":"Sent one"`}},
		{tool: "pending_review", args: map[string]any{"pr": 14}, want: []string{`"comments":[]`}},
		// The latest version is the index's latest (pull request 33's is 1).
		{tool: "walkthrough_position", args: map[string]any{"pr": 33}, want: []string{`"version":1`, `"seen":["stop_1"]`, `This looks off`, `"note":"Overall"`}},
		{tool: "walkthrough_position", args: map[string]any{"pr": 13, "version": 2}, want: []string{`"version":2`, `"seen":["stop_1"]`}},
		{tool: "walkthrough_position", args: map[string]any{"pr": 13, "version": 1}, want: []string{`"seen":[]`}},
		{tool: "walkthrough_position", failed: "no saved review"},
	} {
		d.reset()
		text, failed := data(t, s, token, tt.tool, tt.args)
		if (tt.failed != "") != failed || (failed && !strings.Contains(text, tt.failed)) {
			t.Errorf("%s %v: failed=%v %s", tt.tool, tt.args, failed, text)
			continue
		}
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s %v lacks %s:\n%s", tt.tool, tt.args, want, text)
			}
		}
		for _, leak := range []string{`"id":"c1"`, "revision"} {
			if tt.tool == "pending_review" && strings.Contains(text, leak) {
				t.Errorf("%s has %s: %s", tt.tool, leak, text)
			}
		}
		for _, request := range d.requests() {
			if !strings.HasPrefix(request, "GET ") {
				t.Errorf("%s sent %s", tt.tool, request)
			}
		}
	}
	// The key is the app's: the repository in lower case.
	d.reset()
	data(t, s, token, "pending_review", map[string]any{"repository": "ACME/Web", "pr": 12})
	if got := strings.Join(d.requests(), " "); !strings.Contains(got, "key=acme%2Fweb%2312") {
		t.Errorf("the draft's key: %s", got)
	}
	// A group's patch and file, with the version and hunk passed on.
	text, failed := data(t, s, token, "group_patch", map[string]any{"id": "g-web", "path": "a.go", "hunk": "h_1", "version": 2})
	if failed || !strings.Contains(text, `"route":"patch"`) || !strings.Contains(text, `"version":"2"`) || !strings.Contains(text, `"hunk":"h_1"`) {
		t.Errorf("group_patch: %v %s", failed, text)
	}
	text, failed = data(t, s, token, "group_file", map[string]any{"id": "g-web", "path": "a.go"})
	if failed || !strings.Contains(text, `"route":"file"`) || strings.Contains(text, "hunk\":\"h") {
		t.Errorf("group_file: %v %s", failed, text)
	}
	if text, failed = data(t, s, token, "group_file", map[string]any{"id": "g-web", "path": "a.go", "hunk": "h_1"}); !failed {
		t.Errorf("a hunk for a file: %s", text)
	}
	// The pages list them.
	for page, tools := range map[string][]string{
		PagePullRequest: {"file", "commit", "commit_patch", "pull_request_decisions", "update_branch_options", "rerun_options", "pending_review", "walkthrough_position"},
		PageGroup:       {"group_patch", "group_file"},
	} {
		spec, _ := s.page(page)
		for _, tool := range tools {
			if !contains(spec.Tools, tool) {
				t.Errorf("page %s does not list %s", page, tool)
			}
		}
	}
	if spec, _ := s.page(PagePullRequest); contains(spec.Tools, "group_patch") {
		t.Error("the pull request page lists a group's patch")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// An action reads these for its own repository only, and its draft and
// walkthrough for its own pull request only.
func TestPullRequestExtraToolsAreBoundForActions(t *testing.T) {
	s, d := extrasServer(t)
	token, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{"repository": "evil/secrets", "pr": 1}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for tool, args := range map[string]map[string]any{
		"file":                   with(map[string]any{"path": "main.go"}),
		"commit":                 with(map[string]any{"oid": "abcdef1"}),
		"commit_patch":           with(map[string]any{"oid": "abcdef1", "path": "a.go"}),
		"pull_request_decisions": with(nil),
		"update_branch_options":  with(nil),
		"rerun_options":          with(nil),
		"pending_review":         with(nil),
		"walkthrough_position":   with(map[string]any{"version": 1}),
		"group_patch":            {"repository": "evil/secrets", "id": "g-evil", "path": "a.go"},
		"group_file":             {"repository": "evil/secrets", "id": "g-evil", "path": "a.go"},
	} {
		d.reset()
		text, failed := data(t, s, token, tool, args)
		if !failed || !strings.Contains(text, "own pull request's repository") {
			t.Errorf("%s of another repository: failed=%v %s", tool, failed, text)
		}
		if requests := d.requests(); len(requests) != 0 {
			t.Errorf("%s reached the daemon: %v", tool, requests)
		}
	}
	// Another repository's group, named with the action's own repository, is
	// refused by the group check, never read.
	d.reset()
	text, failed := data(t, s, token, "group_file", map[string]any{"id": "g-evil", "path": "a.go"})
	if !failed || !strings.Contains(text, "own pull request's repository") || strings.Contains(strings.Join(d.requests(), " "), "/g-evil/file") {
		t.Errorf("another repository's group: failed=%v %s %v", failed, text, d.requests())
	}
	// Its own pull request's draft, and its own pull request only: a crafted
	// number reads nothing.
	text, failed = data(t, s, token, "pending_review", nil)
	if failed || !strings.Contains(text, "My draft comment") {
		t.Errorf("its own draft: %v %s", failed, text)
	}
	d.reset()
	for _, args := range []map[string]any{{"pr": 13}, {"pr": 1}} {
		text, failed = data(t, s, token, "pending_review", args)
		if !failed || strings.Contains(text, "FOREIGN") || strings.Contains(strings.Join(d.requests(), " "), "ui-state") {
			t.Errorf("another pull request's draft %v: failed=%v %s %v", args, failed, text, d.requests())
		}
	}
	for _, args := range []map[string]any{{"pr": 13, "version": 2}, {"pr": 33, "version": 1}} {
		text, failed = data(t, s, token, "walkthrough_position", args)
		if !failed || strings.Contains(text, "looks off") {
			t.Errorf("another pull request's walkthrough %v: failed=%v %s", args, failed, text)
		}
	}
	// Its own pull request's walkthrough needs a saved review, which 12 lacks;
	// at an explicit version it reads its own key.
	if text, failed = data(t, s, token, "walkthrough_position", map[string]any{"version": 3}); failed || !strings.Contains(text, `"seen":[]`) {
		t.Errorf("its own walkthrough: %v %s", failed, text)
	}
	// Its own repository's other pull requests' files and commits are read.
	for tool, args := range map[string]map[string]any{
		"file":                   {"pr": 13, "path": "main.go"},
		"commit":                 {"pr": 13, "oid": "abcdef1"},
		"commit_patch":           {"pr": 13, "oid": "abcdef1", "path": "a.go"},
		"pull_request_decisions": {"pr": 13},
		"group_patch":            {"id": "g-web", "path": "a.go"},
		"group_file":             {"id": "g-web", "path": "a.go"},
	} {
		if text, failed := data(t, s, token, tool, args); failed {
			t.Errorf("%s of its own repository: %s", tool, text)
		}
	}
	// The tools exist for it.
	have := listed(t, s, token)
	for _, tool := range []string{"file", "commit", "commit_patch", "pull_request_decisions", "pending_review", "walkthrough_position", "group_patch", "group_file", "update_branch_options", "rerun_options"} {
		if !have[tool] {
			t.Errorf("an action does not list %s", tool)
		}
	}
}

// Each route the extras read is bound to the action's repository (or its
// pull request's key) before the daemon answers; this reaches the binding, not
// a schema.
func TestActionRoutesOfTheExtraTools(t *testing.T) {
	d := extrasDaemon{&fakeDaemon{}}
	clone := func(_ context.Context, id string) (string, error) {
		return map[string]string{"c-web": "acme/web", "c-evil": "evil/secrets"}[id], nil
	}
	reader := NewReader(d, clone).within([]string{"/api"}, "acme/web")
	reader.pull = 12
	get := func(target string) error {
		d.reset()
		_, err := reader.Get(context.Background(), target)
		if err != nil && len(d.requests()) > 0 && !strings.Contains(target, "/groups/") {
			t.Errorf("%s reached the daemon after a refusal: %v", target, d.requests())
		}
		return err
	}
	for _, route := range []string{"/api/file?path=a.go", "/api/commit?oid=abcdef1", "/api/commit-patch?oid=abcdef1&path=a.go", "/api/facts/commit?oid=abcdef1&head=h", "/api/facts/commit-patch?oid=abcdef1&path=a.go&head=h"} {
		if err := get(route + "&clone=c-web&pr=12"); err != nil {
			t.Errorf("%s of its clone: %v", route, err)
		}
		for _, bad := range []string{route + "&clone=c-evil&pr=12", route + "&pr=12", route + "&clone=c-web&clone=c-evil", route + "&clone=unknown", route + "&clone=c-web&other=1"} {
			if err := get(bad); err == nil {
				t.Errorf("%s was read", bad)
			}
		}
	}
	// The decisions of a pull request, never of a session (which names no repository).
	if err := get("/api/decisions?repository=acme%2Fweb&pr=13"); err != nil {
		t.Errorf("decisions: %v", err)
	}
	for _, bad := range []string{"/api/decisions?repository=evil%2Fsecrets&pr=1", "/api/decisions?agent=claude&session=s1", "/api/decisions?repository=acme%2Fweb&agent=claude&session=s1", "/api/decisions?pr=12"} {
		if err := get(bad); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
	// A group's patch and file: the group's repository is read first.
	for _, route := range []string{"patch", "file"} {
		if err := get("/api/groups/g-web/" + route + "?path=a.go&version=1"); err != nil {
			t.Errorf("group %s: %v", route, err)
		}
		for _, bad := range []string{"/api/groups/g-evil/" + route + "?path=a.go", "/api/groups/missing/" + route + "?path=a.go", "/api/groups/g-web/" + route + "?path=a.go&other=1"} {
			if err := get(bad); err == nil {
				t.Errorf("%s was read", bad)
			}
		}
	}
	if strings.Contains(strings.Join(d.requests(), " "), "/g-evil/patch") || strings.Contains(strings.Join(d.requests(), " "), "/g-evil/file") {
		t.Errorf("another repository's group was read: %v", d.requests())
	}
	// The kept state: the action's own pull request's key, at any version for
	// the walkthrough, and nothing else, whoever's it looks like.
	for _, ok := range []string{"/api/ui-state/review?key=acme%2Fweb%2312", "/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v1", "/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v20"} {
		if err := get(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"/api/ui-state/review", "/api/ui-state/review?key=", "/api/ui-state/review?key=acme%2Fweb%2313", "/api/ui-state/review?key=acme%2Fweb%23120",
		"/api/ui-state/review?key=ACME%2Fweb%2312", "/api/ui-state/review?key=evil%2Fsecrets%2312", "/api/ui-state/review?key=acme%2Fweb%2312&key=acme%2Fweb%2313",
		"/api/ui-state/review?key=acme%2Fweb%2312&x=1", "/api/ui-state/review?key=acme%2Fweb%2312%40v1",
		"/api/ui-state/walkthrough?key=acme%2Fweb%2312", "/api/ui-state/walkthrough?key=acme%2Fweb%2313%40v1", "/api/ui-state/walkthrough?key=acme%2Fweb%23120%40v1",
		"/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v", "/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v01", "/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v0",
		"/api/ui-state/walkthrough?key=acme%2Fweb%2312%40v1x", "/api/ui-state/walkthrough?key=evil%2Fsecrets%2312%40v1",
		// Every other kept state is the person's, not the action's.
		"/api/ui-state/inbox", "/api/ui-state/filters", "/api/ui-state/tour",
	} {
		if err := get(bad); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
	// An action with no pull request reads no kept state at all.
	free := NewReader(d, clone).within([]string{"/api"}, "acme/web")
	if _, err := free.Get(context.Background(), "/api/ui-state/review?key=acme%2Fweb%230"); err == nil {
		t.Error("a draft was read without a pull request")
	}
}

func TestExtraToolsDeclareTheirRoutes(t *testing.T) {
	s, _ := extrasServer(t)
	want := map[string][]string{
		"file":                   {"/api/index", "/api/file"},
		"commit":                 {"/api/index", "/api/facts", "/api/commit"},
		"commit_patch":           {"/api/index", "/api/facts", "/api/commit-patch"},
		"pull_request_decisions": {"/api/decisions"},
		"pending_review":         {"/api/ui-state/review"},
		"walkthrough_position":   {"/api/index", "/api/ui-state/walkthrough"},
	}
	got := s.ToolReads()
	for tool, reads := range want {
		if !reflect.DeepEqual(got[tool], reads) {
			t.Errorf("%s reads %v, want %v", tool, got[tool], reads)
		}
	}
}
