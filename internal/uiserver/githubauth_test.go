package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/githubauth"
)

type connectionStore struct {
	mu         sync.Mutex
	credential githubauth.Credential
	err        error
	writeError bool
	afterSave  func(context.Context)
}

func (s *connectionStore) Load(context.Context) (githubauth.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return githubauth.Credential{}, s.err
	}
	if s.credential.AccessToken == "" {
		return s.credential, githubauth.ErrNotConnected
	}
	return s.credential, nil
}
func (s *connectionStore) Save(ctx context.Context, c githubauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = c
	if s.afterSave != nil {
		s.afterSave(ctx)
	}
	if s.writeError {
		return errors.New("lost reply")
	}
	return s.err
}
func (s *connectionStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = githubauth.Credential{}
	return s.err
}

type connectionTransport func(*http.Request) (*http.Response, error)

func (f connectionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func connectionFixture(t *testing.T, store *connectionStore, token func(*http.Request) (string, error)) (*GitHubAuthentication, *githubauth.Manager, *Server, *atomic.Int32) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := githubauth.NewWithOptions(store, githubauth.Options{Transport: connectionTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		header := http.Header{}
		switch r.URL.Path {
		case "/login/device/code":
			body = `{"device_code":"SECRET_DEVICE","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`
		case "/login/oauth/access_token":
			var err error
			body, err = token(r)
			if err != nil {
				return nil, err
			}
		case "/user":
			body = `{"login":"alice","id":1}`
			header.Set("X-OAuth-Scopes", "repo, read:org")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	changes := &atomic.Int32{}
	a := NewGitHubAuthentication(ctx, m, config.Config{}, func() { changes.Add(1) })
	s := diagnosticsServer(t, LocalAPI{GitHubAuth: a.Handler()})
	return a, m, s, changes
}
func connectStatus(t *testing.T, a *GitHubAuthentication, state string) GitHubConnection {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := a.snapshot()
		if s.State == state {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state %q never arrived: %+v", state, a.snapshot())
	return GitHubConnection{}
}

const connectionGrant = `{"access_token":"SECRET_ACCESS","refresh_token":"SECRET_REFRESH","expires_in":3600,"refresh_token_expires_in":7200,"token_type":"bearer"}`

func TestGitHubAuthenticationUsesLocalSecurityBoundary(t *testing.T) {
	a, _, s, _ := connectionFixture(t, &connectionStore{}, func(*http.Request) (string, error) { return connectionGrant, nil })
	connectStatus(t, a, "disconnected")
	for _, path := range []string{"/api/github-auth/start", "/api/github-auth/cancel", "/api/github-auth/disconnect", "/api/github-auth/retry"} {
		for _, origin := range []string{"", "https://attacker.example"} {
			w := request(t, s, "POST", path, "", origin, "application/json", "Bearer "+s.launch, "{}")
			if w.Code != 403 {
				t.Fatalf("%s origin %q: %d", path, origin, w.Code)
			}
		}
	}
	w := request(t, s, "POST", "/api/github-auth/start", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SECRET_") {
		t.Fatal("secret in start response")
	}
	var start GitHubConnection
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil || start.Pending == nil {
		t.Fatal(err, w.Body.String())
	}
	status := connectStatus(t, a, "connected")
	if !status.Saved || status.Login != "alice" || status.Source != config.SourceBaum {
		t.Fatal(status)
	}
	w = request(t, s, "GET", "/api/github-auth", "", "", "", "Bearer "+s.launch, "")
	if strings.Contains(w.Body.String(), "SECRET_") {
		t.Fatal("secret in status")
	}
}

func TestCancelStopsInFlightDevicePoll(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	a, _, s, _ := connectionFixture(t, &connectionStore{}, func(r *http.Request) (string, error) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		return "", r.Context().Err()
	})
	connectStatus(t, a, "disconnected")
	request(t, s, "POST", "/api/github-auth/start", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request(t, s, "POST", "/api/github-auth/cancel", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel waited for network instead of cancelling it")
	}
	if a.snapshot().Pending != nil {
		t.Fatal("pending flow survived cancellation")
	}
}

func TestGitHubAuthenticationOverrideAndHealthRecovery(t *testing.T) {
	a, m, s, _ := connectionFixture(t, &connectionStore{credential: githubauth.Credential{AccessToken: "saved", Login: "alice", ID: 1}}, func(*http.Request) (string, error) { return connectionGrant, nil })
	connectStatus(t, a, "connected")
	token, err := m.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.ObserveResponse(token, 403, http.Header{"X-Github-Sso": []string{"required;url=private"}})
	if a.snapshot().Error != "sso_required" {
		t.Fatal(a.snapshot())
	}
	m.ObserveResponse(token, 200, http.Header{})
	if a.snapshot().Error != "" {
		t.Fatal("stale health error", a.snapshot())
	}
	t.Setenv("GITHUB_TOKEN", "override")
	w := request(t, s, "POST", "/api/github-auth/disconnect", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	state := a.snapshot()
	if state.Saved || !state.Override || state.Source != "environment variable GITHUB_TOKEN" {
		t.Fatal(state)
	}
}

func TestAmbiguousCredentialSaveStillInvalidatesIdentity(t *testing.T) {
	a, _, s, changes := connectionFixture(t, &connectionStore{writeError: true}, func(*http.Request) (string, error) { return connectionGrant, nil })
	connectStatus(t, a, "disconnected")
	request(t, s, "POST", "/api/github-auth/start", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	deadline := time.Now().Add(4 * time.Second)
	for changes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if changes.Load() == 0 {
		t.Fatal("ambiguous persisted write did not invalidate identity")
	}
	if a.snapshot().Error != "credential_store_unavailable" {
		t.Fatal(a.snapshot())
	}
}

func TestCancellationAfterPersistenceStillPublishesAccountChange(t *testing.T) {
	persisted := make(chan struct{})
	store := &connectionStore{afterSave: func(ctx context.Context) { close(persisted); <-ctx.Done() }}
	a, _, s, changes := connectionFixture(t, store, func(*http.Request) (string, error) { return connectionGrant, nil })
	connectStatus(t, a, "disconnected")
	request(t, s, "POST", "/api/github-auth/start", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	select {
	case <-persisted:
	case <-time.After(3 * time.Second):
		t.Fatal("grant not persisted")
	}
	w := request(t, s, "POST", "/api/github-auth/cancel", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	status := a.snapshot()
	if !status.Saved || status.Login != "alice" || changes.Load() == 0 {
		t.Fatalf("committed account change lost to cancellation: %+v changes %d", status, changes.Load())
	}
}

func TestCredentialStoreCanRecoverWithoutNewAuthorization(t *testing.T) {
	store := &connectionStore{credential: githubauth.Credential{AccessToken: "saved", Login: "alice", ID: 1}, err: errors.New("locked")}
	a, manager, s, _ := connectionFixture(t, store, func(*http.Request) (string, error) { t.Fatal("unnecessary OAuth exchange"); return "", nil })
	status := connectStatus(t, a, "disconnected")
	if status.Error != "credential_store_unavailable" {
		t.Fatal(status)
	}
	if _, err := manager.Token(context.Background()); err != githubauth.ErrStore {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	w := request(t, s, "POST", "/api/github-auth/retry", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	status = a.snapshot()
	if !status.Saved || status.Login != "alice" || status.Error != "" {
		t.Fatal(status)
	}
}
