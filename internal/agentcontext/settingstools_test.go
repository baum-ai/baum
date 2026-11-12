package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// settingsDaemon answers the routes of the Settings and repository page tools
// with a planted value in every field that is secret or sensitive: the agent
// environment's values, a pending GitHub sign-in's code and address, a token,
// error text quoting a token and the home folder, clone folders, a check's
// folder, detail and fix, a configuration file outside home and a GitHub
// Enterprise host. Each planted value holds SENTINEL (or is sentinelHome or
// sentinelToken), so a leak is found by looking for those.
type settingsDaemon struct {
	mu   sync.Mutex
	seen []string
}

const (
	sentinelUserCode = "SENTINEL-UCODE"
	sentinelURI      = "https://github.com/login/device/SENTINELURI"
	sentinelConfig   = "/etc/SENTINEL-config/config.toml"
	sentinelHost     = "ghe.corp-SENTINEL.example"
)

func (d *settingsDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

func (d *settingsDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	entry := func(id, custom, source, inherited string) string {
		e := map[string]any{"id": id, "title": "Title of " + id, "group": "fixes", "used_by": "Used when " + id, "default_text": "DEFAULT TEXT OF " + id, "default_version": "fix-public-v1",
			"custom_text": nil, "based_on": nil, "outdated": false, "invalid": false, "invalid_reason": nil,
			"safety_rules":         []map[string]string{{"id": id + ".untrusted", "label": "Untrusted data", "text": "RULE TEXT OF " + id}},
			"missing_safety_rules": []string{}}
		if custom != "" {
			e["custom_text"], e["based_on"], e["outdated"], e["invalid"], e["invalid_reason"] = custom, "fix-public-v0", true, true, "it leaves a fence open"
			e["missing_safety_rules"] = []string{id + ".untrusted"}
		}
		if source != "" {
			e["source"], e["inherited_text"] = source, inherited
		}
		b, _ := json.Marshal(e)
		return string(b)
	}
	switch r.URL.Path {
	case "/api/settings":
		reply(`{"backend":"claude","model":"opus","budgets":{"ReviewTimeout":"SENTINEL_BUDGET"},"caps":{"SENTINEL_CAP":1},
"agent_env":{"ZED_TOKEN":%q,"API_KEY":%q,"OTHER":"%s/SENTINEL_ENV_PATH","bad name SENTINEL_NAME":"x"},"agent_env_source":"app",
"fix_backend":"codex","fix_model":"gpt-5","effort":"high","fix_effort":"low","job_concurrency":3,"review_time_limit_minutes":20,"run_time_limit_minutes":45}`, sentinelEnv, sentinelToken, sentinelHome)
	case "/api/settings/appearance":
		reply(`{"theme":"dark","key_hints":false,"page_modifier":"alt","extra":"SENTINEL_APPEARANCE"}`)
	case "/api/settings/decisions":
		reply(`{"decisions":{"enabled":true,"backend":"","model":"","effort":"","effective_backend":"claude","backend_undetermined":false,"effective_model":"haiku","model_ignored":false,"extra":"SENTINEL_DECISIONS"}}`)
	case "/api/automatic":
		reply(`{"enabled":true,"pull_requests":"authored","repositories":[{"repository":"acme/web","enabled":true,"notice":"unavailable","pending":2},{"repository":"other/api","enabled":false,"notice":"OTHER_NOTICE","pending":5}]}`)
	case "/api/agent-requests":
		reply(`{"requests":[{"id":"r1","question":"SENTINEL_REQUEST"}],"messages":[{"text":"SENTINEL_MESSAGE"}],"settings":{"codex_approvals":true}}`)
	case "/api/github-auth":
		reply(`{"state":"pending","host":%q,"login":"octo","source":"environment variable SENTINEL_TOKEN_VAR","override":true,"saved":true,"workflow":true,
"pending":{"id":"SENTINEL_DEVICE_ID","user_code":%q,"verification_uri":%q,"expires_at":"2026-11-12T10:00:00Z","interval":5,"workflow":true},
"error":"auth_request_failed","message":"GitHub said token=%s for %s/work","organization_access":{"hidden":["acme-corp"],"approval_url":"https://github.com/SENTINEL_APPROVAL"}}`,
			sentinelHost, sentinelUserCode, sentinelURI, sentinelToken, sentinelHome)
	case "/api/diagnostics":
		reply(`{"local":true,"home":%q,"report":{"status":"problem","checks":[
{"id":"claude","label":"claude","status":"ok","detail":"DETAIL SENTINEL %s","path":"%s/bin/claude","version":"2.1.0","source":"PATH","fix":"FIX SENTINEL"},
{"id":"codex","label":"codex","status":"problem","detail":"DETAIL SENTINEL","path":"/opt/SENTINEL/codex","version":"0.9 %s/bin","source":"backend_paths.codex in %s","fix":"FIX SENTINEL %s"},
{"id":"backend-auto","label":"backend: auto","status":"ok","detail":"auto picks claude SENTINEL","version":"2.1.0"},
{"id":"backend-configured","label":"backend: claude","status":"ok","detail":"usable SENTINEL","version":"2.1.0"},
{"id":"anthropic-api-key","label":"ANTHROPIC_API_KEY (anthropic backend)","status":"info","detail":"set SENTINEL"},
{"id":"config","label":"config file","status":"info","path":%q},
{"id":"git","label":"git","status":"warning","detail":"DETAIL SENTINEL","path":"%s/bin/git","version":"2.50"},
{"id":"github-token-%s","label":"GitHub token: %s","status":"ok","source":"environment variable SENTINEL_TOKEN_VAR"}]},
"report_issue_url":"https://github.com/new?body=SENTINEL","report_issue":{"diagnostics":"SENTINEL"}}`,
			sentinelHome, sentinelToken, sentinelHome, sentinelHome, sentinelConfig, sentinelToken, sentinelConfig, sentinelHome, sentinelHost, sentinelHost)
	case "/api/settings/prompts":
		reply(`{"prompts":[%s,%s]}`, entry("fix", "GLOBAL CUSTOM TEXT", "", ""), entry("review-group", "", "", ""))
	case "/api/repositories/acme/web/prompts":
		reply(`{"prompts":[%s,%s]}`, entry("fix", "REPOSITORY CUSTOM TEXT", "repository", "INHERITED TEXT"), entry("review-group", "", "default", "DEFAULT TEXT OF review-group"))
	case "/api/repositories/other/api/prompts":
		reply(`{"prompts":[%s]}`, entry("fix", "FOREIGN_PROMPT", "repository", "FOREIGN_INHERITED"))
	case "/api/repositories":
		reply(`[{"repository":"acme/web","settings":{"automatic":true,"decisions":false},"clones":[
{"id":"c1","path":"%s/web","common_dir":"%s/web/.git","repository":"acme/web","settings":{"automatic":null},"available":true},
{"id":"c2","path":"/srv/SENTINEL/web","common_dir":"/srv/SENTINEL/web/.git","repository":"acme/web","settings":{"automatic":null},"available":false}],"earlier_names":["acme/old"]},
{"repository":"other/api","settings":{"automatic":false,"decisions":null},"clones":[{"id":"c9","path":"%s/FOREIGN_CLONE","repository":"other/api","available":true}],"earlier_names":["other/FOREIGN_NAME"]}]`,
			sentinelHome, sentinelHome, sentinelHome)
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(`{"message":"not found SENTINEL_404"}`)
	}
}

// leaks are what no Settings or repository page tool may return, whatever the
// consumer: the planted values and the fields that carry them.
var settingsLeaks = []string{"SENTINEL", "sentinel", sentinelHome, sentinelToken, sentinelEnv, "/home/", "common_dir", `"path":`, `"detail":`, `"fix":`,
	"user_code", "verification_uri", "approval_url", "budgets", "caps", "report_issue", "DEVICE"}

func settingsServer(t *testing.T) (*Server, *settingsDaemon) {
	t.Helper()
	d := &settingsDaemon{}
	s := New(Options{API: d, Version: "test",
		PlanChoices: func(context.Context) ([]json.RawMessage, error) {
			return []json.RawMessage{
				json.RawMessage(`{"provider":"claude","name":"Claude Code","shown":true,"default":true,"explicit":false,"secret":"SENTINEL_PLAN"}`),
				json.RawMessage(`{"provider":"codex","name":"Codex","shown":false,"default":false,"explicit":true}`),
			}, nil
		},
		CodexHooks: func() (string, time.Time, bool) {
			return "untrusted", time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC), true
		}})
	was := homeDirectory
	homeDirectory = func() (string, error) { return sentinelHome, nil }
	t.Cleanup(func() { homeDirectory = was })
	return s, d
}

// runDirect runs a tool as consumer, on page, without the envelope, through the
// reader the server would give it: the raw text, before any masking.
func runDirect(t *testing.T, s *Server, consumer Consumer, page *PageContext, name string, args any) (string, error) {
	t.Helper()
	tool, ok := toolsByName(s)[name]
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	if args == nil {
		b = []byte(`{}`)
	}
	bound := ""
	if consumer.Kind == Action {
		bound = consumer.Repository
	}
	c := CallContext{Consumer: consumer, Reader: s.reader.forTool(tool, bound, consumer.PullRequest)}
	if page != nil {
		c.Page, c.HasPage = *page, true
	}
	return tool.Call(context.Background(), c, b)
}

var settingsToolNames = []string{"settings_general", "settings_reviews", "settings_fixes", "settings_connections", "settings_computer", "prompts", "prompt", "diagnostics_summary"}
var repositoryToolNames = []string{"repository_reviews", "repository_prompts", "repository_registration"}

// Every Settings and repository page tool, called as a chat with and without
// the envelope, returns what its section shows and none of the planted values.
func TestSettingsToolsCarryNoSecret(t *testing.T) {
	s, d := settingsServer(t)
	token, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageSettings, Section: "computer"})
	calls := []struct {
		tool string
		args map[string]any
		want []string
		page *PageContext
	}{
		{"settings_general", nil, []string{`"theme":"dark"`, `"key_hints":false`, `"page_modifier":"alt"`, `{"provider":"claude","name":"Claude Code","shown":true,"default":true,"explicit":false}`, `{"provider":"codex","name":"Codex","shown":false,"default":false,"explicit":true}`}, nil},
		{"settings_reviews", nil, []string{`"automatic":{"enabled":true,"pull_requests":"authored"}`, `"backend":"claude","model":"opus","effort":"high"`, `"decision_capture":{"enabled":true,"backend":"","model":"","effort":"","effective_backend":"claude","backend_undetermined":false,"effective_model":"haiku","model_ignored":false}`}, nil},
		{"settings_fixes", nil, []string{`{"backend":"codex","model":"gpt-5","effort":"low","same_as_reviews":false}`}, nil},
		{"settings_connections", nil, []string{`"state":"pending"`, `"host":"other host"`, `"login":"octo"`, `"source":"environment"`, `"override":true`, `"saved":true`, `"workflow":true`, `"error":"auth_request_failed"`, `"organizations_not_reachable":["acme-corp"]`, `"codex_hooks":{"state":"untrusted","checked_at":"2026-11-12T09:30:00Z"}`}, nil},
		{"settings_computer", nil, []string{`"job_concurrency":3`, `"review_time_limit_minutes":20`, `"run_time_limit_minutes":45`, `"agent_env_names":["API_KEY","OTHER","ZED_TOKEN"]`, `"agent_env_source":"app"`, `"codex_approvals":true`,
			`{"id":"claude","label":"claude","status":"ok","version":"2.1.0"}`, `{"id":"codex","label":"codex","status":"problem","version":"0.9 ~/bin"}`, `{"id":"backend-auto","label":"backend: auto","status":"ok","version":"2.1.0"}`, `{"id":"anthropic-api-key","label":"ANTHROPIC_API_KEY (anthropic backend)","status":"info"}`, `"diagnostics_status":"problem"`, `"diagnostics_problems":1`}, nil},
		{"prompts", nil, []string{`"id":"fix"`, `"custom":true`, `"based_on":"fix-public-v0"`, `"outdated":true`, `"invalid":true`, `"invalid_reason":"it leaves a fence open"`, `"safety_rules":[{"id":"fix.untrusted","label":"Untrusted data"}]`, `"missing_safety_rules":["fix.untrusted"]`, `"id":"review-group"`, `"custom":false`}, nil},
		{"prompts", map[string]any{"repository": "acme/web"}, []string{`"source":"repository"`, `"source":"default"`}, nil},
		{"prompt", map[string]any{"id": "fix"}, []string{`"default_text":"DEFAULT TEXT OF fix"`, `"custom_text":"GLOBAL CUSTOM TEXT"`, `"default_version":"fix-public-v1"`, `"safety_rules":[{"id":"fix.untrusted","label":"Untrusted data","text":"RULE TEXT OF fix"}]`}, nil},
		{"prompt", map[string]any{"id": "fix", "repository": "acme/web"}, []string{`"custom_text":"REPOSITORY CUSTOM TEXT"`, `"source":"repository"`, `"inherited_text":"INHERITED TEXT"`}, nil},
		{"diagnostics_summary", nil, []string{`"status":"problem"`, `"id":"github-token-other-host"`}, nil},
		{"repository_reviews", map[string]any{"repository": "acme/web"}, []string{`{"repository":"acme/web","automatic":true,"decisions":false,"automatic_status":{"enabled":true,"notice":"unavailable","pending":2}}`}, nil},
		{"repository_prompts", map[string]any{"repository": "acme/web"}, []string{`"source":"repository"`, `"custom":true`}, nil},
		{"repository_prompts", map[string]any{"repository": "acme/web", "id": "fix"}, []string{`"custom_text":"REPOSITORY CUSTOM TEXT"`, `"inherited_text":"INHERITED TEXT"`}, nil},
		{"repository_registration", map[string]any{"repository": "acme/web"}, []string{`{"repository":"acme/web","registered":true,"clone_count":2,"available_clones":1,"clones":[{"id":"c1","available":true},{"id":"c2","available":false}],"earlier_names":["acme/old"]}`}, nil},
		{"repository_registration", nil, []string{`"repository":"acme/web","registered":true`}, &PageContext{Page: PageRepositories, Repository: "acme/web", Section: "registration"}},
	}
	for _, tt := range calls {
		what := fmt.Sprintf("%s %v", tt.tool, tt.args)
		raw, err := runDirect(t, s, Consumer{Kind: Chat}, tt.page, tt.tool, tt.args)
		if err != nil {
			t.Errorf("%s: %v", what, err)
			continue
		}
		absent(t, what+" (raw)", raw, settingsLeaks...)
		for _, want := range tt.want {
			if !strings.Contains(raw, want) {
				t.Errorf("%s lacks %s: %s", what, want, raw)
			}
		}
		if tt.page != nil {
			continue
		}
		text, failed := data(t, s, token, tt.tool, tt.args)
		if failed {
			t.Errorf("%s: %s", what, text)
		}
		absent(t, what, text, settingsLeaks...)
	}
	// The settings were read through the routes the tools declare and never
	// through the plan usage, Codex hooks or sign-in routes that would fetch,
	// start Codex or act.
	for _, r := range d.requests() {
		for _, never := range []string{"plan-usage", "agent-hooks", "/api/github-auth/", "refresh"} {
			if strings.Contains(r, never) {
				t.Errorf("a tool read %s", r)
			}
		}
	}
}

// A tool whose accessor or route fails says what is unavailable and quotes
// nothing of the failure.
func TestSettingsToolsReportWhatIsUnavailable(t *testing.T) {
	s := New(Options{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/settings":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `{"message":"SENTINEL %s"}`, sentinelToken)
		case "/api/settings/appearance":
			_, _ = w.Write([]byte(`{"theme":"system","key_hints":true,"page_modifier":""}`))
		default:
			_, _ = w.Write([]byte(`{"available":false,"code":"SENTINEL_UNAVAILABLE"}`))
		}
	}), PlanChoices: func(context.Context) ([]json.RawMessage, error) {
		return nil, errors.New("SENTINEL " + sentinelToken)
	}})
	for _, tool := range []string{"settings_reviews", "settings_fixes", "settings_computer"} {
		text, err := runDirect(t, s, Consumer{Kind: Chat}, nil, tool, nil)
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), sentinelToken) {
			t.Errorf("%s: %v %s", tool, err, text)
		}
	}
	text, err := runDirect(t, s, Consumer{Kind: Chat}, nil, "settings_general", nil)
	if err != nil || !strings.Contains(text, `"plan_usage":[]`) || !strings.Contains(text, "unavailable") {
		t.Errorf("settings_general: %v %s", err, text)
	}
	absent(t, "settings_general", text, settingsLeaks...)
	text, err = runDirect(t, s, Consumer{Kind: Chat}, nil, "settings_connections", nil)
	if err != nil || !strings.Contains(text, `"github":null`) || !strings.Contains(text, `"codex_hooks":null`) || !strings.Contains(text, `"unavailable":["github","codex_hooks"]`) {
		t.Errorf("settings_connections: %v %s", err, text)
	}
	absent(t, "settings_connections", text, settingsLeaks...)
	// A Codex hook state never read is said, not looked for.
	s.codexHooks = func() (string, time.Time, bool) { return "", time.Time{}, false }
	if text, _ = runDirect(t, s, Consumer{Kind: Chat}, nil, "settings_connections", nil); !strings.Contains(text, "has not checked the Codex hooks") {
		t.Errorf("an unchecked Codex hook state: %s", text)
	}
}

// The Settings tools are global: an action neither lists nor calls them, and
// its reader is refused their projected routes. The repository page's tools
// are repository-scoped: an action lists them and reads only its own
// repository, through bound routes.
func TestSettingsAndRepositoryToolsForActions(t *testing.T) {
	s, d := settingsServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7})
	defer revoke()
	names := listed(t, s, action)
	for _, tool := range settingsToolNames {
		if names[tool] {
			t.Errorf("an action lists %s", tool)
		}
		r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`)
		if r.Error == nil || !strings.Contains(r.Error.Message, "unknown tool") {
			t.Errorf("an action called %s: %+v", tool, r)
		}
		if toolsByName(s)[tool].Scope != ScopeGlobal {
			t.Errorf("%s is not global", tool)
		}
	}
	for _, tool := range repositoryToolNames {
		if !names[tool] {
			t.Errorf("an action does not list %s", tool)
		}
		if toolsByName(s)[tool].Scope != ScopeRepository {
			t.Errorf("%s is not repository-scoped", tool)
		}
	}
	foreign := []string{"other/api", "OTHER_NOTICE", "FOREIGN_PROMPT", "FOREIGN_INHERITED", "FOREIGN_CLONE", "FOREIGN_NAME", "c9"}
	// Its own repository, by default and by name.
	for _, tt := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"repository_reviews", nil, `"automatic_status":{"enabled":true,"notice":"unavailable","pending":2}`},
		{"repository_prompts", map[string]any{"repository": "acme/web"}, `"source":"repository"`},
		{"repository_prompts", map[string]any{"id": "fix"}, `"custom_text":"REPOSITORY CUSTOM TEXT"`},
		{"repository_registration", nil, `"clone_count":2,"available_clones":1`},
	} {
		text, failed := data(t, s, action, tt.tool, tt.args)
		if failed || !strings.Contains(text, tt.want) {
			t.Errorf("an action's %s %v: %v %s", tt.tool, tt.args, failed, text)
		}
		absent(t, "an action's "+tt.tool, text, append(slices.Clone(settingsLeaks), foreign...)...)
		raw, err := runDirect(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7}, nil, tt.tool, tt.args)
		if err != nil {
			t.Errorf("an action's %s (raw): %v", tt.tool, err)
		}
		absent(t, "an action's raw "+tt.tool, raw, append(slices.Clone(settingsLeaks), foreign...)...)
	}
	// Another repository's is refused, and nothing of it is read.
	before := len(d.requests())
	for _, tool := range repositoryToolNames {
		text, failed := data(t, s, action, tool, map[string]any{"repository": "other/api"})
		if !failed || !strings.Contains(text, "only its own") {
			t.Errorf("an action's %s of another repository: %v %s", tool, failed, text)
		}
	}
	if got := d.requests()[before:]; len(got) != 0 {
		t.Errorf("an action's refused calls read %v", got)
	}
	// The bound reader itself, under the tool: the action's routes keep its
	// repository's rows, rebuilt without folders or the global switch, and
	// refuse another repository's prompts and the Settings routes.
	for _, name := range repositoryToolNames {
		reader := s.reader.forTool(toolsByName(s)[name], "acme/web", 7)
		read := func(target string) (string, error) {
			r, err := reader.Get(context.Background(), target)
			return string(r.Body), err
		}
		if body, err := read("/api/repositories"); err != nil || !strings.Contains(body, `"repository":"acme/web"`) {
			t.Errorf("%s: the bound repositories %v %s", name, err, body)
		} else {
			absent(t, name+" bound repositories", body, append([]string{"path", "common_dir", sentinelHome}, foreign...)...)
		}
		if strings.HasPrefix(name, "repository_reviews") {
			if body, err := read("/api/automatic"); err != nil || body != `{"repositories":[{"repository":"acme/web","enabled":true,"notice":"unavailable","pending":2}]}` {
				t.Errorf("the bound automatic overview %v %s", err, body)
			}
		}
		if name == "repository_prompts" {
			if _, err := read("/api/repositories/acme/web/prompts"); err != nil {
				t.Errorf("the bound prompts: %v", err)
			}
			if body, err := read("/api/repositories/other/api/prompts"); !errors.Is(err, errOtherRepository) {
				t.Errorf("another repository's prompts: %v %s", err, body)
			}
			if body, err := read("/api/repositories/acme/web/prompts?repository=other/api"); err == nil {
				t.Errorf("a parameter the route does not list: %s", body)
			}
		}
		for _, target := range []string{"/api/settings", "/api/settings/prompts", "/api/settings/appearance", "/api/github-auth", "/api/diagnostics", "/api/agent-requests", "/api/repositories/acme/web/clone-target"} {
			if body, err := read(target); err == nil {
				t.Errorf("%s: an action read %s: %s", name, target, body)
			}
		}
	}
	// A projected route is never an action's, whatever the tool declares.
	for _, name := range settingsToolNames {
		tool := toolsByName(s)[name]
		reader := s.reader.forTool(tool, "acme/web", 7)
		for _, route := range tool.Projected {
			if _, err := reader.Get(context.Background(), route); err == nil {
				t.Errorf("an action's reader of %s read %s", name, route)
			}
		}
	}
}

func TestPageSectionsAreChecked(t *testing.T) {
	s, _ := settingsServer(t)
	token, id := chat(t, s)
	for _, tt := range []struct {
		page PageContext
		ok   bool
	}{
		{PageContext{Page: PageSettings}, true},
		{PageContext{Page: PageSettings, Section: "general"}, true},
		{PageContext{Page: PageSettings, Section: "computer"}, true},
		{PageContext{Page: PageSettings, Section: "registration"}, false},
		{PageContext{Page: PageSettings, Section: "nope"}, false},
		{PageContext{Page: PageRepositories, Repository: "acme/web", Section: "prompts"}, true},
		{PageContext{Page: PageRepositories, Repository: "acme/web", Section: "registration"}, true},
		{PageContext{Page: PageRepositories, Repository: "acme/web", Section: "general"}, false},
		{PageContext{Page: PageRepositories, Section: "prompts"}, false},
		{PageContext{Page: PageRepositories, Repository: "  ", Section: "prompts"}, false},
		{PageContext{Page: PageInbox, Section: "reviews"}, false},
		{PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 1, Section: "reviews"}, false},
		{PageContext{Page: PageSettings, Section: strings.Repeat("x", maxPageText+1)}, false},
	} {
		err := s.Registry.SetPage(id, tt.page)
		if (err == nil) != tt.ok {
			t.Errorf("%+v: %v", tt.page, err)
		}
		// link agrees with the page context on every page that has an address
		// with a section (link reads a blank repository as none, and so the
		// open page's).
		if (tt.page.Page != PageSettings && tt.page.Page != PageRepositories) || tt.page.Repository == "  " {
			continue
		}
		args := map[string]any{"page": tt.page.Page}
		if tt.page.Repository != "" {
			args["repository"] = tt.page.Repository
		}
		if tt.page.Section != "" {
			args["section"] = tt.page.Section
		}
		if _, failed := data(t, s, token, "link", args); failed == tt.ok {
			t.Errorf("link %v: failed %v, the page context's ok %v", args, failed, tt.ok)
		}
	}
}

func TestCurrentPageNamesTheSectionsTools(t *testing.T) {
	s, _ := settingsServer(t)
	token, id := chat(t, s)
	var page struct {
		Section      string   `json:"section"`
		Tools        []string `json:"tools"`
		SectionTools []string `json:"section_tools"`
	}
	setPage(t, s, id, PageContext{Page: PageSettings, Section: "computer"})
	out(t, s, token, "current_page", nil, &page)
	if page.Section != "computer" || !slices.Equal(page.SectionTools, []string{"settings_computer", "diagnostics_summary"}) {
		t.Errorf("settings: %+v", page)
	}
	for _, tool := range append(slices.Clone(settingsToolNames), commonToolNames...) {
		if !slices.Contains(page.Tools, tool) {
			t.Errorf("settings lacks %s: %v", tool, page.Tools)
		}
	}
	if len(page.Tools) != len(settingsToolNames)+len(commonToolNames) {
		t.Errorf("settings lists %v", page.Tools)
	}
	setPage(t, s, id, PageContext{Page: PageSettings})
	out(t, s, token, "current_page", nil, &page)
	if len(page.SectionTools) != 0 || len(page.Tools) != len(settingsToolNames)+len(commonToolNames) {
		t.Errorf("settings without a section: %+v", page)
	}
	// The Repositories page lists a repository's tools only with a repository.
	setPage(t, s, id, PageContext{Page: PageRepositories})
	out(t, s, token, "current_page", nil, &page)
	for _, tool := range repositoryToolNames {
		if slices.Contains(page.Tools, tool) {
			t.Errorf("the Repositories page lists %s: %v", tool, page.Tools)
		}
	}
	setPage(t, s, id, PageContext{Page: PageRepositories, Repository: "acme/web", Section: "prompts"})
	out(t, s, token, "current_page", nil, &page)
	for _, tool := range append(slices.Clone(repositoryToolNames), "repositories", "clone_target") {
		if !slices.Contains(page.Tools, tool) {
			t.Errorf("a repository's page lacks %s: %v", tool, page.Tools)
		}
	}
	if !slices.Equal(page.SectionTools, []string{"repository_prompts"}) {
		t.Errorf("a repository's prompts section: %+v", page)
	}
}

// Every section of the Settings page and of a repository's page, as the app's
// source lists them, has tools of its own, on the page that shows it, and the
// maps name no section the app does not have.
func TestEverySettingsSectionHasItsTools(t *testing.T) {
	src := appSource(t, "settings.tsx")
	sections := map[string][]string{}
	for _, name := range []string{"settingsSections", "repositorySections"} {
		_, rest, ok := strings.Cut(src, "const "+name+" = [")
		if !ok {
			t.Fatalf("settings.tsx has no %s", name)
		}
		list, _, _ := strings.Cut(rest, "]")
		for _, m := range regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(list, -1) {
			sections[name] = append(sections[name], m[1])
		}
		if len(sections[name]) < 3 {
			t.Fatalf("only %v found in %s: the scan is broken", sections[name], name)
		}
	}
	s := New(Options{API: http.NotFoundHandler()})
	tools := toolsByName(s)
	for name, tt := range map[string]struct {
		page  string
		tools map[string][]string
		scope Scope
	}{
		"settingsSections":   {PageSettings, settingsSectionTools, ScopeGlobal},
		"repositorySections": {PageRepositories, repositorySectionTools, ScopeRepository},
	} {
		spec, _ := s.page(tt.page)
		for _, section := range sections[name] {
			if len(tt.tools[section]) == 0 {
				t.Errorf("the %s section %q has no tool: map it to the tools that read it in settingstools.go", tt.page, section)
			}
			for _, tool := range tt.tools[section] {
				if _, ok := tools[tool]; !ok {
					t.Errorf("the %s section %q maps to %s, which is no tool", tt.page, section, tool)
				}
				if !slices.Contains(spec.Tools, tool) {
					t.Errorf("the %s page does not list %s, its %s section's tool", tt.page, tool, section)
				}
				if tools[tool].Scope != tt.scope {
					t.Errorf("%s has the wrong scope", tool)
				}
			}
		}
		for section := range tt.tools {
			if !slices.Contains(sections[name], section) {
				t.Errorf("the %s page has no section %q", tt.page, section)
			}
		}
	}
}
