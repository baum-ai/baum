package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentplugins"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/uiserver"
)

// settingsFixture is the daemon's LocalAPI with the real registry behind the
// settings, appearance, agent environment, decision capture, plan usage
// choice and prompt routes, the real automatic review overview, the real
// agents' request settings and the real GitHub connection with a sign-in
// waiting, behind an MCP server. Fakes: GitHub's device and token endpoints
// (a transport, which hands out a planted user code), the decision model
// resolver, the Codex hook check, the plan usage provider and the
// diagnostics report (a fixed report with planted folders, details and
// fixes). The verification address cannot be planted: the manager accepts
// only GitHub's own.
type settingsFixture struct {
	*readFixture
	home      string
	clonePath string
	fetches   *atomic.Int32
	hookReads *atomic.Int32
	// resolutions counts the decision model's resolutions.
	resolutions *atomic.Int32
}

const (
	settingsUserCode   = "SENTINEL-UCODE"
	settingsDeviceCode = "SENTINEL_DEVICE_CODE"
	settingsEnvValue   = "SENTINEL_ENV_VALUE"
)

// credentials is an empty credential store: nothing is signed in.
type credentials struct {
	mu sync.Mutex
	c  githubauth.Credential
}

func (s *credentials) Load(context.Context) (githubauth.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c.AccessToken == "" {
		return s.c, githubauth.ErrNotConnected
	}
	return s.c, nil
}

func (s *credentials) Save(_ context.Context, c githubauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c = c
	return nil
}

func (s *credentials) Delete(context.Context) error { return nil }

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newSettingsFixture(t *testing.T) *settingsFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	registry, clone := readRegistry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := appdbtest.TempDir(t)
	if _, err := registry.Pick(ctx, "other/repo", nil); err != nil {
		t.Fatal(err)
	}

	// GitHub: a device sign-in that stays pending.
	manager := githubauth.NewWithOptions(&credentials{}, githubauth.Options{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"error":"authorization_pending"}`
		if r.URL.Path == "/login/device/code" {
			body = fmt.Sprintf(`{"device_code":%q,"user_code":%q,"verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`, settingsDeviceCode, settingsUserCode)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	connection := uiserver.NewGitHubAuthentication(ctx, manager, config.Config{}, nil)

	fetches := new(atomic.Int32)
	plans := &planusage.Service{Sources: []planusage.Source{countingPlanSource{fetches}}}
	hookReads, resolutions := new(atomic.Int32), new(atomic.Int32)
	var hooks uiserver.LastHookTrust
	report := doctor.Report{Status: doctor.StatusProblem, Checks: []doctor.Check{
		{ID: "claude", Label: "claude", Status: doctor.StatusOK, Detail: "DETAIL SENTINEL", Path: home + "/bin/claude", Version: "2.1.0", Source: "PATH", Fix: "FIX SENTINEL"},
		{ID: "codex", Label: "codex", Status: doctor.StatusProblem, Detail: "DETAIL SENTINEL " + home, Path: "/opt/SENTINEL/codex", Version: "0.9", Source: "backend_paths.codex in /etc/SENTINEL/config.toml", Fix: "FIX SENTINEL"},
		{ID: "config", Label: "config file", Status: doctor.StatusInfo, Path: "/etc/SENTINEL/config.toml"},
		{ID: "github-token-ghe.SENTINEL.example", Label: "GitHub token: ghe.SENTINEL.example", Status: doctor.StatusOK, Source: "environment variable SENTINEL_TOKEN"},
	}}
	local := uiserver.LocalAPI{Registry: registry, Config: config.Default(), GitHubAuth: connection.Handler(),
		Automatic:     (&automatic.Scheduler{Registry: registry, Path: filepath.Join(dir, "app.sqlite")}).Handler(),
		AgentRequests: (&agentrequests.Store{Database: filepath.Join(dir, "requests.sqlite")}).Handler(),
		PlanUsage:     plans.Handler(),
		PlanUsageCached: func() []json.RawMessage {
			var out []json.RawMessage
			for _, p := range plans.Cached() {
				raw, _ := json.Marshal(p)
				out = append(out, raw)
			}
			return out
		},
		// Each resolution is counted, and takes a while, as running the
		// harnesses' version checks does.
		DecisionModel: func(context.Context, repos.Preferences) uiserver.DecisionModel {
			resolutions.Add(1)
			time.Sleep(20 * time.Millisecond)
			return uiserver.DecisionModel{Backend: "claude", Model: "haiku"}
		},
		HookTrust: hooks.Wrap(func(context.Context) agentplugins.HookTrust {
			hookReads.Add(1)
			return agentplugins.HookTrust{State: "untrusted"}
		}),
		Diagnostics: func(context.Context) doctor.Report { return report },
		Version:     "test",
	}
	api := local.Handler()
	t.Cleanup(api.Close)
	mcp := agentcontext.New(agentcontext.Options{API: api, Version: "test", PlanChoices: local.CachedPlanChoices, CodexHooks: hooks.Get,
		DecisionSettings: (&uiserver.DecisionViews{API: local}).Get,
		Clone: func(ctx context.Context, id string) (string, error) {
			return uiserver.RegisteredRepository(ctx, registry, id)
		}})
	f := &settingsFixture{readFixture: &readFixture{api: api, mcp: mcp}, home: home, clonePath: clone.Path, fetches: fetches, hookReads: hookReads, resolutions: resolutions}

	// What the person set, through the app's own routes.
	for _, put := range []struct{ method, path, body string }{
		{"PUT", "/api/settings/agent-env", fmt.Sprintf(`{"agent_env":{"ZED_TOKEN":%q,"WORK_DIR":%q}}`, settingsEnvValue, home+"/SENTINEL_ENV_PATH")},
		{"PUT", "/api/settings/appearance", `{"theme":"dark","key_hints":false,"page_modifier":"alt"}`},
		{"PUT", "/api/settings/job-concurrency", `{"job_concurrency":2}`},
		{"PUT", "/api/settings/time-limits", `{"review_time_limit_minutes":25,"run_time_limit_minutes":50}`},
		{"PUT", "/api/automatic", `{"enabled":true,"pull_requests":"authored","fix_backend":"codex","fix_model":"gpt-5"}`},
		{"PUT", "/api/agent-requests/settings", `{"codex_approvals":true}`},
		{"PUT", "/api/settings/prompts/fix", `{"text":"Global custom fix prompt."}`},
		{"PUT", "/api/settings/prompts/fix-checks", `{"text":"Global custom checks prompt."}`},
		{"PUT", "/api/repositories/owner/repo/prompts/fix", `{"text":"Repository custom fix prompt."}`},
		{"PUT", "/api/repositories/other/repo/prompts/fix", `{"text":"FOREIGN repository prompt."}`},
		{"PUT", "/api/repositories/owner/repo/settings", `{"automatic":true,"decisions":false}`},
		{"POST", "/api/github-auth/start", `{}`},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(put.method, "http://local"+put.path, strings.NewReader(put.body))
		r.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", put.method, put.path, w.Code, w.Body.String())
		}
	}
	return f
}

func TestSettingsToolsReadTheRealRoutesAndNoSecret(t *testing.T) {
	f := newSettingsFixture(t)
	chat, action := f.tokens(t)
	leaks := []string{"SENTINEL", "sentinel", f.home, f.clonePath, "login/device", "user_code", "verification_uri", `"path":`, `"detail":`, `"fix":`, "common_dir", "/etc/", "budgets", "caps"}
	noLeak := func(what, text string, extra ...string) {
		t.Helper()
		for _, leak := range append(leaks, extra...) {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %s", what, leak, text)
			}
		}
	}
	// The routes hold every planted value the tools must leave out.
	for path, planted := range map[string][]string{
		"/api/settings":     {settingsEnvValue, f.home + "/SENTINEL_ENV_PATH"},
		"/api/github-auth":  {settingsUserCode, "https://github.com/login/device", `"state":"pending"`},
		"/api/diagnostics":  {"DETAIL SENTINEL", "FIX SENTINEL", "/etc/SENTINEL/config.toml", f.home + "/bin/claude", "ghe.SENTINEL.example"},
		"/api/repositories": {f.clonePath},
	} {
		body := string(f.route(t, path))
		for _, want := range planted {
			if !strings.Contains(body, want) {
				t.Fatalf("%s lacks the planted %q: %s", path, want, body)
			}
		}
	}

	call := func(token, tool string, args map[string]any) string {
		t.Helper()
		text, failed := f.callTool(t, token, tool, args)
		if failed {
			t.Fatalf("%s %v: %s", tool, args, text)
		}
		noLeak(tool, text)
		return text
	}
	// General: the appearance route's fields; the plan choices as the route
	// answers them once the app has read plan usage, and the tool reads no plan.
	var appearance map[string]any
	_ = json.Unmarshal(f.route(t, "/api/settings/appearance"), &appearance)
	text := call(chat, "settings_general", nil)
	for _, key := range []string{"theme", "key_hints", "page_modifier"} {
		readSameJSON(t, "settings_general "+key, readField(t, text, key), mustJSON(t, appearance[key]))
	}
	if f.fetches.Load() != 0 || !strings.Contains(text, `"provider":"claude"`) {
		t.Errorf("settings_general read %d plans: %s", f.fetches.Load(), text)
	}
	var choices struct {
		Plans json.RawMessage `json:"plans"`
	}
	_ = json.Unmarshal(f.route(t, "/api/settings/plan-usage"), &choices)
	fetched := f.fetches.Load()
	readSameJSON(t, "plan choices", readField(t, call(chat, "settings_general", nil), "plan_usage"), choices.Plans)
	if f.fetches.Load() != fetched {
		t.Errorf("settings_general caused %d plan reads", f.fetches.Load()-fetched)
	}

	// Reviews and fixes: the settings and automatic routes' fields.
	var settings map[string]json.RawMessage
	_ = json.Unmarshal(f.route(t, "/api/settings"), &settings)
	text = call(chat, "settings_reviews", nil)
	for _, key := range []string{"backend", "model", "effort"} {
		readSameJSON(t, "settings_reviews "+key, readField(t, text, key), settings[key])
	}
	if !strings.Contains(text, `"automatic":{"enabled":true,"pull_requests":"authored"}`) || !strings.Contains(text, `"effective_backend":"claude"`) || !strings.Contains(text, `"effective_model":"haiku"`) {
		t.Errorf("settings_reviews: %s", text)
	}
	text = call(chat, "settings_fixes", nil)
	if !strings.Contains(text, `"backend":"codex","model":"gpt-5"`) || !strings.Contains(text, `"same_as_reviews":false`) {
		t.Errorf("settings_fixes: %s", text)
	}

	// Connections: the sign-in waits, and nothing of it is passed on but that.
	// The Codex hooks are what the app last read, never read by the tool.
	text = call(chat, "settings_connections", nil)
	if !strings.Contains(text, `"state":"pending"`) || !strings.Contains(text, `"host":"github.com"`) || !strings.Contains(text, "has not checked the Codex hooks") {
		t.Errorf("settings_connections: %s", text)
	}
	f.route(t, "/api/agent-hooks/codex")
	if f.hookReads.Load() != 1 {
		t.Fatalf("the app's read ran the check %d times", f.hookReads.Load())
	}
	for range 3 {
		if text = call(chat, "settings_connections", nil); !strings.Contains(text, `"codex_hooks":{"state":"untrusted"`) {
			t.Errorf("settings_connections after the app's check: %s", text)
		}
	}
	if f.hookReads.Load() != 1 {
		t.Errorf("settings_connections ran the Codex check %d times", f.hookReads.Load()-1)
	}

	// This computer: names, never values; the agent checks, never their
	// folders, details or fixes.
	text = call(chat, "settings_computer", nil)
	for _, want := range []string{`"job_concurrency":2`, `"review_time_limit_minutes":25`, `"run_time_limit_minutes":50`, `"agent_env_names":["WORK_DIR","ZED_TOKEN"]`, `"agent_env_source":"app"`, `"codex_approvals":true`,
		`{"id":"claude","label":"claude","status":"ok","version":"2.1.0"}`, `{"id":"codex","label":"codex","status":"problem","version":"0.9"}`, `"diagnostics_problems":1`} {
		if !strings.Contains(text, want) {
			t.Errorf("settings_computer lacks %s: %s", want, text)
		}
	}
	noLeak("settings_computer", text, settingsEnvValue, "config file")
	noLeak("diagnostics_summary", call(chat, "diagnostics_summary", nil))

	// Prompts: the routes' entries, the list without texts.
	text = call(chat, "prompts", nil)
	if !strings.Contains(text, `"id":"fix"`) || !strings.Contains(text, `"custom":true`) || strings.Contains(text, "Global custom fix prompt") || strings.Contains(text, "default_text") {
		t.Errorf("prompts: %s", text)
	}
	var global struct {
		Prompts []map[string]json.RawMessage `json:"prompts"`
	}
	_ = json.Unmarshal(f.route(t, "/api/settings/prompts"), &global)
	text = call(chat, "prompt", map[string]any{"id": "fix"})
	for _, entry := range global.Prompts {
		if string(entry["id"]) == `"fix"` {
			readSameJSON(t, "prompt", json.RawMessage(text), mustJSON(t, entry))
		}
	}
	var own struct {
		Prompts []map[string]json.RawMessage `json:"prompts"`
	}
	_ = json.Unmarshal(f.route(t, "/api/repositories/owner/repo/prompts"), &own)
	// A chat reads a repository's prompt as the route gives it, with the
	// global custom text it inherits; an action reads it without that, nor
	// the safety rules a running global text dropped.
	for name, token := range map[string]string{"chat": chat, "action": action} {
		for _, id := range []string{"fix", "fix-checks"} {
			text = call(token, "repository_prompts", map[string]any{"repository": "owner/repo", "id": id})
			for _, entry := range own.Prompts {
				if string(entry["id"]) != `"`+id+`"` {
					continue
				}
				if name == "action" {
					if string(entry["source"]) == `"global"` {
						entry["missing_safety_rules"] = json.RawMessage("null")
					}
					delete(entry, "inherited_text")
					entry["withheld"] = readField(t, text, "withheld")
				}
				readSameJSON(t, name+" repository_prompts "+id, json.RawMessage(text), mustJSON(t, entry))
			}
			noLeak("repository_prompts", text, "FOREIGN")
			if name == "action" {
				noLeak("an action's repository_prompts", text, "Global custom", `"inherited_text"`)
			} else if !strings.Contains(text, "Global custom") {
				t.Errorf("a chat's repository prompt %s lacks the inherited text: %s", id, text)
			}
		}
	}
	if text = call(action, "repository_prompts", map[string]any{"repository": "owner/repo", "id": "fix-checks"}); !strings.Contains(text, `"source":"global"`) || !strings.Contains(text, `"missing_safety_rules":null`) {
		t.Errorf("an action's global-sourced prompt: %s", text)
	}
	if text = call(chat, "prompt", map[string]any{"id": "fix", "repository": "other/repo"}); !strings.Contains(text, "FOREIGN repository prompt.") {
		t.Errorf("a chat's prompt of another repository: %s", text)
	}

	// The repository page: a chat's and the action's own repository.
	for name, token := range map[string]string{"chat": chat, "action": action} {
		text = call(token, "repository_reviews", map[string]any{"repository": "owner/repo"})
		if !strings.Contains(text, `"repository":"owner/repo","automatic":true,"decisions":false,"automatic_status":{"enabled":true`) {
			t.Errorf("%s repository_reviews: %s", name, text)
		}
		text = call(token, "repository_registration", map[string]any{"repository": "owner/repo"})
		if !strings.Contains(text, `"registered":true,"clone_count":1,"available_clones":1`) {
			t.Errorf("%s repository_registration: %s", name, text)
		}
		noLeak(name+" repository tools", text, "other/repo")
	}
	// The action is refused another repository's, and every Settings tool.
	for _, tool := range []string{"repository_reviews", "repository_prompts", "repository_registration"} {
		if text, failed := f.callTool(t, action, tool, map[string]any{"repository": "other/repo"}); !failed || strings.Contains(text, "FOREIGN") {
			t.Errorf("an action's %s of another repository: %v %s", tool, failed, text)
		}
	}
	for _, tool := range []string{"settings_general", "settings_reviews", "settings_fixes", "settings_connections", "settings_computer", "prompts", "prompt", "diagnostics_summary"} {
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": map[string]any{}})
		r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+action)
		w := httptest.NewRecorder()
		f.mcp.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), "unknown tool") {
			t.Errorf("an action called %s: %s", tool, w.Body.String())
		}
	}
}

// settings_reviews resolves decision capture's model through DecisionViews:
// however many calls arrive together or in a row, one resolution (which runs
// the harnesses' version checks) serves them while the settings are
// unchanged, and a change resolves again. The app's route still resolves on
// every read.
func TestSettingsReviewsShareOneDecisionResolution(t *testing.T) {
	f := newSettingsFixture(t)
	chat, _ := f.tokens(t)
	f.resolutions.Store(0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if text, failed := f.callTool(t, chat, "settings_reviews", nil); failed || !strings.Contains(text, `"effective_model":"haiku"`) {
				t.Errorf("settings_reviews: %v %s", failed, text)
			}
		})
	}
	wg.Wait()
	for range 5 {
		f.callTool(t, chat, "settings_reviews", nil)
	}
	if n := f.resolutions.Load(); n != 1 {
		t.Errorf("13 calls resolved the decision model %d times", n)
	}
	// A change to the settings is seen at once, with one more resolution.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "http://local/api/settings/decisions", strings.NewReader(`{"enabled":false}`))
	r.Header.Set("Content-Type", "application/json")
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("PUT decisions: %d %s", w.Code, w.Body.String())
	}
	f.resolutions.Store(0)
	for range 3 {
		if text, _ := f.callTool(t, chat, "settings_reviews", nil); !strings.Contains(text, `"decision_capture":{"enabled":false`) {
			t.Errorf("settings_reviews after the change: %s", text)
		}
	}
	if n := f.resolutions.Load(); n != 1 {
		t.Errorf("the changed settings resolved %d times", n)
	}
	// The app's route is unchanged.
	f.route(t, "/api/settings/decisions")
	f.route(t, "/api/settings/decisions")
	if n := f.resolutions.Load(); n != 3 {
		t.Errorf("the route resolved %d times in two reads", n-1)
	}
}

// The GitHub connection's token, whether from the environment or the app's
// own sign-in, never reaches the Settings tools; the sign-in's organizations
// check, which uses it, runs and its answer is shown without it.
func TestSettingsConnectionsCarryNoGitHubToken(t *testing.T) {
	const envToken, savedToken = "SENTINEL_ENV_GITHUB_TOKEN_VALUE", "SENTINEL_SAVED_GITHUB_TOKEN_VALUE"
	connect := func(t *testing.T, store *credentials) (*readFixture, *atomic.Int32) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		registry, _ := readRegistry(t)
		t.Setenv("HOME", t.TempDir())
		authorized := new(atomic.Int32)
		manager := githubauth.NewWithOptions(store, githubauth.Options{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.Header.Get("Authorization"), savedToken) {
				authorized.Add(1)
			}
			body := `[]`
			switch r.URL.Path {
			case "/user/memberships/orgs":
				body = `[{"organization":{"login":"acme-corp"}},{"organization":{"login":"locked-org"}}]`
			case "/user/orgs":
				body = `[{"login":"acme-corp"}]`
			case "/user":
				body = `{"login":"octo","id":1}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Oauth-Scopes": {"repo, read:org"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})})
		connection := uiserver.NewGitHubAuthentication(ctx, manager, config.Config{}, nil)
		connection.Organizations = func(ctx context.Context, refresh bool) (githubauth.OrganizationAccess, error) {
			c, err := store.Load(ctx)
			if err != nil {
				return githubauth.OrganizationAccess{}, err
			}
			return manager.Organizations(ctx, c.AccessToken, refresh)
		}
		api := uiserver.LocalAPI{Registry: registry, Config: config.Default(), GitHubAuth: connection.Handler()}.Handler()
		t.Cleanup(api.Close)
		return &readFixture{api: api, mcp: agentcontext.New(agentcontext.Options{API: api, Version: "test"})}, authorized
	}
	noToken := func(t *testing.T, what, text string) {
		t.Helper()
		for _, leak := range []string{"SENTINEL", envToken, savedToken, "GITHUB_TOKEN", "access_token"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %s", what, leak, text)
			}
		}
	}
	await := func(t *testing.T, f *readFixture, chat, want string) string {
		t.Helper()
		var text string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			text, _ = f.callTool(t, chat, "settings_connections", nil)
			noToken(t, "settings_connections", text)
			if strings.Contains(text, want) {
				return text
			}
		}
		t.Fatalf("settings_connections never showed %s: %s", want, text)
		return ""
	}

	t.Run("environment", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", envToken)
		t.Setenv("GH_TOKEN", "")
		f, _ := connect(t, &credentials{})
		if body := string(f.route(t, "/api/github-auth")); !strings.Contains(body, "environment variable GITHUB_TOKEN") {
			t.Fatalf("the route does not name the environment: %s", body)
		}
		chat, _ := f.tokens(t)
		await(t, f, chat, `"source":"environment","override":true`)
	})

	t.Run("saved sign-in", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		f, authorized := connect(t, &credentials{c: githubauth.Credential{AccessToken: savedToken, Login: "octo", ID: 1}})
		chat, _ := f.tokens(t)
		text := await(t, f, chat, `"organizations_not_reachable":["locked-org"]`)
		if !strings.Contains(text, `"state":"connected"`) || !strings.Contains(text, `"login":"octo"`) || !strings.Contains(text, `"source":"baum"`) || !strings.Contains(text, `"saved":true`) {
			t.Errorf("settings_connections: %s", text)
		}
		// The organizations check ran with the saved token.
		if authorized.Load() == 0 {
			t.Error("the organizations check never used the saved sign-in")
		}
	})
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
