package uiserver_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/uiserver"
)

// Only external boundaries are synthetic: Manager and the controller own the
// real device lifecycle, polling, validation, and persistence failures.
type browserCredentialStore struct {
	mu         sync.Mutex
	credential githubauth.Credential
	mode       func() string
}

func (s *browserCredentialStore) Load(context.Context) (githubauth.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode() == "locked" {
		return githubauth.Credential{}, githubauth.ErrStore
	}
	if s.credential.AccessToken == "" {
		return githubauth.Credential{}, githubauth.ErrNotConnected
	}
	return s.credential, nil
}
func (s *browserCredentialStore) Save(_ context.Context, c githubauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode() == "locked" {
		return githubauth.ErrStore
	}
	s.credential = c
	return nil
}
func (s *browserCredentialStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode() == "locked" {
		return githubauth.ErrStore
	}
	s.credential = githubauth.Credential{}
	return nil
}
func browserAuthentication(t *testing.T, ctx context.Context, state string, cfg config.Config, changed func(), begin func() func(bool)) *uiserver.GitHubAuthentication {
	marker := filepath.Join(state, "github-auth-mode")
	_ = os.Remove(marker)
	mode := func() string { b, _ := os.ReadFile(marker); return strings.TrimSpace(string(b)) }
	store := &browserCredentialStore{mode: mode, credential: githubauth.Credential{AccessToken: "fixture-access-initial", RefreshToken: "fixture-refresh", Login: "initial", ID: 3, ExpiresAt: time.Now().Add(time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(2 * time.Hour).Unix()}}
	var attempt atomic.Int64
	manager := githubauth.NewWithOptions(store, githubauth.Options{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		headers := http.Header{}
		switch r.URL.Path {
		case "/login/device/code":
			body = fmt.Sprintf(`{"device_code":"fixture-device-%d","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`, attempt.Add(1))
		case "/login/oauth/access_token":
			switch mode() {
			case "network":
				return nil, fmt.Errorf("synthetic network outage")
			case "denied":
				body = `{"error":"access_denied"}`
			case "expired":
				body = `{"error":"expired_token"}`
			case "alice", "bob", "scopes", "locked":
				body = fmt.Sprintf(`{"access_token":"fixture-access-%s","refresh_token":"fixture-refresh","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":7200}`, mode())
			default:
				body = `{"error":"authorization_pending"}`
			}
		case "/user":
			headers.Set("X-OAuth-Scopes", "repo, read:org")
			if mode() == "scopes" {
				headers.Set("X-OAuth-Scopes", "read:org")
			}
			if strings.Contains(r.Header.Get("Authorization"), "bob") {
				body = `{"id":2,"login":"bob"}`
			} else {
				body = `{"id":1,"login":"alice"}`
			}
		default:
			return nil, fmt.Errorf("unexpected GitHub auth request: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	previous := config.GitHubAuth
	config.GitHubAuth = manager
	t.Cleanup(func() { config.GitHubAuth = previous })
	return uiserver.NewGitHubAuthentication(ctx, manager, cfg, changed, begin)
}
