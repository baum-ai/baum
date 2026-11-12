package agentcontext

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// commonDaemon answers the routes the common tools read.
type commonDaemon struct {
	mu   sync.Mutex
	seen []string
	// down lists the route paths that answer 503; off those that answer 200
	// {"available":false}, as a daemon without the capability does.
	down map[string]bool
	off  map[string]bool
	// pulls is how many pull requests the index holds, besides the named ones.
	pulls int
}

func (d *commonDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

func (d *commonDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	down, off := d.down[r.URL.Path], d.off[r.URL.Path]
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if off {
		_, _ = fmt.Fprint(w, `{"available":false,"code":"unavailable"}`)
		return
	}
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	// The timeline routes find what holds the query's words; this daemon has
	// only things about retries.
	retries := strings.Contains(strings.ToLower(r.URL.Query().Get("q")), "retry")
	switch r.URL.Path {
	case "/api/index":
		entries := []string{
			`{"pull_request":{"repository":"acme/web","number":12,"title":"Add checkout retry","author":"dana","head_ref":"retry"},"clone_id":"c-web"}`,
			`{"pull_request":{"repository":"acme/web","number":12,"title":"Add checkout retry","author":"dana","head_ref":"retry"},"clone_id":"c-other"}`,
			`{"pull_request":{"repository":"acme/web","number":13,"title":"Fix the retry loop","author":"lee","head_ref":"loop"},"clone_id":"c-web"}`,
		}
		for i := range d.pulls {
			entries = append(entries, fmt.Sprintf(`{"pull_request":{"repository":"acme/bulk","number":%d,"title":"Bulk item %d"}}`, i+1, i+1))
		}
		_, _ = fmt.Fprintf(w, `{"entries":[%s],"problems":[]}`, strings.Join(entries, ","))
	case "/api/groups":
		_, _ = fmt.Fprint(w, `{"available":true,"groups":[{"id":"g-1","repository":"acme/web","name":"Retry rollout","members":[12,13]}]}`)
	case "/api/live-sessions":
		_, _ = fmt.Fprint(w, `{"sessions":[{"agent":"claude","session_id":"s-1","title":"Retry the checkout","repository":"acme/web","branch":"retry","waiting_message":"secret"},{"agent":"codex","session_id":"s-2","repository":"acme/api","branch":"main"},{"agent":"codex","session_id":"s-3","repository":"acme/api","branch":"main"}]}`)
	case "/api/repositories":
		_, _ = fmt.Fprint(w, `[{"repository":"acme/web","clones":[{"path":"/home/dev/web"}]},{"repository":"acme/retry-lib","clones":[]}]`)
	case "/api/timeline/features":
		if !retries {
			_, _ = fmt.Fprint(w, `{"features":[],"next_cursor":null}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"features":[{"id":"f-1","title":"Checkout retries","repository":{"id":"acme/web","full_name":"acme/web"}}],"next_cursor":null}`)
	case "/api/timeline/decisions":
		if !retries {
			_, _ = fmt.Fprint(w, `{"decisions":[],"next_cursor":null}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"decisions":[{"id":"d-1","summary":"Retry only idempotent calls","category":"architecture","repository":{"id":"acme/web","full_name":"acme/web"}}],"next_cursor":null}`)
	case "/api/background":
		_, _ = fmt.Fprint(w, `[{"repository":"acme/web","last_success":"2026-11-12T10:00:00Z","next_refresh":"2026-11-12T10:01:00Z","failures":0},`+
			`{"repository":"acme/api","last_success":"0001-01-01T00:00:00Z","next_refresh":"2026-11-12T10:02:00Z","error":"open /home/dev/secret: denied","failures":3,"problems":[{"pr":4,"reason":"x"}]}]`)
	case "/api/jobs":
		_, _ = fmt.Fprint(w, `{"available":true,"jobs":[{"id":"j1","state":"queued","accounting":{"secret":1}},{"id":"j2","state":"queued"},{"id":"j3","state":"running"},{"id":"j4","state":"completed"}]}`)
	default:
		http.NotFound(w, r)
	}
}

func commonServer(t *testing.T, d *commonDaemon) (*Server, string) {
	t.Helper()
	s := New(Options{API: d, Version: "9.9.9"})
	token, _ := chat(t, s)
	return s, token
}

type searchOut struct {
	Hits []struct {
		Kind   string      `json:"kind"`
		Title  string      `json:"title"`
		Detail string      `json:"detail"`
		Opens  PageContext `json:"opens"`
	} `json:"hits"`
	Truncated   bool     `json:"truncated"`
	Unavailable []string `json:"unavailable"`
}

func search(t *testing.T, s *Server, token string, args map[string]any) (searchOut, string, bool) {
	t.Helper()
	text, failed := data(t, s, token, "search", args)
	var out searchOut
	if !failed {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatal(err, text)
		}
	}
	return out, text, failed
}

func TestSearchFindsWhatGoToFinds(t *testing.T) {
	d := &commonDaemon{}
	s, token := commonServer(t, d)
	out, text, failed := search(t, s, token, map[string]any{"query": "Retry"})
	if failed {
		t.Fatal(text)
	}
	got := map[string]PageContext{}
	var kinds []string
	for _, h := range out.Hits {
		kinds = append(kinds, h.Kind)
		got[h.Kind+":"+h.Title] = h.Opens
	}
	for key, want := range map[string]PageContext{
		"pull_request:Add checkout retry": {Page: PagePullRequest, Repository: "acme/web", PullRequest: 12},
		"group:Retry rollout":             {Page: PageGroup, Repository: "acme/web", Group: "g-1"},
		"session:Retry the checkout":      {Page: PageSession, Repository: "acme/web", Session: "claude:s-1"},
		"repository:acme/retry-lib":       {Page: PageRepositories, Repository: "acme/retry-lib"},
		"feature:Checkout retries":        {Page: PageFeature, Repository: "acme/web", Feature: "f-1"},
	} {
		if g, ok := got[key]; !ok || g.Page != want.Page || g.Repository != want.Repository || g.PullRequest != want.PullRequest || g.Group != want.Group || g.Session != want.Session || g.Feature != want.Feature {
			t.Errorf("%s: %+v, want %+v (hits %v)", key, g, want, kinds)
		}
	}
	if dec := got["decision:Retry only idempotent calls"]; dec.Page != PageTimeline || dec.Filters["q"] != "Retry" {
		t.Errorf("decision opens %+v", dec)
	}
	// Every hit's page feeds link, as the page fields current_page uses.
	for _, h := range out.Hits {
		args := map[string]any{"page": h.Opens.Page}
		for k, v := range map[string]any{"repository": h.Opens.Repository, "pull_request": h.Opens.PullRequest, "group": h.Opens.Group,
			"session": h.Opens.Session, "job": h.Opens.Job, "feature": h.Opens.Feature} {
			if v != "" && v != 0 {
				args[k] = v
			}
		}
		if len(h.Opens.Filters) > 0 {
			args["filters"] = h.Opens.Filters
		}
		if text, failed := data(t, s, token, "link", args); failed {
			t.Errorf("link refuses the page of %s %q: %v: %s", h.Kind, h.Title, args, text)
		}
	}
	// Sessions with the same title and detail are two hits.
	if o, _, _ := search(t, s, token, map[string]any{"query": "codex on main", "kinds": []string{"session"}}); len(o.Hits) != 2 {
		t.Errorf("two sessions of one title are %d hits", len(o.Hits))
	}
	// Two clones of a pull request are one hit; a session's private fields are
	// not returned; titles that start with the words come first.
	if n := strings.Count(text, "Add checkout retry"); n != 1 {
		t.Errorf("a pull request in two clones is %d hits", n)
	}
	if strings.Contains(text, "secret") || strings.Contains(text, "/home/") {
		t.Errorf("search returned more than titles: %s", text)
	}
	if out.Hits[0].Title != "Retry rollout" && out.Hits[0].Title != "Retry the checkout" && out.Hits[0].Title != "Retry only idempotent calls" {
		t.Errorf("a title that starts with the words is not first: %v", out.Hits[0])
	}
	// Every word must be found, in the title, repository or number.
	out, _, _ = search(t, s, token, map[string]any{"query": "acme/web #13 loop", "kinds": []string{"pull_request"}})
	if len(out.Hits) != 1 || out.Hits[0].Opens.PullRequest != 13 {
		t.Errorf("words across fields: %+v", out.Hits)
	}
	if out, _, _ = search(t, s, token, map[string]any{"query": "nothing matches this"}); len(out.Hits) != 0 {
		t.Errorf("hits for nothing: %+v", out.Hits)
	}
}

func TestSearchIsBounded(t *testing.T) {
	d := &commonDaemon{pulls: 80}
	s, token := commonServer(t, d)
	out, text, failed := search(t, s, token, map[string]any{"query": "bulk", "limit": 5})
	if failed || len(out.Hits) != 5 || !out.Truncated {
		t.Fatalf("limit 5: %d hits, truncated %v: %s", len(out.Hits), out.Truncated, text)
	}
	// The hits filled the limit, so the timeline was not asked.
	for _, r := range d.requests() {
		if strings.Contains(r, "/api/timeline/") {
			t.Errorf("a full search still read %s", r)
		}
	}
	if out, _, _ = search(t, s, token, map[string]any{"query": "bulk"}); len(out.Hits) != defaultSearchLimit || !out.Truncated {
		t.Errorf("default limit: %d hits", len(out.Hits))
	}
	if out, _, _ = search(t, s, token, map[string]any{"query": "bulk", "limit": 50}); len(out.Hits) != 50 {
		t.Errorf("limit 50: %d hits", len(out.Hits))
	}
	for _, args := range []map[string]any{
		{"query": "bulk", "limit": 51}, {"query": "bulk", "limit": 0}, {"query": ""}, {"limit": 3}, {"query": "x", "kinds": []string{"job"}}, {"query": "x", "extra": 1},
	} {
		if _, text, failed := search(t, s, token, args); !failed {
			t.Errorf("%v accepted: %s", args, text)
		}
	}
	// Only the asked kinds are read.
	d.mu.Lock()
	d.seen = nil
	d.mu.Unlock()
	search(t, s, token, map[string]any{"query": "retry", "kinds": []string{"group"}})
	if r := d.requests(); len(r) != 1 || !strings.Contains(r[0], "/api/groups") {
		t.Errorf("a search of groups read %v", r)
	}
}

func TestSearchNamesWhatItCouldNotRead(t *testing.T) {
	d := &commonDaemon{down: map[string]bool{"/api/live-sessions": true, "/api/timeline/decisions": true}}
	s, token := commonServer(t, d)
	out, text, failed := search(t, s, token, map[string]any{"query": "retry"})
	if failed || len(out.Hits) == 0 || !slices.Equal(out.Unavailable, []string{"sessions", "decisions"}) {
		t.Fatalf("%v %s", failed, text)
	}
	d.down = map[string]bool{"/api/live-sessions": true}
	if _, text, failed := search(t, s, token, map[string]any{"query": "retry", "kinds": []string{"session"}}); !failed || !strings.Contains(text, "could not be searched") {
		t.Errorf("a search that read nothing succeeded: %s", text)
	}
}

func TestLinkBuildsTheAppsAddresses(t *testing.T) {
	s, token := commonServer(t, &commonDaemon{})
	for _, tt := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"page": "inbox"}, "?page=inbox"},
		{map[string]any{"page": "pulls"}, "?page=pulls"},
		{map[string]any{"page": "pulls", "filters": map[string]string{"filter": "history", "repo": "acme/web"}}, "?page=pulls&filter=history&repo=acme%2Fweb"},
		{map[string]any{"page": "timeline"}, "?page=timeline&who=human"},
		{map[string]any{"page": "timeline", "filters": map[string]string{"tab": "all"}}, "?page=timeline&tab=all"},
		{map[string]any{"page": "sessions"}, "?page=sessions"},
		{map[string]any{"page": "analytics", "filters": map[string]string{"days": "30"}}, "?page=analytics&days=30"},
		{map[string]any{"page": "jobs"}, "?page=jobs"},
		{map[string]any{"page": "repositories"}, "?page=repositories"},
		{map[string]any{"page": "repositories", "repository": "acme/web", "section": "prompts"}, "?page=repositories&repository=acme%2Fweb&section=prompts"},
		{map[string]any{"page": "settings"}, "?page=settings"},
		{map[string]any{"page": "settings", "section": "connections"}, "?page=settings&section=connections"},
		{map[string]any{"page": "pull_request", "repository": "acme/web", "pull_request": 42}, "?repository=acme%2Fweb&pr=42"},
		{map[string]any{"page": "group", "group": "g 1"}, "?group=g+1"},
		{map[string]any{"page": "session", "session": "claude:s-1"}, "?page=sessions&session=claude%3As-1"},
		{map[string]any{"page": "job", "job": "j-1"}, "?page=jobs&job=j-1"},
		{map[string]any{"page": "feature", "feature": "f-1"}, "?page=timeline&feature=f-1"},
	} {
		text, failed := data(t, s, token, "link", tt.args)
		var out struct{ Page, Address string }
		if failed || json.Unmarshal([]byte(text), &out) != nil || out.Address != tt.want {
			t.Errorf("%v: %s (%v), want %s", tt.args, text, failed, tt.want)
		}
	}
}

func TestLinkRefusesWhatItCannotBuild(t *testing.T) {
	s, token := commonServer(t, &commonDaemon{})
	for name, args := range map[string]map[string]any{
		"unknown page":              {"page": "nowhere"},
		"pull request, no number":   {"page": "pull_request", "repository": "acme/web"},
		"pull request, no repo":     {"page": "pull_request", "pull_request": 4},
		"group, no id":              {"page": "group"},
		"session, no id":            {"page": "session"},
		"job, no id":                {"page": "job"},
		"feature, no id":            {"page": "feature"},
		"subject of another page":   {"page": "pulls", "group": "g-1"},
		"unknown settings section":  {"page": "settings", "section": "nope"},
		"unknown repository sect.":  {"page": "repositories", "repository": "acme/web", "section": "general"},
		"section without a repo":    {"page": "repositories", "section": "prompts"},
		"filter that is not a key":  {"page": "pulls", "filters": map[string]string{"a b": "c"}},
		"filter that sets the page": {"page": "pulls", "filters": map[string]string{"page": "settings"}},
		"repository, one word":      {"page": "pull_request", "repository": "web", "pull_request": 1},
	} {
		if text, failed := data(t, s, token, "link", args); !failed {
			t.Errorf("%s accepted: %s", name, text)
		}
	}
	if _, err := linkOf("nowhere", "", 0, "", "", "", "", "", nil); err == nil {
		t.Error("linkOf built an unknown page")
	}
}

func TestLinkRefusesAnotherRepositoryToAnAction(t *testing.T) {
	s, _ := commonServer(t, &commonDaemon{})
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	text, failed := data(t, s, action, "link", map[string]any{"page": "pull_request", "repository": "evil/secrets", "pull_request": 1})
	if !failed || !strings.Contains(text, errOtherRepository.Error()) {
		t.Fatalf("an action linked another repository: %s", text)
	}
	if text, failed = data(t, s, action, "link", map[string]any{"page": "repositories", "repository": "evil/secrets"}); !failed {
		t.Fatalf("an action linked another repository's page: %s", text)
	}
	if text, failed = data(t, s, action, "link", map[string]any{"page": "pull_request", "repository": "ACME/web", "pull_request": 14}); failed || !strings.Contains(text, "pr=14") {
		t.Fatalf("an action could not link its own repository: %s", text)
	}
}

func TestAppHelp(t *testing.T) {
	s, token := commonServer(t, &commonDaemon{})
	whole, failed := data(t, s, token, "app_help", nil)
	if failed || !strings.Contains(whole, "## shortcuts") || !strings.Contains(whole, "## sessions") {
		t.Fatalf("whole help: %.200s", whole)
	}
	for topic, want := range map[string]string{"sessions": "agent sessions", "shortcuts": "`Mod+K`", "Pull_Request": "walkthrough", "up next": "queue of pull requests", "decision": "choice made"} {
		text, failed := data(t, s, token, "app_help", map[string]any{"topic": topic})
		if failed || !strings.Contains(text, want) || strings.Contains(text, "## inbox") {
			t.Errorf("%s: %.200s", topic, text)
		}
	}
	if text, failed := data(t, s, token, "app_help", map[string]any{"topic": "billing"}); !failed || !strings.Contains(text, "shortcuts") {
		t.Errorf("unknown topic: %s", text)
	}
}

func TestDaemonStatusProjectsSafeFields(t *testing.T) {
	d := &commonDaemon{}
	s, token := commonServer(t, d)
	text, failed := data(t, s, token, "daemon_status", nil)
	if failed {
		t.Fatal(text)
	}
	var got daemonStatus
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err, text)
	}
	if got.Health != "ok" || got.Version != "9.9.9" || got.Jobs.Queued != 2 || got.Jobs.Running != 1 || len(got.Repositories) != 2 {
		t.Fatalf("%+v", got)
	}
	api := got.Repositories[1]
	if api.Repository != "acme/api" || !api.Failing || api.Failures != 3 || api.Problems != 1 || api.LastSuccess != "" || api.NextRefresh == "" {
		t.Fatalf("%+v", api)
	}
	for _, leak := range []string{"/home/", "secret", "denied", "accounting"} {
		if strings.Contains(text, leak) {
			t.Errorf("status leaks %q: %s", leak, text)
		}
	}
	// A queue that cannot be read is said so; background work that cannot is an error.
	d.down = map[string]bool{"/api/jobs": true}
	if text, failed = data(t, s, token, "daemon_status", nil); failed || !strings.Contains(text, `"unavailable":true`) {
		t.Errorf("jobs down: %v %s", failed, text)
	}
	d.down = map[string]bool{"/api/background": true}
	if _, failed = data(t, s, token, "daemon_status", nil); !failed {
		t.Error("background down was not an error")
	}
}

func TestCommonToolsAreOnEveryPage(t *testing.T) {
	s, _ := commonServer(t, &commonDaemon{})
	token, id, revoke := mint(t, s, Consumer{Kind: Chat})
	t.Cleanup(revoke)
	for page := range pageNames {
		if err := s.Registry.SetPage(id, PageContext{Page: page}); err != nil {
			t.Fatal(err)
		}
		text, _ := data(t, s, token, "current_page", nil)
		var got struct{ Tools []string }
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err, text)
		}
		for _, name := range commonToolNames {
			if !slices.Contains(got.Tools, name) {
				t.Errorf("page %s does not list %s: %v", page, name, got.Tools)
			}
		}
	}
	// Registering a page keeps them; the pages' own tools come first.
	if err := s.RegisterPage(PageSpec{Name: PagePulls}); err != nil {
		t.Fatal(err)
	}
	if spec, _ := s.page(PagePulls); !slices.Equal(spec.Tools, commonToolNames) {
		t.Errorf("registered page tools %v", spec.Tools)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tool := range s.tools {
		switch tool.Name {
		case "search", "daemon_status":
			if tool.Scope != ScopeGlobal || len(tool.Reads) == 0 {
				t.Errorf("%s: scope %v reads %v", tool.Name, tool.Scope, tool.Reads)
			}
		case "link", "app_help":
			if len(tool.Reads) != 0 {
				t.Errorf("%s reads %v", tool.Name, tool.Reads)
			}
		}
	}
}

func TestActionsLackSearchAndDaemonStatus(t *testing.T) {
	d := &commonDaemon{}
	s, _ := commonServer(t, d)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	names := listed(t, s, action)
	if names["search"] || names["daemon_status"] || !names["link"] || !names["app_help"] {
		t.Fatalf("an action lists %v", names)
	}
	for _, name := range []string{"search", "daemon_status"} {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{"query": "x"}})
		r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`)
		if r.Error == nil || r.Error.Code != codeInvalidParams {
			t.Errorf("an action called %s: %+v %s", name, r.Error, r.Result)
		}
	}
	if len(d.requests()) != 0 {
		t.Errorf("an action's refused calls read %v", d.requests())
	}
	text, _ := data(t, s, action, "current_page", nil)
	if strings.Contains(text, "search") || strings.Contains(text, "daemon_status") || !strings.Contains(text, "app_help") {
		t.Errorf("an action's page lists %s", text)
	}
}

// The app's sources are the truth about its pages and keys: every page link
// can build, and every key the app defines, is in the help.
const appSources = "../../app/src/"

func appSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(appSources + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func appPages(t *testing.T) []string {
	t.Helper()
	src := appSource(t, "pages.ts")
	_, table, ok := strings.Cut(src, "export const PAGES")
	if !ok {
		t.Fatal("pages.ts has no PAGES")
	}
	table, _, _ = strings.Cut(table, "\n];")
	var pages []string
	for _, m := range regexp.MustCompile(`\{\s*page:\s*"(\w+)"`).FindAllStringSubmatch(table, -1) {
		pages = append(pages, m[1])
	}
	if len(pages) < 8 {
		t.Fatalf("found %d pages in PAGES, the app has at least 8: %v", len(pages), pages)
	}
	return pages
}

// shortcutKeys are the keys shortcuts.tsx lists, as the help writes them.
func shortcutKeys(t *testing.T) []string {
	t.Helper()
	src := appSource(t, "shortcuts.tsx")
	names := map[string]string{"ArrowDown": "↓", "ArrowUp": "↑", "ArrowLeft": "←", "ArrowRight": "→", " ": "Space"}
	seen := map[string]bool{}
	var keys []string
	for _, at := range regexp.MustCompile(`\bkeys:`).FindAllStringIndex(src, -1) {
		// The literals of the expression up to its comma or closing brace.
		depth, quote := 0, byte(0)
		var literal []byte
		for i := at[1]; i < len(src); i++ {
			c := src[i]
			switch {
			case quote != 0 && c == '\\':
				literal = append(literal, src[i:i+2]...)
				i++
			case quote != 0 && c == quote:
				key := string(literal)
				if name, ok := names[key]; ok {
					key = name
				}
				key = strings.Replace(key, "${COMMAND}", "Mod+", 1)
				if !seen[key] {
					seen[key] = true
					keys = append(keys, key)
				}
				quote, literal = 0, nil
			case quote != 0:
				literal = append(literal, c)
			case c == '"' || c == '`':
				quote = c
			case c == '[' || c == '(':
				depth++
			case c == ']' || c == ')':
				depth--
			case (c == ',' || c == '}') && depth <= 0:
				i = len(src)
			}
		}
	}
	if len(keys) < 40 {
		t.Fatalf("found %d keys in shortcuts.tsx, the app has more than 40: %v", len(keys), keys)
	}
	return keys
}

func TestHelpAndLinkCoverTheAppsSources(t *testing.T) {
	names, sections := helpSections()
	for _, page := range appPages(t) {
		if _, ok := sections[page]; !ok {
			t.Errorf("app_help has no section for the page %s (sections %v)", page, names)
		}
		if _, err := linkOf(page, "", 0, "", "", "", "", "", nil); err != nil {
			t.Errorf("link cannot build the page %s: %v", page, err)
		}
	}
	// Every page the server knows has a link and a section too.
	for page := range pageNames {
		if _, ok := sections[page]; !ok {
			t.Errorf("app_help has no section for the page %s", page)
		}
		if _, ok := pageSubjects[page]; !ok {
			t.Errorf("link has no mapping for the page %s", page)
		}
	}
	for _, key := range shortcutKeys(t) {
		if !strings.Contains(sections["shortcuts"], "`"+key+"`") {
			t.Errorf("the shortcut %q is not in the help", key)
		}
	}
	// The sections the settings pages have.
	for file, want := range map[string][]string{"settings.tsx": settingsSections} {
		src := appSource(t, file)
		for _, section := range want {
			if !strings.Contains(src, `"`+section+`"`) {
				t.Errorf("%s has no section %s", file, section)
			}
		}
	}
	for _, word := range []string{"inbox", "up next", "group", "review", "fix", "session", "job", "timeline", "decision", "feature", "prompt"} {
		if !strings.Contains(sections["words"], "- **"+word+"**") {
			t.Errorf("the help does not explain %q", word)
		}
	}
}

func TestSettingsSectionsMatchTheApp(t *testing.T) {
	src := appSource(t, "settings.tsx")
	var found [][]string
	for _, name := range []string{"settingsSections", "repositorySections"} {
		_, rest, ok := strings.Cut(src, "const "+name+" = [")
		if !ok {
			t.Fatalf("settings.tsx has no %s", name)
		}
		list, _, _ := strings.Cut(rest, "]")
		var sections []string
		for _, m := range regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(list, -1) {
			sections = append(sections, m[1])
		}
		found = append(found, sections)
	}
	if !slices.Equal(found[0], settingsSections) || !slices.Equal(found[1], repositorySections) {
		t.Errorf("settings sections %v and %v, link knows %v and %v", found[0], found[1], settingsSections, repositorySections)
	}
}

func TestLinkFiltersCannotNameTheSubject(t *testing.T) {
	s, token := commonServer(t, &commonDaemon{})
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	for _, key := range []string{"repository", "clone", "pr", "group", "session", "job", "feature", "section", "inbox", "page"} {
		for _, page := range []string{"repositories", "settings", "timeline"} {
			args := map[string]any{"page": page, "filters": map[string]string{key: "evil/secrets"}}
			for who, tok := range map[string]string{"chat": token, "action": action} {
				if text, failed := data(t, s, tok, "link", args); !failed {
					t.Errorf("%s: %s with filter %s accepted: %s", who, page, key, text)
				}
			}
		}
	}
	// A filter that names repositories is another repository to an action.
	for _, repo := range []string{"evil/secrets", "acme/web,evil/secrets"} {
		text, failed := data(t, s, action, "link", map[string]any{"page": "timeline", "filters": map[string]string{"repo": repo}})
		if !failed || !strings.Contains(text, errOtherRepository.Error()) {
			t.Errorf("an action linked the timeline of %s: %s", repo, text)
		}
	}
	if text, failed := data(t, s, action, "link", map[string]any{"page": "timeline", "filters": map[string]string{"repo": "acme/web"}}); failed {
		t.Errorf("an action's own repository filter: %s", text)
	}
	if text, failed := data(t, s, token, "link", map[string]any{"page": "analytics", "filters": map[string]string{"repo": "any/thing"}}); failed {
		t.Errorf("a chat's repo filter: %s", text)
	}
}

func TestDisabledCapabilitiesAreUnavailable(t *testing.T) {
	d := &commonDaemon{off: map[string]bool{"/api/groups": true}}
	s, token := commonServer(t, d)
	out, text, failed := search(t, s, token, map[string]any{"query": "retry"})
	if failed || !slices.Equal(out.Unavailable, []string{"groups"}) {
		t.Fatalf("%v %s", failed, text)
	}
	if _, text, failed := search(t, s, token, map[string]any{"query": "retry", "kinds": []string{"group"}}); !failed || !strings.Contains(text, "could not be searched") {
		t.Errorf("a search of a disabled source succeeded: %s", text)
	}
	d.off = map[string]bool{"/api/background": true}
	if text, failed := data(t, s, token, "daemon_status", nil); !failed {
		t.Errorf("background off: %s", text)
	}
	d.off = map[string]bool{"/api/jobs": true}
	if text, failed := data(t, s, token, "daemon_status", nil); failed || !strings.Contains(text, `"unavailable":true`) || strings.Contains(text, `"queued":2`) {
		t.Errorf("jobs off: %v %s", failed, text)
	}
}

// link builds the addresses the app's own source builds: the sidebar pages'
// hrefs in pages.ts and the detail pages' hrefs.
func TestLinkMatchesTheAppsHrefs(t *testing.T) {
	src := appSource(t, "pages.ts")
	_, table, _ := strings.Cut(src, "export const PAGES")
	table, _, _ = strings.Cut(table, "\n];")
	entries := regexp.MustCompile(`page:\s*"(\w+)",\s*label:\s*"[^"]*",\s*href:\s*\(\)\s*=>\s*appURL\((?:"([^"]*)")?\)`).FindAllStringSubmatch(table, -1)
	if len(entries) != len(appPages(t)) {
		t.Fatalf("parsed %d hrefs for %d pages", len(entries), len(appPages(t)))
	}
	for _, e := range entries {
		got, err := linkOf(e[1], "", 0, "", "", "", "", "", nil)
		want := e[2]
		// The inbox's address is the bare path, which link names with its page.
		if want == "" {
			want = "?page=inbox"
		}
		if err != nil || got != want {
			t.Errorf("page %s: link %q (%v), the app %q", e[1], got, err, want)
		}
	}
	// The detail pages' hrefs, as templates in their sources, filled in.
	fill := func(template string, values ...string) string {
		i := 0
		return regexp.MustCompile(`\$\{[^}]*\}`).ReplaceAllStringFunc(template, func(string) string {
			i++
			return url.QueryEscape(values[i-1])
		})
	}
	for _, tt := range []struct {
		file, template string
		values         []string
		got            func() (string, error)
	}{
		{"timeline.tsx", "?repository=${encodeURIComponent(repository)}&pr=${number}", []string{"acme/web", "7"},
			func() (string, error) { return linkOf(PagePullRequest, "acme/web", 7, "", "", "", "", "", nil) }},
		{"api.ts", "?page=jobs&job=${encodeURIComponent(job.id)}", []string{"j-1"},
			func() (string, error) { return linkOf(PageJob, "", 0, "", "", "j-1", "", "", nil) }},
		{"sessions.tsx", "?page=sessions&session=${encodeURIComponent(sessionKey(s))}", []string{"claude:s-1"},
			func() (string, error) { return linkOf(PageSession, "", 0, "", "claude:s-1", "", "", "", nil) }},
		{"app.tsx", "?group=${encodeURIComponent(group.id)}", []string{"g-1"},
			func() (string, error) { return linkOf(PageGroup, "", 0, "g-1", "", "", "", "", nil) }},
		{"settings.tsx", "?page=repositories&repository=${encodeURIComponent(repository)}", []string{"acme/web"},
			func() (string, error) { return linkOf(PageRepositories, "acme/web", 0, "", "", "", "", "", nil) }},
	} {
		if !strings.Contains(appSource(t, tt.file), "`"+tt.template+"`") && !strings.Contains(appSource(t, tt.file), "appURL(`"+tt.template+"`)") {
			t.Errorf("%s no longer has the href %s: update link", tt.file, tt.template)
			continue
		}
		if got, err := tt.got(); err != nil || got != fill(tt.template, tt.values...) {
			t.Errorf("%s: link %q (%v), the app %q", tt.file, got, err, fill(tt.template, tt.values...))
		}
	}
}

// The keys shortcuts.tsx names by identifier, not literal, are resolved here:
// each definition is checked in the source so a change to one is caught, then
// the help must have the key.
func TestHelpHasTheKeysNamedByIdentifiers(t *testing.T) {
	src := appSource(t, "shortcuts.tsx")
	_, help := helpSections()
	shortcuts := help["shortcuts"]
	for _, tt := range []struct{ definition, key string }{
		{"export const REFRESH_KEY_LABEL = `${COMMAND}R`;", "Mod+R"},
		{`export const GITHUB_KEY_LABEL = MAC ? "⇧⌘G" : "Ctrl+Shift+G";`, "Mod+Shift+G"},
		{"const digit = /^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1];", "Mod+1"},
		{`if (event.key === ",") return { kind: "page", page: "settings" };`, "Mod+,"},
		{`if (letter === "k") return { kind: "go-to" };`, "Mod+K"},
	} {
		if !strings.Contains(src, tt.definition) {
			t.Errorf("shortcuts.tsx no longer has %s: update the help", tt.definition)
		}
		if !strings.Contains(shortcuts, "`"+tt.key+"`") {
			t.Errorf("the help lacks %s", tt.key)
		}
	}
	// The page keys run to the number of sidebar pages.
	if want := fmt.Sprintf("`Mod+1` to `Mod+%d`", len(appPages(t))); !strings.Contains(shortcuts, want) {
		t.Errorf("the help lacks %s", want)
	}
	// The pull request tabs' keys are a literal table.
	m := regexp.MustCompile(`VIEW_KEYS = \{ previous: \[([^\]]*)\], next: \[([^\]]*)\] \}`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("shortcuts.tsx has no VIEW_KEYS")
	}
	for _, key := range regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(m[1]+m[2], -1) {
		name := map[string]string{"ArrowLeft": "←", "ArrowRight": "→"}[key[1]]
		if name == "" {
			name = key[1]
		}
		if !strings.Contains(shortcuts, "`"+name+"`") {
			t.Errorf("the help lacks the view key %s", name)
		}
	}
}
