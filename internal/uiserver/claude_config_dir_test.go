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

// GET /api/settings reports the Claude Code configuration directories chosen
// for reviews and fixes, the one a run gets without a choice, and every one
// baum knows.
func TestLocalAPIReportsTheClaudeConfigDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	def, personal, work := filepath.Join(home, ".claude"), filepath.Join(home, ".claude.personal"), filepath.Join(home, ".claude-work")
	for _, dir := range []string{personal, work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := agentdirs.Claude.Remember(dir); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	cfg := config.Default()
	handler := LocalAPI{Registry: registry, Config: cfg}.Handler()
	defer handler.Close()
	type dirs struct {
		Review  string   `json:"claude_config_dir"`
		Fix     string   `json:"fix_claude_config_dir"`
		Default string   `json:"default_claude_config_dir"`
		Known   []string `json:"claude_config_dirs"`
	}
	current := func() dirs {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		var body dirs
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(rec.Body.String(), err)
		}
		return body
	}
	if got := current(); got.Review != "" || got.Fix != "" || got.Default != def || !slices.Equal(got.Known, []string{def, personal, work}) {
		t.Fatalf("nothing chosen: %+v", got)
	}
	claude := config.BackendClaude
	update := func(change func(*repos.Preferences)) {
		t.Helper()
		if _, err := registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
			change(&p)
			return p
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A fix directory shows only with the fix agent it belongs to.
	update(func(p *repos.Preferences) { p.ClaudeConfigDir, p.FixClaudeConfigDir = &personal, &work })
	if got := current(); got.Review != personal || got.Fix != "" {
		t.Fatalf("reviews: %+v", got)
	}
	update(func(p *repos.Preferences) { p.FixBackend = &claude })
	if got := current(); got.Review != personal || got.Fix != work {
		t.Fatalf("fixes: %+v", got)
	}
	// The default is what a run gets without a choice: agent_env's, else the
	// daemon's own.
	t.Setenv(agentdirs.Claude.Env, work)
	if got := current(); got.Default != work {
		t.Fatalf("the daemon's own: %+v", got)
	}
	update(func(p *repos.Preferences) { p.AgentEnv = map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude.personal"} })
	if got := current(); got.Default != personal {
		t.Fatalf("agent_env's: %+v", got)
	}
}

// PUT /api/settings/decisions chooses extraction's own Claude Code
// configuration directory, checked as Settings' others are, and only with an
// agent of its own; following the reviews' agent again drops it.
func TestDecisionSettingsChooseAClaudeConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	personal, signedOut := filepath.Join(home, ".claude.personal"), filepath.Join(home, ".claude-signed-out")
	for _, dir := range []string{personal, signedOut} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	handler := LocalAPI{Registry: registry, Config: config.Default(), ClaudeSignedIn: func(_ context.Context, dir string) (bool, bool) {
		return dir != signedOut, true
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
		if p.Decisions.ClaudeConfigDir == nil {
			return ""
		}
		return *p.Decisions.ClaudeConfigDir
	}
	for body, want := range map[string]string{
		`{"enabled":true,"claude_config_dir":"~/.claude.personal"}`:                      "choose the agent for data extraction",
		`{"enabled":true,"backend":"claude","claude_config_dir":"~/.claude-signed-out"}`: "not signed in with " + signedOut,
		`{"enabled":true,"backend":"claude","claude_config_dir":"~/missing"}`:            "not a directory",
	} {
		if code, out := put(body); code != 400 || !strings.Contains(out, want) {
			t.Errorf("%s = %d %s", body, code, out)
		}
	}
	if saved() != "" {
		t.Fatalf("a refused directory was saved: %q", saved())
	}
	if code, out := put(`{"enabled":true,"backend":"claude","claude_config_dir":"~/.claude.personal"}`); code != 200 || !strings.Contains(out, `"claude_config_dir":"`+filepath.ToSlash(personal)+`"`) {
		t.Fatalf("choose = %d %s", code, out)
	}
	if saved() != personal || !slices.Contains(agentdirs.Claude.Recorded(), personal) {
		t.Fatalf("saved %q, recorded %v", saved(), agentdirs.Claude.Recorded())
	}
	// A save without the field keeps it, as an older app sends none.
	if code, _ := put(`{"enabled":true,"backend":"claude","model":"m"}`); code != 200 || saved() != personal {
		t.Fatalf("kept: %d %q", code, saved())
	}
	p, _ := registry.Preferences(context.Background())
	if got := p.ResolveExtraction(p.Resolve(config.Default())).ClaudeConfigDir; got != personal {
		t.Fatalf("extraction runs with %q", got)
	}
	// Following the reviews' agent again drops it.
	if code, _ := put(`{"enabled":true,"backend":""}`); code != 200 || saved() != "" {
		t.Fatalf("following reviews: %d %q", code, saved())
	}
}
