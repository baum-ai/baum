package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// sentinelDaemon answers the routes of the Analytics, Jobs, Repositories and
// Report a problem tools with a planted value in every field the tools must
// not pass on: a home folder, a token, an environment value.
type sentinelDaemon struct {
	mu   sync.Mutex
	seen []string
}

const (
	sentinelHome  = "/home/dev/.hidden-home"
	sentinelToken = "ghp_aB3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF3hJ6"
	sentinelEnv   = "SENTINEL_ENV_VALUE"
)

func (d *sentinelDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

func (d *sentinelDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q, p := r.URL.Query(), r.URL.Path
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	job := func(id, repository, state string, extra string) string {
		return fmt.Sprintf(`{"id":%q,"clone":"c1","repository":%q,"pr":7,"state":%q,"phase":"done","calls":3,"tokens":120,"repairs":0,"actual_cost_usd":0.25,"backend":"claude","model":"opus","config_digest":%q,"price_version":"pv","head_sha":"h","accounting":{"secret":%q},"created_at":"2026-11-12T09:00:00Z","updated_at":"2026-11-12T09:05:00Z"%s}`,
			id, repository, state, sentinelEnv, sentinelEnv, extra)
	}
	switch {
	case p == "/api/analytics":
		reply(`{"window":{"days":%s},"filters":{"repository":%q,"agent":%q},"options":{"repositories":["acme/web"],"agents":["codex"]},"indexing":{"done":1,"total":1,"complete":true},
"summary":{"sessions":2,"cost":{"usd":1.5,"incomplete":false,"reasons":[]}},"daily":[],"hours":[],"by_agent":[{"key":"codex","sessions":2}],"by_model":[],"by_repository":[{"key":"acme/web","sessions":2}],"longest":[],
"commits":{"share":0.5},"baum":{"available":true,"reviews":3},"delivery":{"status":"ready"},
"plan_usage":{"available":true,"series":[{"provider":"claude","window":"5-hour","points":[{"at":"2026-11-12T09:00:00Z","used_percent":10}],"peak":10,"limit_hits":0}]},
"flow":{"available":true,"yours":true,"unlisted":[],"unread":[],"days":[{"date":"2026-11-12","mine":{"opened":1,"opened_merged":1,"merged":1,"merge_seconds":[100.5]},"team":{"opened":2,"opened_merged":1,"merged":1,"merge_seconds":[100.5,200]}}]}}`, q.Get("days"), q.Get("repo"), q.Get("agent"))
	case p == "/api/jobs" && q.Get("before") == "":
		reply(`{"available":true,"jobs":[%s,%s,%s],"next":"j-old"}`,
			job("j-new", "acme/web", "running", ""),
			job("j-fail", "acme/web", "failed", `,"reason":"agent_failed","detail":"The agent stopped: `+sentinelToken+`","failed_sections":["sec_1"],"context_failed":true`),
			job("j-other", "other/api", "failed", `,"reason":"timeout"`))
	case p == "/api/jobs" && q.Get("before") == "j-old":
		reply(`{"available":true,"jobs":[%s]}`, job("j-last", "acme/web", "failed", `,"reason":"cancelled"`))
	case p == "/api/jobs" && q.Get("before") == "j-new":
		reply(`{"available":true,"jobs":[%s],"next":"j-fail"}`, job("j-fail", "acme/web", "failed", ""))
	case p == "/api/jobs" && q.Get("before") == "j-fail":
		reply(`{"available":true,"jobs":[]}`)
	case p == "/api/jobs/j-fail":
		reply("%s", job("j-fail", "acme/web", "failed", `,"reason":"agent_failed","detail":"The agent stopped: `+sentinelToken+`","failed_sections":["sec_1"],"context_failed":true,"fix":{"head_ref":"`+sentinelEnv+`"}`))
	case p == "/api/jobs/j-new":
		reply("%s", job("j-new", "acme/web", "running", `,"started_at":"2026-11-12T09:01:00Z"`))
	case p == "/api/jobs/j-other":
		reply("%s", job("j-other", "other/api", "failed", `,"reason":"timeout"`))
	case p == "/api/repositories":
		reply(`[{"repository":"acme/web","settings":{"automatic":true,"decisions":false},"clones":[{"id":"c1","path":%q,"common_dir":%q,"repository":"acme/web","settings":{"automatic":null},"available":true},{"id":"c2","path":"%s/gone","common_dir":"%s/gone/.git","repository":"acme/web","settings":{"automatic":null},"available":false}],"earlier_names":["acme/old"]},
{"repository":"other/api","settings":{"automatic":null},"clones":[]}]`, sentinelHome+"/web", sentinelHome+"/web/.git", sentinelHome, sentinelHome)
	case p == "/api/clones/found":
		reply(`{"scanned_at":"2026-11-12T09:00:00Z","running":false,"repositories":{"acme/web":[{"path":%q,"common_dir":%q,"last_activity":"2026-11-10T00:00:00Z","source":"worktree"},{"path":%q,"common_dir":%q,"last_activity":"2026-11-11T00:00:00Z","source":"scan"}]},"attached":[{"repository":"acme/web","path":%q,"at":"2026-11-09T00:00:00Z"}]}`,
			sentinelHome+"/a", sentinelHome+"/a/.git", sentinelHome+"/b", sentinelHome+"/b/.git", sentinelHome+"/a")
	case p == "/api/github/repositories":
		reply(`{"fetched_at":"2026-11-12T09:00:00Z","total_count":2,"repositories":[{"repository":"acme/web","private":true,"top":true,"fork":false,"pushed_at":"2026-11-10T00:00:00Z","description":"The web app","picked":true,"clone":"c1"},{"repository":"acme/lib","private":false,"fork":true,"picked":false,"clone":""}],"organization_access":{"organizations":["%s"]},"search_error":%q,"query":%q}`, sentinelEnv, sentinelToken, q.Get("query"))
	case p == "/api/repositories/acme/web/clone-target":
		reply(`{"dir":%q}`, sentinelHome+"/src/web")
	case p == "/api/repositories/acme/outside/clone-target":
		reply(`{"dir":"/srv/elsewhere/outside"}`)
	case p == "/api/diagnostics":
		reply(`{"local":true,"generation_runner":true,"discovery":true,"index":true,"home":%q,"report":{"status":"warning","checks":[
{"id":"claude","label":"Claude Code","status":"ok","detail":"DETAIL-%s","path":%q,"version":"2.1.0","source":"PATH","fix":"FIX-%s"},
{"id":"git","label":"git at %s/bin","status":"warning","detail":"DETAIL-%s","path":%q,"version":"git %s/v","fix":"FIX"}]},
"report_issue_url":"https://github.com/new?body=%s","report_issue":{"version":"v","os":"linux","diagnostics":"%s"}}`,
			sentinelHome, sentinelEnv, sentinelHome+"/bin/claude", sentinelEnv, sentinelHome, sentinelToken, sentinelHome+"/bin/git", sentinelHome, sentinelEnv, sentinelToken)
	default:
		http.NotFound(w, r)
	}
}

func sentinelServer(t *testing.T, planUsage func(context.Context) ([]json.RawMessage, error)) (*Server, *sentinelDaemon, string) {
	t.Helper()
	d := &sentinelDaemon{}
	s := New(Options{API: d, Version: "test", PlanUsage: planUsage})
	token, _ := chat(t, s)
	return s, d, token
}

func noSentinel(t *testing.T, what, text string, extra ...string) {
	t.Helper()
	for _, leak := range append([]string{sentinelHome, sentinelToken, sentinelEnv, "/home/", "common_dir", "config_digest", "DETAIL-", "FIX-", "report_issue", "organization_access", "search_error"}, extra...) {
		if strings.Contains(text, leak) {
			t.Errorf("%s carries %q: %s", what, leak, text)
		}
	}
}

func TestRepositoryToolsCarryNoPathTokenOrEnvironment(t *testing.T) {
	s, _, token := sentinelServer(t, nil)
	text, failed := data(t, s, token, "repositories", nil)
	if failed {
		t.Fatalf("repositories: %s", text)
	}
	noSentinel(t, "repositories", text, `"path"`)
	for _, want := range []string{`"repository":"acme/web"`, `"automatic":true`, `"decisions":false`, `"clone_count":2`, `"available_clones":1`, `{"id":"c1","available":true}`, `"earlier_names":["acme/old"]`, `"repository":"other/api"`} {
		if !strings.Contains(text, want) {
			t.Errorf("repositories lacks %s: %s", want, text)
		}
	}
	if text, _ = data(t, s, token, "repositories", map[string]any{"repository": "OTHER/api"}); strings.Contains(text, "acme/web") || !strings.Contains(text, "other/api") {
		t.Errorf("one repository: %s", text)
	}
	if text, failed = data(t, s, token, "repositories", map[string]any{"repository": "no/such"}); !failed {
		t.Errorf("an unknown repository: %s", text)
	}
	// The repository page's repository is the default.
	pageToken, id, revoke := mint(t, s, Consumer{Kind: Chat})
	defer revoke()
	if err := s.Registry.SetPage(id, PageContext{Page: PageRepositories, Repository: "other/api"}); err != nil {
		t.Fatal(err)
	}
	if text, _ = data(t, s, pageToken, "repositories", nil); strings.Contains(text, "acme/web") || !strings.Contains(text, "other/api") {
		t.Errorf("the repository page's repository: %s", text)
	}

	text, failed = data(t, s, token, "clones_found", nil)
	if failed {
		t.Fatalf("clones_found: %s", text)
	}
	noSentinel(t, "clones_found", text, `"path"`)
	for _, want := range []string{`"scanned_at":"2026-11-12T09:00:00Z"`, `"repository":"acme/web","clones":2`, `"sources":["worktree","scan"]`, `"last_activity":"2026-11-11T00:00:00Z"`, `"attached":[{"repository":"acme/web","at":"2026-11-09T00:00:00Z"}]`} {
		if !strings.Contains(text, want) {
			t.Errorf("clones_found lacks %s: %s", want, text)
		}
	}

	text, failed = data(t, s, token, "github_repositories", map[string]any{"query": "acme"})
	if failed {
		t.Fatalf("github_repositories: %s", text)
	}
	noSentinel(t, "github_repositories", text, `"clone"`, "fetched_at")
	for _, want := range []string{`"repository":"acme/web","private":true`, `"description":"The web app"`, `"picked":true,"cloned":true`, `"repository":"acme/lib","private":false,"fork":true,"picked":false,"cloned":false`, `"total":2`} {
		if !strings.Contains(text, want) {
			t.Errorf("github_repositories lacks %s: %s", want, text)
		}
	}
	if text, _ = data(t, s, token, "github_repositories", map[string]any{"limit": 1}); strings.Contains(text, "acme/lib") || !strings.Contains(text, `"more":true`) {
		t.Errorf("a limit: %s", text)
	}

	// The folder is written from home: ~/src/web, never the home itself.
	was := homeDirectory
	homeDirectory = func() (string, error) { return sentinelHome, nil }
	t.Cleanup(func() { homeDirectory = was })
	text, failed = data(t, s, token, "clone_target", map[string]any{"repository": "acme/web"})
	if failed || !strings.Contains(text, `"folder":"~/src/web"`) {
		t.Errorf("clone_target: %v %s", failed, text)
	}
	noSentinel(t, "clone_target", text)
	if text, _ = data(t, s, token, "clone_target", map[string]any{"repository": "acme/outside"}); !strings.Contains(text, `.../outside`) || strings.Contains(text, "elsewhere") {
		t.Errorf("a folder outside home: %s", text)
	}
	if text, failed = data(t, s, token, "clone_target", map[string]any{"repository": "../x"}); !failed {
		t.Errorf("a bad repository: %s", text)
	}
}

func TestDiagnosticsSummaryProjectsEveryCheck(t *testing.T) {
	s, d, token := sentinelServer(t, nil)
	text, failed := data(t, s, token, "diagnostics_summary", nil)
	if failed {
		t.Fatalf("diagnostics_summary: %s", text)
	}
	noSentinel(t, "diagnostics_summary", text, `"home"`, `"path"`, `"detail"`, `"fix"`, "github.com/new", "report_issue")
	for _, want := range []string{`"status":"warning"`, `{"id":"claude","label":"Claude Code","status":"ok","version":"2.1.0","source":"PATH"}`, `"label":"git at ~/bin"`, `"version":"git ~/v"`} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics_summary lacks %s: %s", want, text)
		}
	}
	if got := d.requests(); len(got) != 1 || got[0] != "GET /api/diagnostics" {
		t.Errorf("requests %v", got)
	}
	// An action has neither the tool nor the route.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7})
	defer revoke()
	for _, tool := range []string{"diagnostics_summary", "github_repositories", "plan_usage", "analytics", "job_list", "repositories", "clones_found", "clone_target"} {
		r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`)
		if r.Error == nil || !strings.Contains(r.Error.Message, "unknown tool") {
			t.Errorf("an action called %s: %+v", tool, r)
		}
	}
}

func TestAnalyticsToolProjectsTheReport(t *testing.T) {
	s, d, token := sentinelServer(t, nil)
	text, failed := data(t, s, token, "analytics", map[string]any{"days": 90, "repository": "ACME/web", "agent": "codex"})
	if failed {
		t.Fatalf("analytics: %s", text)
	}
	if got := d.requests(); len(got) != 1 || got[0] != "GET /api/analytics?agent=codex&days=90&repo=acme%2Fweb" {
		t.Errorf("requests %v", got)
	}
	for _, want := range []string{`"summary":{"sessions":2`, `"by_repository":[{"key":"acme/web"`, `"baum":{"available":true,"reviews":3}`, `"series":[{"limit_hits":0,"peak":10,"provider":"claude","window":"5-hour"}]`, `"opened_merged":1`} {
		if !strings.Contains(text, want) {
			t.Errorf("analytics lacks %s: %s", want, text)
		}
	}
	for _, gone := range []string{"points", "merge_seconds", "100.5"} {
		if strings.Contains(text, gone) {
			t.Errorf("analytics keeps %s: %s", gone, text)
		}
	}
	// The page's own filters are the defaults.
	_, id := chat(t, s)
	_ = id
	if text, failed = data(t, s, token, "analytics", map[string]any{"days": 14}); !failed {
		t.Errorf("days 14: %s", text)
	}
	if text, failed = data(t, s, token, "analytics", map[string]any{"repository": "nowhere"}); !failed {
		t.Errorf("a bad repository: %s", text)
	}
}

func TestAnalyticsToolDefaultsToThePageFilters(t *testing.T) {
	s, d, _ := sentinelServer(t, nil)
	token, id, revoke := mint(t, s, Consumer{Kind: Chat})
	defer revoke()
	if err := s.Registry.SetPage(id, PageContext{Page: PageAnalytics, Filters: map[string]string{"days": "7", "repo": "acme/web", "agent": "claude_code"}}); err != nil {
		t.Fatal(err)
	}
	if _, failed := data(t, s, token, "analytics", nil); failed {
		t.Fatal("analytics failed")
	}
	if got := d.requests(); len(got) != 1 || got[0] != "GET /api/analytics?agent=claude_code&days=7&repo=acme%2Fweb" {
		t.Errorf("requests %v", got)
	}
}

func TestJobTools(t *testing.T) {
	s, d, token := sentinelServer(t, nil)
	text, failed := data(t, s, token, "job_list", nil)
	if failed {
		t.Fatalf("job_list: %s", text)
	}
	noSentinel(t, "job_list", text, "accounting", "head_sha", "price_version")
	for _, want := range []string{`"id":"j-new","type":"review","state":"running"`, `"id":"j-fail"`, `"id":"j-other"`, `"id":"j-last"`, `"model":"opus"`, `"cost_usd":0.25`} {
		if !strings.Contains(text, want) {
			t.Errorf("job_list lacks %s: %s", want, text)
		}
	}
	// A narrowed list reads on until it has enough, and says where to resume.
	d.mu.Lock()
	d.seen = nil
	d.mu.Unlock()
	text, _ = data(t, s, token, "job_list", map[string]any{"state": "failed", "repository": "ACME/web"})
	if strings.Contains(text, "j-new") || strings.Contains(text, "j-other") || !strings.Contains(text, `"id":"j-fail"`) || !strings.Contains(text, `"id":"j-last"`) || strings.Contains(text, `"next"`) {
		t.Errorf("a narrowed list: %s", text)
	}
	if got := strings.Join(d.requests(), " "); !strings.Contains(got, "before=j-old") {
		t.Errorf("the older page was not read: %s", got)
	}
	text, _ = data(t, s, token, "job_list", map[string]any{"limit": 1})
	if !strings.Contains(text, `"id":"j-new"`) || strings.Contains(text, `"id":"j-fail"`) || !strings.Contains(text, `"next":"j-new"`) {
		t.Errorf("limit 1: %s", text)
	}
	text, _ = data(t, s, token, "job_list", map[string]any{"before": "j-new", "limit": 5})
	if !strings.Contains(text, `"id":"j-fail"`) || strings.Contains(text, `"id":"j-new"`) {
		t.Errorf("before: %s", text)
	}
	if text, failed = data(t, s, token, "job_list", map[string]any{"before": "../x"}); !failed {
		t.Errorf("a bad cursor: %s", text)
	}

	text, failed = data(t, s, token, "job", map[string]any{"id": "j-fail"})
	if failed {
		t.Fatalf("job: %s", text)
	}
	noSentinel(t, "job", text, "accounting", "head_sha", "fix", "price_version")
	for _, want := range []string{`"type":"review"`, `"reason":"agent_failed"`, `"failed_sections":["sec_1"]`, `"context_failed":true`, `"backend":"claude"`} {
		if !strings.Contains(text, want) {
			t.Errorf("job lacks %s: %s", want, text)
		}
	}
	// The failure: the reason and the words, which the envelope masks the
	// token in; no accounting and no fix inputs.
	text, failed = data(t, s, token, "job_failure", map[string]any{"id": "j-fail"})
	if failed {
		t.Fatalf("job_failure: %s", text)
	}
	if strings.Contains(text, sentinelToken) {
		t.Errorf("the failure carries a token: %s", text)
	}
	noSentinel(t, "job_failure", text, "accounting", "cost", "calls", "tokens", "fix", "created_at")
	for _, want := range []string{`"reason":"agent_failed"`, `"detail":"The agent stopped:`, `"failed_sections":["sec_1"]`, `"backend":"claude"`, `"model":"opus"`} {
		if !strings.Contains(text, want) {
			t.Errorf("job_failure lacks %s: %s", want, text)
		}
	}
	if text, failed = data(t, s, token, "job_failure", map[string]any{"id": "j-new"}); !failed || !strings.Contains(text, "has not failed") {
		t.Errorf("a running job's failure: %v %s", failed, text)
	}
	if text, failed = data(t, s, token, "job", map[string]any{"id": "missing"}); !failed || !strings.Contains(text, "not loaded") {
		t.Errorf("a missing job: %v %s", failed, text)
	}
	// An action's job must be its repository's: the reader checks it.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7})
	defer revoke()
	for _, tool := range []string{"job", "job_failure"} {
		if text, failed = data(t, s, action, tool, map[string]any{"id": "j-other"}); !failed || !strings.Contains(text, "own pull request's repository") || strings.Contains(text, "timeout") {
			t.Errorf("an action's %s of another repository's job: %v %s", tool, failed, text)
		}
		if text, failed = data(t, s, action, tool, map[string]any{"id": "j-fail"}); failed {
			t.Errorf("an action's %s of its own job: %s", tool, text)
		}
	}
	// The open job page's job is the default.
	chatToken, id, revokeChat := mint(t, s, Consumer{Kind: Chat})
	defer revokeChat()
	if err := s.Registry.SetPage(id, PageContext{Page: PageJob, Job: "j-fail"}); err != nil {
		t.Fatal(err)
	}
	if text, failed = data(t, s, chatToken, "job", nil); failed || !strings.Contains(text, `"id":"j-fail"`) {
		t.Errorf("the page's job: %v %s", failed, text)
	}
	if text, failed = data(t, s, token, "job", nil); !failed {
		t.Errorf("no job page: %s", text)
	}
}

// plan_usage reads the daemon's cache through the accessor, never a route,
// and never makes the daemon read a provider.
func TestPlanUsageReadsTheAccessorOnly(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	plans := []json.RawMessage{json.RawMessage(`{"provider":"claude","available":true,"plan":"Max 5x","windows":[{"name":"5-hour","used_percent":38,"resets_at":"2026-11-12T15:00:00Z"}],"fetched_at":"2026-11-12T12:00:00Z","token":"` + sentinelToken + `"}`),
		json.RawMessage(`{"provider":"codex","available":false,"reason":"No Codex sessions found yet.","windows":null,"fetched_at":"2026-11-12T12:00:00Z"}`)}
	s, d, token := sentinelServer(t, func(context.Context) ([]json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return plans, nil
	})
	text, failed := data(t, s, token, "plan_usage", nil)
	if failed {
		t.Fatalf("plan_usage: %s", text)
	}
	noSentinel(t, "plan_usage", text, "token")
	for _, want := range []string{`"provider":"claude","available":true,"plan":"Max 5x"`, `"name":"5-hour","used_percent":38`, `"provider":"codex","available":false,"reason":"No Codex sessions found yet.","windows":[]`} {
		if !strings.Contains(text, want) {
			t.Errorf("plan_usage lacks %s: %s", want, text)
		}
	}
	if calls != 1 {
		t.Errorf("the accessor was called %d times", calls)
	}
	// It reads no route: the daemon, which would fetch, was never asked.
	if got := d.requests(); len(got) != 0 {
		t.Errorf("plan_usage read %v", got)
	}
	// No accessor, an empty cache and a failing one are said, not fetched.
	none, _, tokenNone := sentinelServer(t, nil)
	if text, failed = data(t, none, tokenNone, "plan_usage", nil); !failed || !strings.Contains(text, "unavailable") {
		t.Errorf("no accessor: %v %s", failed, text)
	}
	empty, _, tokenEmpty := sentinelServer(t, func(context.Context) ([]json.RawMessage, error) { return nil, nil })
	if text, failed = data(t, empty, tokenEmpty, "plan_usage", nil); failed || !strings.Contains(text, `"plans":[]`) || !strings.Contains(text, "No plan has been read") {
		t.Errorf("an empty cache: %v %s", failed, text)
	}
	broken, _, tokenBroken := sentinelServer(t, func(context.Context) ([]json.RawMessage, error) { return nil, errors.New("boom") })
	if text, failed = data(t, broken, tokenBroken, "plan_usage", nil); !failed || strings.Contains(text, "boom") {
		t.Errorf("a failing accessor: %v %s", failed, text)
	}
	// The route itself stays unreadable to every tool.
	for _, tool := range toolsByName(s) {
		for _, route := range tool.Reads {
			if strings.HasPrefix(route, "/api/plan-usage") {
				t.Errorf("%s reads %s", tool.Name, route)
			}
		}
	}
}
