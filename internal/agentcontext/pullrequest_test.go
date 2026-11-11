package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon answers the routes the pull request tools read, for two
// repositories: acme/web (pull request 12 has only facts, 13 a saved review)
// and evil/secrets. It refuses anything but GET and records each request.
type fakeDaemon struct {
	mu   sync.Mutex
	seen []string
}

func (d *fakeDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

func (d *fakeDaemon) reset() {
	d.mu.Lock()
	d.seen = nil
	d.mu.Unlock()
}

const fakeIndex = `{"entries":[
 {"pull_request":{"repository":"acme/web","number":12,"head_sha":"h12","title":"Add search","unresolved":2},"clone_id":"c-web","status":"none","version_count":0},
 {"pull_request":{"repository":"acme/web","number":13,"head_sha":"h13","title":"Fix search"},"clone_id":"c-web","status":"ready","version_count":2},
 {"pull_request":{"repository":"evil/secrets","number":1,"head_sha":"hx","title":"Steal the keys"},"clone_id":"c-evil","status":"none","version_count":0}],
 "problems":[{"clone_id":"c-evil","reason":"secret reason"}]}`

const fakeInbox = `{"count":2,"items":[
 {"key":"acme/web#12","kind":"fix","fixing":true,"inboxes":["mine"]},
 {"key":"evil/secrets#1","kind":"review","inboxes":["mine"]}],
 "inboxes":[{"id":"mine","query":"secret query","count":2}]}`

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	// The clone routes take clone and pr, and the facts a head.
	clonePR := func() (string, string) { return q.Get("clone"), q.Get("pr") }
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/index":
		reply("%s", fakeIndex)
	case p == "/api/inbox":
		reply("%s", fakeInbox)
	case strings.HasPrefix(p, "/api/prs/"):
		// What each GitHub-reading route answers names its request, so a test
		// sees which repository and pull request were read.
		reply(`{"route":%q,"repository":%q,"pr":%q,"head_sha":%q,"unresolved":%q,"checks":[{"name":"test","status":"completed","conclusion":"failure"}],"threads":[]}`,
			p, q.Get("repository"), q.Get("pr"), q.Get("head_sha"), q.Get("unresolved"))
	case p == "/api/facts" || p == "/api/document":
		clone, pr := clonePR()
		head := map[string]string{"12": "h12", "13": "h13"}[pr]
		if clone == "" || head == "" || (p == "/api/facts" && q.Get("head") == "" && false) {
			http.NotFound(w, r)
			return
		}
		reply(`{"review":{"head":%q,"clone":%q},"index":{"hunks":[
		 {"path":"a.go","change":"modified","additions":3,"deletions":1},
		 {"path":"a.go","change":"modified","additions":2,"deletions":0},
		 {"path":"b.go","old_path":"old.go","change":"renamed","additions":0,"deletions":0}]}}`, head, clone)
	case p == "/api/patch" || p == "/api/facts/patch":
		clone, pr := clonePR()
		reply(`{"patch":"@@ %s %s %s %s"}`, p, clone, pr, q.Get("path"))
	case p == "/api/commits" || p == "/api/facts/commits":
		clone, pr := clonePR()
		reply(`[{"oid":"c1","subject":"commit of %s %s head %s","links":[{"session":"s1","method":"commit_ref"}]}]`, clone, pr, q.Get("head"))
	case p == "/api/sessions" || p == "/api/facts/sessions":
		clone, pr := clonePR()
		reply(`{"available":true,"sessions":[{"id":"s1","title":"session of %s %s"}],"commits":[]}`, clone, pr)
	case strings.HasPrefix(p, "/api/sessions/") || strings.HasPrefix(p, "/api/facts/sessions/"):
		if id := p[strings.LastIndex(p, "/")+1:]; id != "s1" {
			http.NotFound(w, r)
			return
		}
		clone, pr := clonePR()
		reply(`{"id":"s1","turns":["transcript of %s %s via %s"]}`, clone, pr, p)
	case p == "/api/jobs":
		reply(`{"available":true,"jobs":[{"id":"j-web","kind":"fix","state":"done","repository":%q,"pr":%q,"has_transcript":true,"accounting":{"secret":1},"summary":"fixed"}]}`, q.Get("repository"), q.Get("pr"))
	case strings.HasPrefix(p, "/api/jobs/"):
		id, rest, _ := strings.Cut(strings.TrimPrefix(p, "/api/jobs/"), "/")
		jobs := map[string]string{
			"j-web":      `{"id":"j-web","repository":"acme/web","pr":12,"has_transcript":true}`,
			"j-web13":    `{"id":"j-web13","repository":"acme/web","pr":13,"has_transcript":true}`,
			"j-review":   `{"id":"j-review","repository":"acme/web","pr":12}`,
			"j-evil":     `{"id":"j-evil","repository":"evil/secrets","pr":1,"has_transcript":true}`,
			"j-evil-pr":  `{"id":"j-evil-pr","repository":"evil/secrets","pr":12,"has_transcript":true}`,
			"j-no-group": `{"id":"j-no-group","repository":"acme/web","pr":0,"has_transcript":true}`,
		}
		job, ok := jobs[id]
		switch {
		case !ok:
			http.NotFound(w, r)
		case rest == "":
			reply("%s", job)
		case rest == "transcript":
			reply(`{"entries":[{"seq":4,"text":"transcript of %s after %s"}],"next":4,"more":false,"done":true}`, id, q.Get("after"))
		default:
			http.NotFound(w, r)
		}
	case strings.HasPrefix(p, "/api/groups/"):
		id, rest, _ := strings.Cut(strings.TrimPrefix(p, "/api/groups/"), "/")
		groups := map[string]string{
			"g-web":  `{"id":"g-web","repository":"acme/web","name":"Search","members":[12,13],"stack":false,"landed":[],"outdated":true}`,
			"g-evil": `{"id":"g-evil","repository":"evil/secrets","name":"Loot","members":[1,2],"landed":[],"outdated":false}`,
		}
		group, ok := groups[id]
		switch {
		case !ok:
			http.NotFound(w, r)
		case rest == "":
			reply("%s", group)
		case rest == "versions":
			reply(`[{"version":1,"group":%q}]`, id)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func fakeServer(t *testing.T) (*Server, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{}
	clones := map[string]string{"c-web": "acme/web", "c-evil": "evil/secrets"}
	s := New(Options{API: d, Version: "test", Clone: func(_ context.Context, id string) (string, error) {
		if repository, ok := clones[id]; ok {
			return repository, nil
		}
		return "", errors.New("unknown clone")
	}})
	return s, d
}

// data is a tool call's text between the fence, and whether it failed.
func data(t *testing.T, s *Server, token, name string, args any) (string, bool) {
	t.Helper()
	r := call(t, s, token, name, args)
	text, _ := unwrap(t, r.Content[0].Text)
	return text, r.IsError
}

func TestPullRequestToolsReadThePageRoutes(t *testing.T) {
	s, d := fakeServer(t)
	token, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		tool string
		args any
		want []string
		not  []string
	}{
		// The header from the review index, the description and the inbox.
		{"pull_request", nil, []string{`"title":"Add search"`, `"number":12`, `"route":"/api/prs/description"`, `"kind":"fix"`, `"fixing":true`, `"version_count":0`}, []string{"c-web", "Steal the keys", "secret query"}},
		{"pull_request", map[string]any{"pr": 13}, []string{`"title":"Fix search"`, `"version_count":2`, `"inbox":null`}, []string{`"kind":"fix"`}},
		// Without a review: the facts' files, and each file's patch from the facts.
		{"diff", nil, []string{`"source":"facts"`, `"head":"h12"`, `"path":"a.go"`, `"additions":5`, `"deletions":1`, `"hunks":2`, `"old_path":"old.go"`, `"@@ /api/facts/patch c-web 12 a.go"`, `"@@ /api/facts/patch c-web 12 b.go"`}, nil},
		{"diff", map[string]any{"path": "b.go"}, []string{`"path":"b.go"`, `"@@ /api/facts/patch c-web 12 b.go"`}, []string{`"path":"a.go"`}},
		// With a review: the document's files and the version's patches.
		{"diff", map[string]any{"pr": 13, "path": "a.go"}, []string{`"source":"review"`, `"head":"h13"`, `"@@ /api/patch c-web 13 a.go"`}, []string{`"path":"b.go"`}},
		{"commits", nil, []string{`commit of c-web 12 head h12`}, nil},
		{"commits", map[string]any{"pr": 13}, []string{`commit of c-web 13 head `}, nil},
		{"sessions", nil, []string{`session of c-web 12`}, nil},
		{"sessions", map[string]any{"pr": 13}, []string{`session of c-web 13`}, nil},
		{"session_transcript", map[string]any{"id": "s1"}, []string{`transcript of c-web 12 via /api/facts/sessions/s1`}, nil},
		{"session_transcript", map[string]any{"id": "s1", "pr": 13}, []string{`transcript of c-web 13 via /api/sessions/s1`}, nil},
		{"checks", nil, []string{`"head_sha":"h12"`, `"checks":[{"name":"test"`, `"failing":[{"name":"test"`}, nil},
		// The app names the unresolved count discovery has.
		{"review_threads", nil, []string{`"route":"/api/prs/review-threads"`, `"unresolved":"2"`, `"repository":"acme/web"`}, nil},
		{"merge_state", nil, []string{`"merge":{"route":"/api/prs/merge-options"`, `/api/prs/merge-options`, `/api/prs/update-branch-options`, `/api/prs/rerun-checks-options`}, nil},
		{"jobs", nil, []string{`"id":"j-web"`, `"state":"done"`, `"summary":"fixed"`, `"has_transcript":true`}, []string{"accounting", "secret"}},
		{"job_transcript", map[string]any{"id": "j-web", "after": 3}, []string{`transcript of j-web after 3`}, nil},
		{"group", map[string]any{"id": "g-web"}, []string{`"name":"Search"`, `"members":[12,13]`, `"outdated":true`}, nil},
		{"group_versions", map[string]any{"id": "g-web"}, []string{`"group":"g-web"`}, nil},
	} {
		d.reset()
		text, failed := data(t, s, token, tt.tool, tt.args)
		if failed {
			t.Errorf("%s %v: %s", tt.tool, tt.args, text)
			continue
		}
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s %v lacks %s:\n%s", tt.tool, tt.args, want, text)
			}
		}
		for _, not := range tt.not {
			if strings.Contains(text, not) {
				t.Errorf("%s %v has %s:\n%s", tt.tool, tt.args, not, text)
			}
		}
		for _, request := range d.requests() {
			if !strings.HasPrefix(request, "GET ") {
				t.Errorf("%s sent %s", tt.tool, request)
			}
		}
	}
	// Another repository's pull request is not in this one's reach by name
	// alone: a chat may name it, and gets only what the route answers.
	if text, failed := data(t, s, token, "pull_request", map[string]any{"repository": "evil/secrets", "pr": 1}); failed || !strings.Contains(text, "Steal the keys") {
		t.Errorf("a chat could not read another repository: %s", text)
	}
	if text, failed := data(t, s, token, "pull_request", map[string]any{"pr": 99}); !failed || !strings.Contains(text, "not in the review index") {
		t.Errorf("an unknown pull request: %s", text)
	}
}

func TestFocusIsThePagesOwn(t *testing.T) {
	s, _ := fakeServer(t)
	token, id := chat(t, s)
	if text, failed := data(t, s, token, "focus", nil); failed || text != "none" {
		t.Fatalf("no page: %q", text)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	if text, _ := data(t, s, token, "focus", nil); text != "none" {
		t.Fatalf("no focus: %q", text)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12, Focus: json.RawMessage(`{"section":"s2","stop":3,"overview":false,"anchors":["h_1"]}`)}); err != nil {
		t.Fatal(err)
	}
	text, _ := data(t, s, token, "focus", nil)
	if text != `{"page":"pull_request","focus":{"section":"s2","stop":3,"overview":false,"anchors":["h_1"]}}` {
		t.Fatalf("focus %s", text)
	}
	// An action's page is its pull request, with no focus.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	if text, _ := data(t, s, action, "focus", nil); text != "none" {
		t.Fatalf("an action's focus: %q", text)
	}
}

// An action's pull request tools default to its own pull request, read
// others in its repository, and are refused any other repository.
func TestActionToolsAreBoundToItsRepository(t *testing.T) {
	s, d := fakeServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	names := listed(t, s, action)
	for _, tool := range []string{"pull_request", "diff", "commits", "checks", "review_threads", "merge_state", "jobs", "job_transcript", "sessions", "session_transcript", "focus", "group", "group_versions"} {
		if !names[tool] {
			t.Errorf("an action cannot list %s", tool)
		}
	}
	args := map[string]map[string]any{
		"session_transcript": {"id": "s1"},
		"job_transcript":     {"id": "j-web"},
		"group":              {"id": "g-web"},
		"group_versions":     {"id": "g-web"},
	}
	for _, tool := range []string{"pull_request", "diff", "commits", "checks", "review_threads", "merge_state", "jobs", "job_transcript", "sessions", "session_transcript", "group", "group_versions"} {
		d.reset()
		text, failed := data(t, s, action, tool, args[tool])
		if failed {
			t.Errorf("%s by default: %s", tool, text)
		}
		for _, request := range d.requests() {
			// Everything it read is its repository's: its clone, its
			// repository name, or an id the reader bound.
			if strings.Contains(request, "evil") || strings.Contains(request, "c-evil") || strings.Contains(request, "pr=1&") {
				t.Errorf("%s read %s", tool, request)
			}
		}
		if text, failed := data(t, s, action, tool, merged(args[tool], map[string]any{"repository": "evil/secrets"})); !failed || !strings.Contains(text, "own pull request's repository") {
			t.Errorf("%s of another repository: %v %s", tool, failed, text)
		}
		if strings.Contains(strings.Join(d.requests(), " "), "evil") {
			t.Errorf("%s read another repository: %v", tool, d.requests())
		}
		// The repository's own name, in any case, is its own.
		if tool != "group" && tool != "group_versions" {
			other := merged(args[tool], map[string]any{"repository": "ACME/web", "pr": 13})
			if tool == "job_transcript" {
				other["id"] = "j-web13"
			}
			if text, failed := data(t, s, action, tool, other); failed {
				t.Errorf("%s of another pull request of its repository: %s", tool, text)
			}
		}
	}
	// The default is the action's own pull request.
	d.reset()
	if text, _ := data(t, s, action, "checks", nil); !strings.Contains(text, `"number":12`) {
		t.Fatalf("checks of %s", text)
	}
	if !slices.ContainsFunc(d.requests(), func(r string) bool {
		return strings.Contains(r, "/api/prs/checks?head_sha=h12&pr=12&repository=acme%2Fweb")
	}) {
		t.Fatalf("requests %v", d.requests())
	}
	// A tool cannot be steered with its arguments to a pull request of
	// another repository: the repository is the action's, whatever else.
	if text, failed := data(t, s, action, "pull_request", map[string]any{"repository": "evil/secrets", "pr": 1}); !failed {
		t.Fatalf("an action read another repository: %s", text)
	}
}

func merged(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestJobTranscriptChecksTheJob(t *testing.T) {
	s, d := fakeServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	chatToken, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{action, chatToken} {
		for _, job := range []string{"j-evil", "j-evil-pr", "j-web13", "j-no-group", "missing", "../j-web", "j-web/transcript"} {
			d.reset()
			text, failed := data(t, s, token, "job_transcript", map[string]any{"id": job})
			if !failed || strings.Contains(text, "transcript of") {
				t.Errorf("job %s: %v %s", job, failed, text)
			}
			for _, request := range d.requests() {
				if strings.Contains(request, "/transcript") {
					t.Errorf("job %s: read %s", job, request)
				}
			}
		}
		// A job without a transcript says so; a fix job's is read.
		if text, failed := data(t, s, token, "job_transcript", map[string]any{"id": "j-review"}); failed || !strings.Contains(text, "only fix jobs do") {
			t.Errorf("a review job: %v %s", failed, text)
		}
		if text, failed := data(t, s, token, "job_transcript", map[string]any{"id": "j-web"}); failed || !strings.Contains(text, "transcript of j-web after") {
			t.Errorf("a fix job: %v %s", failed, text)
		}
		// Another pull request's job is that pull request's to read.
		if text, failed := data(t, s, token, "job_transcript", map[string]any{"id": "j-web13", "pr": 13}); failed || !strings.Contains(text, "transcript of j-web13") {
			t.Errorf("a job of the pull request named: %v %s", failed, text)
		}
	}
}

func TestGroupToolsRefuseAnotherRepositorysGroup(t *testing.T) {
	s, d := fakeServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	chatToken, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PageGroup, Repository: "acme/web", Group: "g-web"}); err != nil {
		t.Fatal(err)
	}
	// A chat on a group page defaults to that group; an action has none.
	if text, failed := data(t, s, chatToken, "group", nil); failed || !strings.Contains(text, `"name":"Search"`) {
		t.Errorf("the open group: %v %s", failed, text)
	}
	if text, failed := data(t, s, action, "group", nil); !failed || !strings.Contains(text, "name a group") {
		t.Errorf("an action's default group: %v %s", failed, text)
	}
	for _, token := range []string{action, chatToken} {
		for _, tool := range []string{"group", "group_versions"} {
			d.reset()
			text, failed := data(t, s, token, tool, map[string]any{"id": "g-evil"})
			if !failed || strings.Contains(text, "Loot") || strings.Contains(text, `"members":[1,2]`) {
				t.Errorf("%s of a foreign group: %v %s", tool, failed, text)
			}
			for _, request := range d.requests() {
				if strings.Contains(request, "/versions") {
					t.Errorf("%s read the foreign group's versions: %s", tool, request)
				}
			}
		}
	}
}

// What the reader gives an action of the routes that answer for every
// repository is its own repository's part.
func TestActionReaderFiltersAllRepositoryRoutes(t *testing.T) {
	d := &fakeDaemon{}
	reader := NewReader(d, nil).within([]string{"/api/index", "/api/inbox"}, "ACME/web")
	response, err := reader.Get(context.Background(), "/api/index?history=1")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Entries []struct {
			PullRequest struct{ Repository string } `json:"pull_request"`
		}
		Problems []any
	}
	if err := json.Unmarshal(response.Body, &index); err != nil || len(index.Entries) != 2 || len(index.Problems) != 0 {
		t.Fatalf("index %s: %v", response.Body, err)
	}
	for _, e := range index.Entries {
		if e.PullRequest.Repository != "acme/web" {
			t.Fatalf("another repository's entry: %s", response.Body)
		}
	}
	response, err = reader.Get(context.Background(), "/api/inbox")
	if err != nil || strings.Contains(string(response.Body), "evil") || strings.Contains(string(response.Body), "secret query") || !strings.Contains(string(response.Body), `"count":1`) {
		t.Fatalf("inbox %s: %v", response.Body, err)
	}
	// A route that answers an error, or not JSON this reader can filter,
	// gives nothing.
	broken := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) })
	if response, err := NewReader(broken, nil).within([]string{"/api/index"}, "acme/web").Get(context.Background(), "/api/index"); err == nil {
		t.Fatalf("an unfilterable body came back: %s", response.Body)
	}
	missing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := NewReader(missing, nil).within([]string{"/api/index"}, "acme/web").Get(context.Background(), "/api/index"); err == nil {
		t.Fatal("a missing index came back")
	}
	// Parameters the entry does not list are refused.
	if _, err := reader.Get(context.Background(), "/api/index?history=1&repository=evil/secrets"); err == nil {
		t.Fatal("an unlisted parameter was read")
	}
}

// Every route an action's tool reads is in the action route table, bound as
// its handler reads it, and every clone route is bound to its clone.
func TestActionRouteTableCoversTheToolsAndBindsClones(t *testing.T) {
	s, d := fakeServer(t)
	// A clone of another repository is refused on every clone route.
	for _, path := range []string{"/api/versions", "/api/document", "/api/patch", "/api/commits", "/api/sessions", "/api/sessions/s1", "/api/facts", "/api/facts/patch", "/api/facts/commits", "/api/facts/sessions", "/api/facts/sessions/s1"} {
		for clone, allowed := range map[string]bool{"c-web": true, "c-evil": false, "nope": false} {
			reader := s.reader.within([]string{"/api"}, "acme/web")
			d.reset()
			_, err := reader.Get(context.Background(), path+"?"+url.Values{"clone": {clone}, "pr": {"12"}}.Encode())
			if (err == nil) != allowed || (!allowed && len(d.requests()) != 0) {
				t.Errorf("%s with clone %s: %v, reads %v", path, clone, err, d.requests())
			}
		}
	}
}

func TestToolsReadOnlyKnownRoutes(t *testing.T) {
	s := New(Options{API: http.NotFoundHandler()})
	reads := s.ToolReads()
	for _, tool := range []string{"pull_request", "diff", "commits", "checks", "review_threads", "merge_state", "jobs", "job_transcript", "sessions", "session_transcript", "focus", "group", "group_versions"} {
		prefixes, ok := reads[tool]
		if !ok {
			t.Errorf("%s is not registered", tool)
		}
		for _, prefix := range prefixes {
			for _, hidden := range unreadable {
				if strings.HasPrefix(prefix, hidden) || strings.HasPrefix(hidden, prefix) {
					t.Errorf("%s reads %s, which is under or over %s", tool, prefix, hidden)
				}
			}
			// What an action may read of it is in the table.
			if !slices.ContainsFunc(actionRoutes, func(r actionRoute) bool {
				return strings.HasPrefix(r.pattern, prefix) && (len(r.pattern) == len(prefix) || r.pattern[len(prefix)] == '/')
			}) {
				t.Errorf("%s reads %s, which no action route covers", tool, prefix)
			}
		}
	}
	for name := range reads {
		if strings.Contains(name, "propose") {
			t.Errorf("%s proposes", name)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tool := range s.tools {
		if tool.Proposes {
			t.Errorf("%s proposes", tool.Name)
		}
	}
}
