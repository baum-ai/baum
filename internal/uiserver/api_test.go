package uiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Picking and unpicking a repository reach the registry and wake the
// scheduler like clone changes do.
func TestRepositoriesRoutesNotifyOnWrites(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	changed := 0
	handler := LocalAPI{Registry: registry, Config: config.Default(), Changed: func() { changed++ }}.Handler()
	defer handler.Close()
	for _, tt := range []struct {
		method, path, body string
		code, changed      int
	}{
		{"POST", "/api/repositories", `{"repository":"owner/repo"}`, 200, 1},
		{"GET", "/api/repositories", "", 200, 1},
		{"DELETE", "/api/repositories/owner/repo", "", 204, 2},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tt.method, "http://local"+tt.path, strings.NewReader(tt.body)))
		if w.Code != tt.code || changed != tt.changed {
			t.Fatalf("%s %s: %d %s, changed %d", tt.method, tt.path, w.Code, w.Body.String(), changed)
		}
	}
}

func TestLocalAPISelectorsAndSecretFreeSettings(t *testing.T) {
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	dir := appdbtest.TempDir(t)
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	clone, err := registry.Add(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const id = "att_0123456789abcdef0123456789abcdef"
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	document := []byte(fmt.Sprintf(`{"review":{"repository":"owner/repo","pull_request":7,"version":1},"index":{"attachments":[{"id":%q}]}}`, id))
	if err = versions.Put(ctx, store.Key{Repository: "owner/repo", Number: 7}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document, Attachments: []store.Attachment{{ID: id, ContentType: "image/png", Bytes: png}}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.GitHubToken = "secret-github"
	cfg.AnthropicAPIKey = "secret-model"
	handler := LocalAPI{Registry: registry, Config: cfg}.Handler()
	defer handler.Close()
	selector := "?clone=" + clone.ID + "&pr=7"
	for _, tt := range []struct {
		path string
		code int
	}{{"/api/repos", 200}, {"/api/repositories", 200}, {"/api/settings", 200}, {"/api/diagnostics", 200}, {"/api/document" + selector, 200}, {"/api/versions" + selector, 200}, {"/api/sessions" + selector, 200}, {"/api/attachments/" + id + selector, 200}, {"/api/document?clone=/tmp&pr=7", 404}, {"/api/document?clone=" + clone.ID + "&pr=8", 404}, {"/api/document?pr=7", 400}, {"/api/attachments/att_ffffffffffffffffffffffffffffffff" + selector, 404}, {"/api/jobs", 200}} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+tt.path, nil))
			if w.Code != tt.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret-github") || strings.Contains(w.Body.String(), "secret-model") {
				t.Fatal("credentials exposed")
			}
			if strings.HasPrefix(tt.path, "/api/attachments/"+id) && w.Header().Get("Content-Type") != "image/png" {
				t.Fatal("wrong media type")
			}
		})
	}
	// Registry writes are still rejected by the enclosing server before routing.
	s, err := New(Options{API: handler})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.listener.Close() }()
	w := request(t, s, "DELETE", "/api/repos/"+clone.ID, "", "https://evil.example", "application/json", "Bearer "+s.launch, "{}")
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	list, err := registry.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("CSRF removed registry record", err)
	}
}

func TestLocalAPIJobConcurrency(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	applied := 0
	handler := LocalAPI{Registry: registry, Config: config.Default(), ConcurrencyChanged: func(n int) { applied = n }}.Handler()
	defer handler.Close()
	current := func() int {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body struct {
			JobConcurrency int `json:"job_concurrency"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(rec.Body.String(), err)
		}
		return body.JobConcurrency
	}
	put := func(body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings/job-concurrency", strings.NewReader(body)))
		return rec.Code
	}
	if n := current(); n != repos.MaxJobConcurrency {
		t.Fatalf("default = %d", n)
	}
	if code := put(`{"job_concurrency":2}`); code != 200 || applied != 2 || current() != 2 {
		t.Fatalf("PUT = %d, applied %d, now %d", code, applied, current())
	}
	for _, body := range []string{`{"job_concurrency":0}`, `{"job_concurrency":5}`, `{}`, `{"concurrency":2}`} {
		if code := put(body); code != 400 {
			t.Errorf("%s = %d", body, code)
		}
	}
	if applied != 2 || current() != 2 {
		t.Fatalf("a refused PUT changed it: applied %d, now %d", applied, current())
	}
}

func TestLocalAPITimeLimits(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	type limits struct {
		Review int `json:"review_time_limit_minutes"`
		Run    int `json:"run_time_limit_minutes"`
	}
	current := func() limits {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body limits
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(rec.Body.String(), err)
		}
		return body
	}
	put := func(body string) (int, limits) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings/time-limits", strings.NewReader(body)))
		var answer limits
		_ = json.Unmarshal(rec.Body.Bytes(), &answer)
		return rec.Code, answer
	}
	if got := current(); got != (limits{10, 30}) {
		t.Fatalf("defaults = %+v", got)
	}
	if code, answer := put(`{"review_time_limit_minutes":15,"run_time_limit_minutes":60}`); code != 200 || answer != (limits{15, 60}) || current() != answer {
		t.Fatalf("PUT = %d %+v, now %+v", code, answer, current())
	}
	prefs, err := registry.Preferences(context.Background())
	if err != nil || prefs.ReviewTimeLimit != 15 || prefs.RunTimeLimit != 60 {
		t.Fatalf("stored %+v, %v", prefs, err)
	}
	for _, body := range []string{`{"review_time_limit_minutes":0,"run_time_limit_minutes":30}`, `{"review_time_limit_minutes":10,"run_time_limit_minutes":241}`, `{"review_time_limit_minutes":10}`, `{}`, `{"review_minutes":10,"run_time_limit_minutes":30}`} {
		if code, _ := put(body); code != 400 {
			t.Errorf("%s = %d", body, code)
		}
	}
	if current() != (limits{15, 60}) {
		t.Fatalf("a refused PUT changed them: %+v", current())
	}
}

func TestLocalAPIAgentEnvironment(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	cfg := config.Default()
	cfg.AgentEnv = map[string]string{"FROM_FILE": "1"}
	handler := LocalAPI{Registry: registry, Config: cfg}.Handler()
	defer handler.Close()
	settings := func() (env map[string]string, source string) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body struct {
			AgentEnv       map[string]string `json:"agent_env"`
			AgentEnvSource string            `json:"agent_env_source"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(rec.Body.String(), err)
		}
		return body.AgentEnv, body.AgentEnvSource
	}
	put := func(body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings/agent-env", strings.NewReader(body)))
		return rec.Code
	}
	fixAgent := func() (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body struct {
			FixBackend *string `json:"fix_backend"`
			FixModel   *string `json:"fix_model"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.FixBackend == nil || body.FixModel == nil {
			t.Fatal(rec.Body.String(), err)
		}
		return *body.FixBackend, *body.FixModel
	}
	if b, m := fixAgent(); b != "" || m != "" {
		t.Fatalf("unset fix agent %q %q", b, m)
	}
	if _, err := registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
		backend, model := config.BackendCodex, "gpt-fix"
		p.FixBackend, p.FixModel = &backend, &model
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if b, m := fixAgent(); b != "codex" || m != "gpt-fix" {
		t.Fatalf("fix agent %q %q", b, m)
	}
	if _, err := registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
		p.FixBackend, p.FixModel = nil, nil
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if env, source := settings(); env["FROM_FILE"] != "1" || source != "file" {
		t.Fatalf("before a choice: %v from %q", env, source)
	}
	if code := put(`{"agent_env":{"GIT_CONFIG_GLOBAL":"~/agent.gitconfig"}}`); code != 200 {
		t.Fatalf("PUT = %d", code)
	}
	if env, source := settings(); len(env) != 1 || env["GIT_CONFIG_GLOBAL"] != "~/agent.gitconfig" || source != "app" {
		t.Fatalf("after the app's choice: %v from %q", env, source)
	}
	if code := put(`{"agent_env":{"1BAD":"x"}}`); code != 400 {
		t.Fatalf("an invalid name = %d", code)
	}
	if code := put(`{"agent_env":{}}`); code != 200 {
		t.Fatalf("clearing = %d", code)
	}
	if env, source := settings(); env["FROM_FILE"] != "1" || source != "file" {
		t.Fatalf("after clearing: %v from %q", env, source)
	}
}
