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

// The settings page chooses the Codex home of reviews and of fixes, under
// the rules of the Claude Code directories, with Codex's own sign-in check
// and its own record.
func TestPutChoosesTheCodexHomes(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	t.Setenv(agentdirs.Codex.Env, "")
	work, personal, signedOut := filepath.Join(home, ".codex-work"), filepath.Join(home, ".codex.personal"), filepath.Join(home, ".codex-signed-out")
	for _, dir := range []string{work, personal, signedOut} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	h.scheduler.CodexSignedIn = func(_ context.Context, dir string) (bool, bool) {
		asked = append(asked, dir)
		return dir != signedOut, true
	}
	h.scheduler.ClaudeSignedIn = func(context.Context, string) (bool, bool) {
		t.Error("Claude Code was asked about a Codex home")
		return false, false
	}
	prefs := func() (string, string) {
		t.Helper()
		p, err := h.registry.Preferences(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		review, fix := "", ""
		if p.CodexHome != nil {
			review = *p.CodexHome
		}
		if p.FixCodexHome != nil {
			fix = *p.FixCodexHome
		}
		return review, fix
	}

	for _, body := range []string{
		`{"codex_home":"relative"}`,
		`{"codex_home":"~/missing"}`,
		`{"fix_codex_home":"~/.codex.personal"}`,
	} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s = %d", body, code)
		}
	}
	w := httptest.NewRecorder()
	h.scheduler.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/automatic", strings.NewReader(`{"codex_home":"~/.codex-signed-out"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Codex is not signed in with "+signedOut) || !strings.Contains(w.Body.String(), "CODEX_HOME="+signedOut+" codex login") {
		t.Fatalf("signed out: %d %s", w.Code, w.Body.String())
	}
	if review, fix := prefs(); review != "" || fix != "" {
		t.Fatalf("a rejected change was saved: %q %q", review, fix)
	}

	if code := put(h.scheduler, `{"backend":"codex","codex_home":"~/.codex-work"}`); code != 200 {
		t.Fatal(code)
	}
	if code := put(h.scheduler, `{"fix_backend":"codex","fix_model":"m","fix_codex_home":`+quote(personal)+`}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != work || fix != personal {
		t.Fatalf("chosen: %q %q", review, fix)
	}
	if got := agentdirs.Codex.Recorded(); !slices.Equal(got, []string{personal, work}) {
		t.Fatalf("recorded %v", got)
	}
	if got := agentdirs.Claude.Recorded(); len(got) != 0 {
		t.Fatalf("Claude Code's record: %v", got)
	}
	if !slices.Contains(asked, work) || !slices.Contains(asked, personal) {
		t.Fatalf("asked about %v", asked)
	}
	// Fixes that follow reviews again drop their home.
	if code := put(h.scheduler, `{"fix_backend":""}`); code != 200 {
		t.Fatal(code)
	}
	if review, fix := prefs(); review != work || fix != "" {
		t.Fatalf("following reviews: %q %q", review, fix)
	}
	if code := put(h.scheduler, `{"codex_home":""}`); code != 200 {
		t.Fatal(code)
	}
	if review, _ := prefs(); review != "" {
		t.Fatalf("cleared: %q", review)
	}
}
