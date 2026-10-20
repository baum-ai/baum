//go:build !windows

package baumcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newFakeServer serves the auth endpoints the CLI uses. The device-token
// endpoint answers authorization_pending (pendingPolls) times before issuing
// the token, with interval 0 so tests do not sleep.
func newFakeServer(t *testing.T, pendingPolls int) (*httptest.Server, *fakeServerState) {
	t.Helper()
	state := &fakeServerState{pendingPolls: pendingPolls}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /auth/device", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		state.tokenName = req.Name
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code":               "the-device-code",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          "http://ui.example/device",
			"verification_uri_complete": "http://ui.example/device?user_code=WDJB-MJHT",
			"expires_in":                600,
			"interval":                  0,
		})
	})
	mux.HandleFunc("POST /auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		state.polls++
		if state.polls <= state.pendingPolls {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      "rcnt_issued",
			"expires_at": time.Now().Add(365 * 24 * time.Hour),
			"user":       map[string]any{"id": "user-1", "github_login": "octocat"},
		})
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rcnt_issued" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		name := "Mona"
		writeJSON(w, http.StatusOK, map[string]any{"id": "user-1", "github_login": "octocat", "name": name})
	})
	mux.HandleFunc("DELETE /tokens/current", func(w http.ResponseWriter, r *http.Request) {
		state.revoked = append(state.revoked, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, state
}

type fakeServerState struct {
	pendingPolls int
	polls        int
	tokenName    string
	revoked      []string
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	return runCLIWithInput(t, "yes\n", args...)
}

func runCLIWithInput(t *testing.T, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	originalPollAfter := pollAfter
	pollAfter = func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- time.Now()
		return ready
	}
	defer func() { pollAfter = originalPollAfter }()

	var out, errOut strings.Builder
	err = runWithInput(args, strings.NewReader(input), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestLoginEndToEnd(t *testing.T) {
	setTempConfigHome(t)
	srv, state := newFakeServer(t, 2)

	stdout, _, err := runCLI(t, "login", "--server", srv.URL, "--name", "test-laptop")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(stdout, "WDJB-MJHT") || !strings.Contains(stdout, "http://ui.example/device?user_code=WDJB-MJHT") {
		t.Errorf("stdout missing code or URL:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Logged in as octocat") {
		t.Errorf("stdout missing login confirmation:\n%s", stdout)
	}
	if state.tokenName != "test-laptop" || state.polls != 3 {
		t.Errorf("server saw name=%q polls=%d", state.tokenName, state.polls)
	}

	creds, ok, err := loadCredentials()
	if err != nil || !ok {
		t.Fatalf("credentials not saved: ok=%v err=%v", ok, err)
	}
	if creds.Token != "rcnt_issued" || creds.ServerURL != srv.URL {
		t.Errorf("credentials = %+v", creds)
	}
}

func TestLoginDenied(t *testing.T) {
	setTempConfigHome(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"device_code": "dc", "user_code": "X", "interval": 0, "expires_in": 600})
	})
	mux.HandleFunc("POST /auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access_denied"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, _, err := runCLI(t, "login", "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %v, want denial", err)
	}
	if _, ok, _ := loadCredentials(); ok {
		t.Error("credentials saved despite denial")
	}
}

func TestWhoami(t *testing.T) {
	setTempConfigHome(t)
	srv, _ := newFakeServer(t, 0)

	// Soon-expiring token from the credentials file triggers the warning.
	if err := saveCredentials(credentials{
		ServerURL: srv.URL,
		Token:     "rcnt_issued",
		ExpiresAt: time.Now().Add(10 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	stdout, stderr, err := runCLI(t, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if want := "octocat (Mona)\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "token expires in 9 days") {
		t.Errorf("stderr missing expiry warning: %q", stderr)
	}
}

func TestWhoamiNotLoggedIn(t *testing.T) {
	setTempConfigHome(t)
	if _, _, err := runCLI(t, "whoami"); err == nil || !strings.Contains(err.Error(), "baum login") {
		t.Errorf("err = %v, want login hint", err)
	}
}

func TestWhoamiRevokedToken(t *testing.T) {
	setTempConfigHome(t)
	srv, _ := newFakeServer(t, 0)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_wrong"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, _, err := runCLI(t, "whoami"); err == nil || !strings.Contains(err.Error(), "baum login") {
		t.Errorf("err = %v, want login hint", err)
	}
}

func TestLogout(t *testing.T) {
	setTempConfigHome(t)
	srv, state := newFakeServer(t, 0)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_issued"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	stdout, _, err := runCLI(t, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(stdout, "Logged out") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(state.revoked) != 1 || state.revoked[0] != "Bearer rcnt_issued" {
		t.Errorf("revoked = %v", state.revoked)
	}
	if _, ok, _ := loadCredentials(); ok {
		t.Error("credentials still present after logout")
	}

	stdout, _, err = runCLI(t, "logout")
	if err != nil || !strings.Contains(stdout, "Not logged in") {
		t.Errorf("second logout: stdout = %q, err = %v", stdout, err)
	}
}

func TestLogoutWithBaumTokenRevokesOnlyStoredToken(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_shared")
	srv, state := newFakeServer(t, 0)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_issued"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, stderr, err := runCLI(t, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(state.revoked) != 1 || state.revoked[0] != "Bearer rcnt_issued" {
		t.Errorf("revoked = %v, want only the stored token", state.revoked)
	}
	if !strings.Contains(stderr, "BAUM_TOKEN is set; it was not revoked") {
		t.Errorf("stderr = %q, want BAUM_TOKEN warning", stderr)
	}
}

func TestLogoutWithOnlyBaumTokenRevokesNothing(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_shared")
	srv, state := newFakeServer(t, 0)
	t.Setenv(serverEnvVar, srv.URL)

	stdout, stderr, err := runCLI(t, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(state.revoked) != 0 {
		t.Errorf("revoked = %v, want nothing", state.revoked)
	}
	if !strings.Contains(stdout, "Not logged in") || !strings.Contains(stderr, "BAUM_TOKEN is set") {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestLogoutRemovesCredentialsWhenServerUnreachable(t *testing.T) {
	setTempConfigHome(t)
	if err := saveCredentials(credentials{ServerURL: "http://127.0.0.1:1", Token: "rcnt_issued"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	stdout, stderr, err := runCLI(t, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(stdout, "Logged out") || !strings.Contains(stderr, "warning: revoking token on server failed") {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
	if _, ok, _ := loadCredentials(); ok {
		t.Error("credentials still present")
	}
}

func TestLoginWarnsWhenReplacingCredentials(t *testing.T) {
	setTempConfigHome(t)
	srv, _ := newFakeServer(t, 0)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_old"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, stderr, err := runCLI(t, "login", "--server", srv.URL)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(stderr, "replacing existing credentials") {
		t.Errorf("stderr = %q, want replacement warning", stderr)
	}
	creds, _, _ := loadCredentials()
	if creds.Token != "rcnt_issued" {
		t.Errorf("token = %q, want fresh token", creds.Token)
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	_, stderr, err := runCLI(t, "frobnicate")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr = %q, want usage", stderr)
	}
}
