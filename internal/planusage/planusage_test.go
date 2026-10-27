package planusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "sk-ant-oat01-SECRET-TOKEN"

var clock = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func writeRollout(t *testing.T, home, day, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexRead(home string) Plan {
	return (&Codex{Home: home, Now: func() time.Time { return clock }}).Read(context.Background())
}

func TestCodexNewFormatUsesNewestSnapshot(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/25", "rollout-2026-10-25T09-00-00-a.jsonl",
		`{"timestamp":"2026-10-25T09:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":99,"window_minutes":300,"resets_at":1793100000}}}}`)
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T10-00-00-b.jsonl",
		`{"timestamp":"2026-10-26T10:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_at":1793100000},"secondary":{"used_percent":20.5,"window_minutes":10080,"resets_at":1793500000},"plan_type":"pro"}}}`,
		`{"timestamp":"2026-10-26T10:05:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":42,"window_minutes":300,"resets_at":1793100000},"secondary":{"used_percent":21,"window_minutes":10080,"resets_at":1793500000},"plan_type":"pro"}}}`,
		`{"timestamp":"2026-10-26T10:06:00Z","type":"event_msg","payload":{"type":"agent_message"}}`,
		`not json rate_limits`)
	p := codexRead(home)
	if !p.Available || p.Plan != "pro" || len(p.Windows) != 2 {
		t.Fatalf("%+v", p)
	}
	if *p.Windows[0].UsedPercent != 42 || p.Windows[0].Name != "5-hour" || p.Windows[1].Name != "weekly" {
		t.Fatalf("%+v", p.Windows)
	}
	if want := time.Date(2026, 10, 26, 10, 5, 0, 0, time.UTC); !p.FetchedAt.Equal(want) {
		t.Fatalf("snapshot time %v", p.FetchedAt)
	}
	if !p.Windows[0].ResetsAt.Equal(time.Unix(1793100000, 0)) {
		t.Fatalf("reset %v", p.Windows[0].ResetsAt)
	}
}

func TestCodexOldFormatResetsInSeconds(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T11-00-00-a.jsonl",
		`{"timestamp":"2026-10-26T11:30:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":30,"window_minutes":300,"resets_in_seconds":7200}}}}`)
	p := codexRead(home)
	if !p.Available || len(p.Windows) != 1 {
		t.Fatalf("%+v", p)
	}
	want := time.Date(2026, 10, 26, 13, 30, 0, 0, time.UTC)
	if !p.Windows[0].ResetsAt.Equal(want) || p.Windows[0].UsedPercent == nil {
		t.Fatalf("%+v", p.Windows[0])
	}
}

func TestCodexPassedResetHasNoFigure(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T01-00-00-a.jsonl",
		`{"timestamp":"2026-10-26T01:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":90,"window_minutes":300,"resets_in_seconds":600}}}}`)
	w := codexRead(home).Windows[0]
	if !w.Reset || w.UsedPercent != nil {
		t.Fatalf("%+v", w)
	}
}

func TestCodexSkipsFilesWithoutSnapshotsAndReportsMissing(t *testing.T) {
	home := t.TempDir()
	if p := codexRead(home); p.Available || !strings.Contains(p.Reason, "No Codex sessions") {
		t.Fatalf("%+v", p)
	}
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T09-00-00-a.jsonl",
		`{"timestamp":"2026-10-26T09:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":5,"window_minutes":300,"resets_in_seconds":36000}}}}`)
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T10-00-00-b.jsonl",
		`{"timestamp":"2026-10-26T10:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":null}}`)
	if p := codexRead(home); !p.Available || *p.Windows[0].UsedPercent != 5 {
		t.Fatalf("%+v", p)
	}
	empty := t.TempDir()
	writeRollout(t, empty, "2026/10/26", "rollout-x.jsonl", `{"payload":{"type":"agent_message"}}`)
	if p := codexRead(empty); p.Available || p.Reason == "" {
		t.Fatalf("%+v", p)
	}
}

func TestCodexHomeFromEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got := (&Codex{}).home(); got != home {
		t.Fatal(got)
	}
}

func claudeServer(t *testing.T, status int, body string, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func claudeRead(url string, cred Credential) Plan {
	c := &Claude{URL: url, Now: func() time.Time { return clock },
		Credential: func(context.Context) (Credential, error) { return cred, nil }}
	return c.Read(context.Background())
}

func TestClaudeParsesWindowsLeniently(t *testing.T) {
	var seen http.Header
	srv := claudeServer(t, 200, `{"five_hour":{"utilization":62.5,"resets_at":"2026-10-26T15:00:00Z"},
		"seven_day":{"utilization":10,"resets_at":null},"seven_day_opus":null,
		"seven_day_sonnet":{"utilization":"bad"},"extra_usage":{"x":1},"future":5}`, &seen)
	p := claudeRead(srv.URL, Credential{Token: secret, ExpiresAt: clock.Add(time.Hour)})
	if !p.Available || len(p.Windows) != 2 || p.Windows[0].Name != "5-hour" || *p.Windows[0].UsedPercent != 62.5 || p.Windows[1].Name != "weekly" {
		t.Fatalf("%+v", p)
	}
	if p.Windows[1].ResetsAt != nil || !p.Windows[0].ResetsAt.Equal(time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", p.Windows)
	}
	if seen.Get("Authorization") != "Bearer "+secret || seen.Get("anthropic-beta") != "oauth-2025-04-20" {
		t.Fatalf("headers %v", seen)
	}
	assertNoSecret(t, p)
}

func TestClaudeFailuresAreWordsWithoutToken(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		reason string
	}{
		"unauthorized": {401, `{"error":"` + secret + `"}`, "Open Claude Code"},
		"server error": {500, secret, "unavailable right now"},
		"garbage":      {200, "<html>" + secret, "does not understand"},
		"no windows":   {200, `{"five_hour":null}`, "does not understand"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := claudeServer(t, tc.status, tc.body, nil)
			p := claudeRead(srv.URL, Credential{Token: secret})
			if p.Available || !strings.Contains(p.Reason, tc.reason) {
				t.Fatalf("%+v", p)
			}
			assertNoSecret(t, p)
		})
	}
	p := claudeRead("http://127.0.0.1:1/"+secret, Credential{Token: secret})
	if p.Available || p.Reason == "" {
		t.Fatalf("%+v", p)
	}
	assertNoSecret(t, p)
}

func TestClaudeExpiredTokenIsNotSentOrRefreshed(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	p := claudeRead(srv.URL, Credential{Token: secret, ExpiresAt: clock.Add(-time.Minute)})
	if called || p.Available || p.Reason != "Open Claude Code to refresh its sign-in." {
		t.Fatalf("called=%v %+v", called, p)
	}
}

func TestClaudeMissingCredential(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	c := &Claude{Now: func() time.Time { return clock }}
	if p := c.Read(context.Background()); p.Available || !strings.Contains(p.Reason, "not signed in") {
		t.Fatalf("%+v", p)
	}
}

func TestStoredCredentialFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`","expiresAt":1793000000000,"refreshToken":"r"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := StoredCredential(context.Background())
	if err != nil || cred.Token != secret || !cred.ExpiresAt.Equal(time.UnixMilli(1793000000000)) {
		t.Fatalf("%+v %v", cred, err)
	}
}

func assertNoSecret(t *testing.T, p Plan) {
	t.Helper()
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "SECRET") {
		t.Fatalf("token leaked: %s", b)
	}
}

type countingSource struct{ n int }

func (c *countingSource) Read(context.Context) Plan {
	c.n++
	return Plan{Provider: "x", Windows: []Window{}}
}

func TestServiceCachesAndLimitsRefresh(t *testing.T) {
	src := &countingSource{}
	now := clock
	s := &Service{Sources: []Source{src}, Now: func() time.Time { return now }}
	ctx := context.Background()
	s.Plans(ctx, false)
	s.Plans(ctx, false)
	s.Plans(ctx, true) // within a minute
	if src.n != 1 {
		t.Fatalf("reads %d", src.n)
	}
	now = now.Add(2 * time.Minute)
	s.Plans(ctx, false) // still cached
	if src.n != 1 {
		t.Fatalf("reads %d", src.n)
	}
	s.Plans(ctx, true) // on demand after a minute
	if src.n != 2 {
		t.Fatalf("reads %d", src.n)
	}
	now = now.Add(CacheTTL)
	s.Plans(ctx, false)
	if src.n != 3 {
		t.Fatalf("reads %d", src.n)
	}
}

func TestHandler(t *testing.T) {
	s := &Service{Sources: []Source{&countingSource{}}, Now: func() time.Time { return clock }}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/plan-usage", nil))
	var body struct{ Plans []Plan }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Plans) != 1 || body.Plans[0].Provider != "x" {
		t.Fatalf("%s %v", rec.Body, err)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/plan-usage", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
}
