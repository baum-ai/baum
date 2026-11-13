package automatic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
)

// The settings page chooses the Claude Code configuration directory of
// reviews and of fixes. A chosen directory must exist, is stored absolute and
// is remembered for reading its sessions; an empty one goes back to the
// environment's.
func TestPutChoosesTheClaudeConfigDirectories(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	personal, work, signedOut := filepath.Join(home, ".claude.personal"), filepath.Join(home, ".claude-work"), filepath.Join(home, ".claude-signed-out")
	for _, dir := range []string{personal, work, signedOut} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	h.scheduler.ClaudeSignedIn = func(_ context.Context, dir string) (bool, bool) {
		asked = append(asked, dir)
		// Claude Code cannot say for the work directory, which is accepted.
		return dir != signedOut, dir != work
	}
	prefs := func() (string, string) {
		t.Helper()
		p, err := h.registry.Preferences(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		review, fix := "", ""
		if p.ClaudeConfigDir != nil {
			review = *p.ClaudeConfigDir
		}
		if p.FixClaudeConfigDir != nil {
			fix = *p.FixClaudeConfigDir
		}
		return review, fix
	}

	for _, body := range []string{
		`{"claude_config_dir":"relative"}`,
		`{"claude_config_dir":"~/missing"}`,
		`{"claude_config_dir":` + quote(filepath.Join(home, ".config", "file")) + `}`,
		// A fix directory needs the fix agent chosen.
		`{"fix_claude_config_dir":"~/.claude-work"}`,
	} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s = %d", body, code)
		}
	}
	if review, fix := prefs(); review != "" || fix != "" {
		t.Fatalf("a rejected change was saved: %q %q", review, fix)
	}
	// Only a directory that exists is asked about.
	if !slices.Equal(asked, []string{work}) {
		t.Fatalf("asked about %v", asked)
	}
	// One Claude Code is signed out with is refused with what to do, and is
	// neither stored nor remembered.
	w := httptest.NewRecorder()
	h.scheduler.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/automatic", strings.NewReader(`{"claude_config_dir":"~/.claude-signed-out"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "not signed in with "+signedOut) || !strings.Contains(w.Body.String(), "/login") {
		t.Fatalf("signed out: %d %s", w.Code, w.Body.String())
	}
	if review, _ := prefs(); review != "" || slices.Contains(agentdirs.Claude.Recorded(), signedOut) {
		t.Fatalf("a signed-out directory was kept: %q %v", review, agentdirs.Claude.Recorded())
	}

	if code := put(h.scheduler, `{"claude_config_dir":" ~/.claude.personal/ "}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != personal || fix != "" {
		t.Fatalf("reviews: %q %q", review, fix)
	}
	if code := put(h.scheduler, `{"fix_backend":"claude","fix_model":"m","fix_claude_config_dir":`+quote(work)+`}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != personal || fix != work {
		t.Fatalf("fixes: %q %q", review, fix)
	}
	// Both are known to baum from then on, the fix's refused one above too,
	// since it exists: knowing a directory only lets its sessions be read.
	if got := agentdirs.Claude.Recorded(); !slices.Equal(got, []string{work, personal}) {
		t.Fatalf("recorded %v", got)
	}
	// Other choices leave the directories alone.
	if code := put(h.scheduler, `{"model":"other"}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != personal || fix != work {
		t.Fatalf("after another change: %q %q", review, fix)
	}
	// Fixes that follow reviews again have no directory of their own.
	if code := put(h.scheduler, `{"fix_backend":"","fix_model":"","fix_claude_config_dir":""}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != personal || fix != "" {
		t.Fatalf("following reviews: %q %q", review, fix)
	}
	if code := put(h.scheduler, `{"claude_config_dir":""}`); code != 200 {
		t.Fatal(code)
	}
	if review, _ := prefs(); review != "" {
		t.Fatalf("cleared: %q", review)
	}
}

func quote(s string) string { return `"` + filepath.ToSlash(s) + `"` }
