package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// codexHomes gives the test its own home and record, and makes the work and
// personal Codex homes.
func codexHomes(t *testing.T) (home, work, personal string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	t.Setenv(agentdirs.Codex.Env, "")
	work, personal = filepath.Join(home, ".codex-work"), filepath.Join(home, ".codex.personal")
	for _, dir := range []string{work, personal} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, work, personal
}

// GET /api/settings reports the Codex homes as it does the Claude Code
// directories.
func TestLocalAPIReportsTheCodexHomes(t *testing.T) {
	home, work, personal := codexHomes(t)
	def := filepath.Join(home, ".codex")
	if _, err := agentdirs.Codex.Remember(work); err != nil {
		t.Fatal(err)
	}
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	type homes struct {
		Review  string   `json:"codex_home"`
		Fix     string   `json:"fix_codex_home"`
		Default string   `json:"default_codex_home"`
		Known   []string `json:"codex_homes"`
		Claude  []string `json:"claude_config_dirs"`
	}
	current := func() homes {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body homes
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(rec.Body.String(), err)
		}
		return body
	}
	if got := current(); got.Review != "" || got.Fix != "" || got.Default != def || !slices.Equal(got.Known, []string{def, work}) || !slices.Equal(got.Claude, []string{filepath.Join(home, ".claude")}) {
		t.Fatalf("nothing chosen: %+v", got)
	}
	codex := config.BackendCodex
	update := func(change func(*repos.Preferences)) {
		t.Helper()
		if _, err := registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
			change(&p)
			return p
		}); err != nil {
			t.Fatal(err)
		}
	}
	update(func(p *repos.Preferences) { p.CodexHome, p.FixCodexHome = &work, &personal })
	if got := current(); got.Review != work || got.Fix != "" {
		t.Fatalf("reviews: %+v", got)
	}
	update(func(p *repos.Preferences) { p.FixBackend = &codex })
	if got := current(); got.Review != work || got.Fix != personal {
		t.Fatalf("fixes: %+v", got)
	}
	t.Setenv(agentdirs.Codex.Env, personal)
	if got := current(); got.Default != personal || !slices.Equal(got.Known, []string{def, personal, work}) {
		t.Fatalf("the daemon's own: %+v", got)
	}
	update(func(p *repos.Preferences) { p.AgentEnv = map[string]string{"CODEX_HOME": "/srv/codex"} })
	if got := current(); got.Default != "/srv/codex" {
		t.Fatalf("agent_env's: %+v", got)
	}
}

// PUT /api/settings/decisions chooses extraction's own Codex home, checked
// with Codex's sign-in, and only with an agent of its own.
func TestDecisionSettingsChooseACodexHome(t *testing.T) {
	home, work, _ := codexHomes(t)
	signedOut := filepath.Join(home, ".codex-signed-out")
	if err := os.MkdirAll(signedOut, 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	handler := LocalAPI{Registry: registry, Config: config.Default(),
		CodexSignedIn: func(_ context.Context, dir string) (bool, bool) { return dir != signedOut, true },
		ClaudeSignedIn: func(context.Context, string) (bool, bool) {
			t.Error("Claude Code was asked about a Codex home")
			return false, false
		}}.Handler()
	defer handler.Close()
	put := func(body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings/decisions", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	saved := func() string {
		t.Helper()
		p, err := registry.Preferences(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if p.Decisions.CodexHome == nil {
			return ""
		}
		return *p.Decisions.CodexHome
	}
	for body, want := range map[string]string{
		`{"enabled":true,"codex_home":"~/.codex-work"}`:                         "choose the agent for data extraction",
		`{"enabled":true,"backend":"codex","codex_home":"~/.codex-signed-out"}`: "CODEX_HOME=" + signedOut + " codex login",
	} {
		if code, out := put(body); code != 400 || !strings.Contains(out, want) {
			t.Errorf("%s = %d %s", body, code, out)
		}
	}
	if code, out := put(`{"enabled":true,"backend":"codex","codex_home":"~/.codex-work"}`); code != 200 || !strings.Contains(out, `"codex_home":"`+filepath.ToSlash(work)+`"`) || !strings.Contains(out, `"claude_config_dir":""`) {
		t.Fatalf("choose = %d %s", code, out)
	}
	if saved() != work || !slices.Contains(agentdirs.Codex.Recorded(), work) || len(agentdirs.Claude.Recorded()) != 0 {
		t.Fatalf("saved %q, recorded %v / %v", saved(), agentdirs.Codex.Recorded(), agentdirs.Claude.Recorded())
	}
	if code, _ := put(`{"enabled":true,"backend":"codex","model":"m"}`); code != 200 || saved() != work {
		t.Fatalf("kept: %d %q", code, saved())
	}
	p, _ := registry.Preferences(context.Background())
	if got := p.ResolveExtraction(p.Resolve(config.Default())).CodexHome; got != work {
		t.Fatalf("extraction runs with %q", got)
	}
	if code, _ := put(`{"enabled":true,"backend":""}`); code != 200 || saved() != "" {
		t.Fatalf("following reviews: %d %q", code, saved())
	}
}
