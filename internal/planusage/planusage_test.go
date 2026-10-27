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

// codexRead reads the session files only: the home has no auth.json.
func codexRead(home string) Plan {
	return (&Codex{Home: home, Now: func() time.Time { return clock }}).Read(context.Background())
}

const codexSecret = "codex-SECRET-TOKEN"

func codexAPIRead(t *testing.T, home, url string, cred CodexCredential) Plan {
	t.Helper()
	c := &Codex{Home: home, URL: url, Now: func() time.Time { return clock },
		Credential: func(context.Context) (CodexCredential, error) { return cred, nil }}
	p := c.Read(context.Background())
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), codexSecret) {
		t.Fatalf("token leaked: %s", b)
	}
	return p
}

func TestCodexAPIGivesLiveFigures(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = w.Write([]byte(`{"plan_type":"prolite","rate_limit":{"allowed":true,
			"primary_window":{"used_percent":11,"limit_window_seconds":604800,"reset_after_seconds":492726,"reset_at":1793616165},
			"secondary_window":null},"credits":{"balance":"0"}}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	writeRollout(t, home, "2026/10/25", "rollout-2026-10-25T09-00-00-a.jsonl",
		`{"timestamp":"2026-10-25T09:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":99,"window_minutes":10080,"resets_at":1793616165}}}}`)
	p := codexAPIRead(t, home, srv.URL, CodexCredential{Token: codexSecret, AccountID: "acct", ExpiresAt: clock.Add(time.Hour)})
	if !p.Available || p.Plan != "Pro 100" || len(p.Windows) != 1 || p.Windows[0].Name != "weekly" || *p.Windows[0].UsedPercent != 11 {
		t.Fatalf("%+v", p)
	}
	if !p.FetchedAt.Equal(clock) || !p.Windows[0].ResetsAt.Equal(time.Unix(1793616165, 0)) {
		t.Fatalf("%+v %v", p, p.Windows[0].ResetsAt)
	}
	if seen.Get("Authorization") != "Bearer "+codexSecret || seen.Get("ChatGPT-Account-Id") != "acct" || seen.Get("User-Agent") != "baum" {
		t.Fatalf("headers %v", seen)
	}
}

func TestCodexFallsBackToSessionsWhenTheAPICannotAnswer(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-2026-10-26T10-00-00-a.jsonl",
		`{"timestamp":"2026-10-26T10:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":40,"window_minutes":300,"resets_at":1793100000},"plan_type":"plus"}}}`)
	calls := 0
	for name, c := range map[string]struct {
		status int
		body   string
		cred   CodexCredential
	}{
		"refused":    {401, `{}`, CodexCredential{Token: codexSecret, AccountID: "acct"}},
		"unexpected": {200, `{"rate_limit":null}`, CodexCredential{Token: codexSecret, AccountID: "acct"}},
		"no windows": {200, `{"rate_limit":{"primary_window":null}}`, CodexCredential{Token: codexSecret, AccountID: "acct"}},
		"expired":    {200, `{}`, CodexCredential{Token: codexSecret, AccountID: "acct", ExpiresAt: clock.Add(-time.Minute)}},
		"no account": {200, `{}`, CodexCredential{Token: codexSecret}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			p := codexAPIRead(t, home, srv.URL, c.cred)
			if !p.Available || p.Plan != "Plus" || *p.Windows[0].UsedPercent != 40 {
				t.Fatalf("%+v", p)
			}
		})
	}
	// An expired token or a sign-in without an account is never sent.
	if calls != 3 {
		t.Fatalf("requests %d", calls)
	}
}

func TestStoredCodexCredential(t *testing.T) {
	home := t.TempDir()
	if _, err := StoredCodexCredential(home); err == nil {
		t.Fatal("no auth.json is no credential")
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x","tokens":null}`)
	if _, err := StoredCodexCredential(home); err == nil {
		t.Fatal("an API key sign-in has no plan")
	}
	// {"exp":1793000000} as a JWT payload.
	write(`{"auth_mode":"chatgpt","tokens":{"access_token":"h.eyJleHAiOjE3OTMwMDAwMDB9.s","account_id":"acct","refresh_token":"r"}}`)
	cred, err := StoredCodexCredential(home)
	if err != nil || cred.AccountID != "acct" || !cred.ExpiresAt.Equal(time.Unix(1793000000, 0)) {
		t.Fatalf("%+v %v", cred, err)
	}
	if !jwtExpiry("not-a-jwt").IsZero() || !jwtExpiry("a.!!.c").IsZero() {
		t.Fatal("an unreadable token has no expiry")
	}
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
	if !p.Available || p.Plan != "Pro 200" || len(p.Windows) != 2 {
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
	p := claudeRead(srv.URL, Credential{Token: secret, ExpiresAt: clock.Add(time.Hour), Plan: "Max 5x"})
	if !p.Available || p.Plan != "Max 5x" || len(p.Windows) != 2 || p.Windows[0].Name != "5-hour" || *p.Windows[0].UsedPercent != 62.5 || p.Windows[1].Name != "weekly" {
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
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`","expiresAt":1793000000000,"refreshToken":"r","subscriptionType":"team","rateLimitTier":"default_claude_max_5x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := StoredCredential(context.Background())
	if err != nil || cred.Token != secret || !cred.ExpiresAt.Equal(time.UnixMilli(1793000000000)) || cred.Plan != "Team Premium" {
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

func TestCodexPlanNamesAreCodexs(t *testing.T) {
	for planType, want := range map[string]string{
		"prolite": "Pro 100", "pro": "Pro 200", "promax": "Pro 500",
		"plus": "Plus", "edu_pro": "Edu Pro", "some_new_plan": "some_new_plan", "": "",
	} {
		if got := codexPlanName(planType); got != want {
			t.Errorf("codexPlanName(%q) = %q, want %q", planType, got, want)
		}
	}
}

func TestClaudePlanNamesAreAnthropics(t *testing.T) {
	for _, c := range []struct{ subscription, tier, want string }{
		{"pro", "default_claude_ai", "Pro"},
		{"max", "default_claude_max_5x", "Max 5x"},
		{"max", "default_claude_max_20x", "Max 20x"},
		{"max", "", "Max"},
		{"team", "default_claude_max_5x", "Team Premium"},
		{"team", "default_claude_ai", "Team Standard"},
		{"team", "", "Team"},
		{"enterprise", "default_claude_max_5x", "Enterprise"},
		{"something_new", "x", "something_new"},
		{"", "", ""},
	} {
		if got := claudePlanName(c.subscription, c.tier); got != c.want {
			t.Errorf("claudePlanName(%q, %q) = %q, want %q", c.subscription, c.tier, got, c.want)
		}
	}
}
