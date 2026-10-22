package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	c     Credential
	err   error
	saves int
}

func (s *memoryStore) Load(context.Context) (Credential, error) {
	if s.err != nil {
		return Credential{}, s.err
	}
	if s.c.AccessToken == "" {
		return Credential{}, ErrNotConnected
	}
	return s.c, nil
}
func (s *memoryStore) Save(_ context.Context, c Credential) error {
	if s.err != nil {
		return s.err
	}
	s.c = c
	s.saves++
	return nil
}
func (s *memoryStore) Delete(context.Context) error { s.c = Credential{}; return s.err }
func fixture(t *testing.T) (*Manager, *memoryStore, *time.Time, *string, *int) {
	t.Helper()
	now := time.Unix(10000, 0)
	reply := `{"error":"authorization_pending"}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if r.Form.Get("scope") != Scopes {
				t.Error("missing scopes")
			}
			if _, err := fmt.Fprint(w, `{"device_code":"secret-device","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`); err != nil {
				t.Error(err)
			}
		case "/login/oauth/access_token":
			calls++
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if r.Form.Get("client_secret") != "" {
				t.Error("client secret sent")
			}
			if _, err := fmt.Fprint(w, reply); err != nil {
				t.Error(err)
			}
		case "/user":
			if r.Header.Get("Authorization") == "" {
				t.Error("missing token")
			}
			w.Header().Set("X-OAuth-Scopes", "read:org, repo")
			if _, err := fmt.Fprint(w, `{"login":"octocat","id":1}`); err != nil {
				t.Error(err)
			}
		default:
			t.Error("unexpected URL")
		}
	}))
	t.Cleanup(srv.Close)
	s := &memoryStore{}
	m := New(s)
	m.oauth = srv.URL
	m.api = srv.URL
	m.now = func() time.Time { return now }
	return m, s, &now, &reply, &calls
}
func TestDeviceLifecycle(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	ctx := context.Background()
	d, err := m.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "secret-device") {
		t.Fatal("device secret exposed")
	}
	st, err := m.Poll(ctx, d.ID)
	if err != nil || st.State != "pending" || *calls != 0 {
		t.Fatal(st, err)
	}
	*now = now.Add(5 * time.Second)
	*reply = `{"error":"slow_down"}`
	if status, err := m.Poll(ctx, d.ID); err != nil || status.State != "pending" {
		t.Fatal(status, err)
	}
	*now = now.Add(5 * time.Second)
	if status, err := m.Poll(ctx, d.ID); err != nil || status.State != "pending" {
		t.Fatal(status, err)
	}
	if *calls != 1 {
		t.Fatal("ignored slow_down")
	}
	*now = now.Add(5 * time.Second)
	*reply = `{"access_token":"secret-access","token_type":"bearer","expires_in":3600,"refresh_token":"secret-refresh","refresh_token_expires_in":7200}`
	st, err = m.Poll(ctx, d.ID)
	if err != nil || st.State != "connected" || s.saves != 1 {
		t.Fatal(st, err)
	}
	b, _ = json.Marshal(st)
	if strings.Contains(string(b), "secret") {
		t.Fatal("token exposed")
	}
	if token, err := m.Token(ctx); err != nil || token != "secret-access" {
		t.Fatal(err)
	}
	if err = m.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Token(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatal(err)
	}
}
func TestTerminalDeviceStates(t *testing.T) {
	for _, state := range []string{"denied", "expired", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			m, _, now, reply, _ := fixture(t)
			d, _ := m.Start(context.Background())
			*now = now.Add(5 * time.Second)
			switch state {
			case "denied":
				*reply = `{"error":"access_denied"}`
			case "expired":
				*now = now.Add(time.Hour)
			case "cancelled":
				if err := m.Cancel(context.Background(), d.ID); err != nil {
					t.Fatal(err)
				}
			}
			s, err := m.Poll(context.Background(), d.ID)
			if err != nil || s.State != state {
				t.Fatal(s, err)
			}
		})
	}
}
func TestRefreshSerializedAndPersisted(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: now.Unix(), ID: 1, Login: "octocat"}
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"new-refresh","refresh_token_expires_in":7200}`
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			token, err := m.Token(context.Background())
			if err != nil || token != "new" {
				t.Error(token, err)
			}
		})
	}
	wg.Wait()
	if *calls != 1 || s.saves != 1 || s.c.RefreshToken != "new-refresh" {
		t.Fatal("refresh not serialized/persisted")
	}
}
func TestRefreshRejectsFailure(t *testing.T) {
	m, s, now, reply, _ := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: now.Unix(), ID: 2}
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"new-refresh","refresh_token_expires_in":7200}`
	token, err := m.Token(context.Background())
	if token != "" || !errors.Is(err, ErrReconnect) || s.saves != 0 {
		t.Fatal(token, err)
	}
	*reply = `{"error":"bad_refresh_token","error_description":"secret"}`
	token, err = m.Token(context.Background())
	if token != "" || !errors.Is(err, ErrReconnect) {
		t.Fatal(token, err)
	}
}
func TestRequestDoesNotFollowRedirect(t *testing.T) {
	m, _, _, _, _ := fixture(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redirect.Close()
	m.oauth = redirect.URL
	if _, err := m.Start(context.Background()); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
}

func TestPersistenceFailureDoesNotReturnToken(t *testing.T) {
	m, s, now, reply, _ := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: now.Unix(), ID: 1}
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"new-refresh","refresh_token_expires_in":7200}`
	// Fail only writes: a rotated credential must not escape before persistence.
	m.store = writeFailure{s}
	token, err := m.Token(context.Background())
	if token != "" || !errors.Is(err, ErrStore) {
		t.Fatal(token, err)
	}
}

type writeFailure struct{ *memoryStore }

func (writeFailure) Save(context.Context, Credential) error {
	return errors.New("secret upstream store detail")
}
func TestScopeAndMalformedTokenRejected(t *testing.T) {
	for _, reply := range []string{`{"access_token":"secret","token_type":"bearer","expires_in":9223372036854775807}`, `{"access_token":"secret","token_type":"unknown"}`, `{"access_token":"secret","token_type":"bearer","expires_in":1}`} {
		m, _, _, _, _ := fixture(t)
		var r tokenResponse
		if err := json.Unmarshal([]byte(reply), &r); err != nil {
			t.Fatal(err)
		}
		if _, err := m.validate(context.Background(), r); !errors.Is(err, ErrRequest) {
			t.Fatal(err)
		}
	}
	m, _, _, _, _ := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo")
		if _, err := fmt.Fprint(w, `{"login":"octocat","id":1}`); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m.api = srv.URL
	if _, err := m.validate(context.Background(), tokenResponse{Access: "token", TokenType: "bearer", Refresh: "refresh", Expires: 3600, RefreshExpires: 7200}); !errors.Is(err, ErrScopes) {
		t.Fatal(err)
	}
}
func TestSafeUpstreamErrors(t *testing.T) {
	m, _, _, _, _ := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		if _, err := fmt.Fprint(w, "secret upstream detail"); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m.oauth = srv.URL
	if _, err := m.Start(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestRotatedTokenRetriedWithoutSecondExchange(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "spent", ExpiresAt: now.Unix(), ID: 1}
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"rotated","refresh_token_expires_in":7200}`
	m.store = writeFailure{s}
	if token, err := m.Token(context.Background()); token != "" || !errors.Is(err, ErrStore) {
		t.Fatal(token, err)
	}
	receipt := now.Unix()
	*now = now.Add(20 * time.Second)
	m.store = s
	*reply = `{"error":"bad_refresh_token"}`
	token, err := m.Token(context.Background())
	if err != nil || token != "new" || *calls != 1 || s.c.RefreshToken != "rotated" || s.c.ExpiresAt != receipt+3600 {
		t.Fatal(token, err, *calls)
	}
}
func TestCancelledLockAcquisition(t *testing.T) {
	m, _, _, _, _ := fixture(t)
	m.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-m.gate
	if _, err := m.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestPendingGrantRetriesPersistence(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	d, _ := m.Start(context.Background())
	*now = now.Add(5 * time.Second)
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"rotated","refresh_token_expires_in":7200}`
	m.store = writeFailure{s}
	if _, err := m.Poll(context.Background(), d.ID); !errors.Is(err, ErrStore) {
		t.Fatal(err)
	}
	receipt := now.Unix()
	*now = now.Add(20 * time.Second)
	m.store = s
	status, err := m.Poll(context.Background(), d.ID)
	if err != nil || status.State != "connected" || *calls != 1 || s.c.ExpiresAt != receipt+3600 {
		t.Fatal(status, err, *calls)
	}
}

func TestRetainedRotationRefreshUsesLatestToken(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "spent", ExpiresAt: now.Unix(), ID: 1}
	*reply = `{"access_token":"first","token_type":"bearer","expires_in":3600,"refresh_token":"current","refresh_token_expires_in":7200}`
	m.store = writeFailure{s}
	if token, err := m.Token(context.Background()); token != "" || !errors.Is(err, ErrStore) {
		t.Fatal(token, err)
	}
	receipt := now.Unix()
	*now = now.Add(2 * time.Hour)
	if _, err := m.Token(context.Background()); !errors.Is(err, ErrReconnect) {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatal("expired refresh token was sent")
	}
	*now = time.Unix(receipt+3550, 0)
	m.store = s
	*reply = `{"access_token":"second","token_type":"bearer","expires_in":3600,"refresh_token":"latest","refresh_token_expires_in":7200}`
	token, err := m.Token(context.Background())
	if err != nil || token != "second" || *calls != 2 || s.c.ExpiresAt != now.Unix()+3600 {
		t.Fatal(token, err, *calls, s.c.ExpiresAt)
	}
}
