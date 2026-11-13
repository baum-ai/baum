package planusage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

// The plans of the other Claude Code configuration directories in use are
// read after the sources', each named by its directory with the home written
// as ~, by one reader per directory.
func TestServiceReadsThePlansOfOtherClaudeConfigDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	personal, elsewhere := filepath.Join(home, ".claude.personal"), filepath.Join(t.TempDir(), "claude")
	claude := func(dirs ...string) []Other {
		var others []Other
		for _, dir := range dirs {
			others = append(others, Other{Provider: "claude", Dir: dir})
		}
		return others
	}
	dirs := claude(personal, "relative", personal)
	built := map[string]int{}
	now := clock
	s := &Service{
		Sources: []Source{sourceFunc(func(context.Context) Plan { return Plan{Provider: "claude", Windows: []Window{}} }), &countingSource{}},
		Now:     func() time.Time { return now },
		Others:  func(context.Context) []Other { return dirs },
		OtherIn: func(o Other) Source {
			built[o.Dir]++
			return sourceFunc(func(context.Context) Plan {
				return Plan{Provider: "claude", Available: true, Plan: "Ultra", Windows: []Window{}}
			})
		},
	}
	plans := s.Plans(context.Background(), false)
	// It follows the Claude plan it belongs with, before the next provider's.
	if len(plans) != 3 || plans[0].ConfigDir != "" || plans[1].Provider != "claude" || plans[1].ConfigDir != "~/.claude.personal" || plans[1].Plan != "Ultra" || plans[2].Provider != "x" {
		t.Fatalf("plans = %+v", plans)
	}
	// A change of the directories reads again at once, whatever the cache's
	// age; the same directories are served from it.
	source := s.Sources[1].(*countingSource)
	if s.Plans(context.Background(), false); source.n != 1 {
		t.Fatalf("read %d times for the same directories", source.n)
	}
	dirs = claude(personal, elsewhere)
	plans = s.Plans(context.Background(), false)
	if source.n != 2 || len(plans) != 4 || plans[2].ConfigDir != elsewhere || plans[3].Provider != "x" {
		t.Fatalf("after a change: read %d times, %+v", source.n, plans)
	}
	dirs = nil
	if plans = s.Plans(context.Background(), false); len(plans) != 2 {
		t.Fatalf("no other directory: %+v", plans)
	}
	if built[personal] != 1 || built[elsewhere] != 1 || len(built) != 2 {
		t.Fatalf("readers built: %v", built)
	}
}

// A directory's credential is its own .credentials.json, not the one of this
// process's CLAUDE_CONFIG_DIR.
func TestStoredCredentialInReadsTheDirectorysOwnCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	own, other := filepath.Join(home, ".claude"), filepath.Join(home, ".claude.personal")
	t.Setenv("CLAUDE_CONFIG_DIR", own)
	for dir, token := range map[string]string{own: "own-token", other: "other-token"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"claudeAiOauth":{"accessToken":"` + token + `","subscriptionType":"pro"}}`
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// On macOS the Keychain is asked first; these directories have no item.
	if cred, err := StoredCredentialIn(context.Background(), other); err != nil || cred.Token != "other-token" || cred.Plan != "Pro" {
		t.Fatalf("other = %+v, %v", cred.Plan, err)
	}
	if _, err := StoredCredentialIn(context.Background(), filepath.Join(home, "missing")); err == nil {
		t.Fatal("a directory without a credential has one")
	}
	if got := keychainServiceFor("/opt/claude-work"); len(got) != len(keychainService)+9 || got[:len(keychainService)+1] != keychainService+"-" {
		t.Fatalf("keychain item %q", got)
	}
}

// The history is kept by provider and window, so a second directory's plan
// is not sampled into the first's.
func TestHistoryLeavesOutThePlansOfOtherConfigDirectories(t *testing.T) {
	now := clock
	used := 10.0
	resets := clock.Add(5 * time.Hour)
	plan := func(dir string, used float64) Plan {
		return Plan{Provider: "claude", Available: true, ConfigDir: dir, FetchedAt: now, Windows: []Window{{Name: "5-hour", UsedPercent: &used, ResetsAt: &resets}}}
	}
	s := &Service{Now: func() time.Time { return now }, Sources: []Source{
		sourceFunc(func(context.Context) Plan { return plan("", used) }),
		sourceFunc(func(context.Context) Plan { return plan("~/.claude.personal", 90) }),
	}}
	h := &History{Path: filepath.Join(appdbtest.TempDir(t), "p.json"), Now: func() time.Time { return now }, Service: s}
	if err := h.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.Since(time.Time{}); len(got) != 1 || got[0].UsedPercent != 10 {
		t.Fatalf("samples = %+v", got)
	}
}

// A Codex home's plan is read with that home's sign-in and session files, and
// follows the Codex plan as a second Claude plan follows the Claude one.
func TestServiceReadsThePlansOfOtherCodexHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := filepath.Join(home, ".codex-work")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	now := clock
	s := &Service{
		Sources: []Source{
			sourceFunc(func(context.Context) Plan { return Plan{Provider: "claude", Windows: []Window{}} }),
			sourceFunc(func(context.Context) Plan { return Plan{Provider: "codex", Windows: []Window{}} }),
			&countingSource{},
		},
		Now: func() time.Time { return now },
		Others: func(context.Context) []Other {
			return []Other{{Provider: "codex", Dir: work}, {Provider: "claude", Dir: filepath.Join(home, ".claude.personal")}}
		},
		OtherIn: func(o Other) Source {
			return sourceFunc(func(context.Context) Plan { return Plan{Provider: o.Provider, Windows: []Window{}} })
		},
	}
	plans := s.Plans(context.Background(), false)
	var order []string
	for _, p := range plans {
		order = append(order, p.Provider+" "+p.ConfigDir)
	}
	if want := []string{"claude ", "claude ~/.claude.personal", "codex ", "codex ~/.codex-work", "x "}; !slices.Equal(order, want) {
		t.Fatalf("order = %q, want %q", order, want)
	}

	// The default reader reads the home's own session files, not those of
	// this process's CODEX_HOME.
	reader, ok := NewOtherIn(Other{Provider: "codex", Dir: work}).(*Codex)
	if !ok || reader.home() != work {
		t.Fatalf("reader = %#v", reader)
	}
	reader.Now = func() time.Time { return now }
	reader.Credential = func(context.Context) (CodexCredential, error) { return CodexCredential{}, errNoCredential }
	if p := reader.Read(context.Background()); p.Available || !strings.Contains(p.Reason, "No Codex sessions") {
		t.Fatalf("an empty home: %+v", p)
	}
	if _, ok := NewOtherIn(Other{Provider: "claude", Dir: work}).(*Claude); !ok {
		t.Fatal("a Claude Code directory is not read by the Claude reader")
	}
}

// A Codex home's sign-in is its own auth.json.
func TestCodexReaderUsesTheHomesOwnCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "own"))
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"tokens":{"access_token":"work-token","account_id":"acct"}}`
	if err := os.WriteFile(filepath.Join(work, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	reader := NewOtherIn(Other{Provider: "codex", Dir: work}).(*Codex)
	reader.URL = srv.URL
	reader.Read(context.Background())
	if seen != "Bearer work-token" {
		t.Fatalf("asked with %q", seen)
	}
}
