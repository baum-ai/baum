package apiclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStartDeviceAuthorization(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/device" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"device_code": "dc", "user_code": "WDJB-MJHT",
			"verification_uri": "http://ui/device",
			"verification_uri_complete": "http://ui/device?user_code=WDJB-MJHT",
			"expires_in": 600, "interval": 5
		}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/"}
	start, err := c.StartDeviceAuthorization(context.Background(), "my-laptop")
	if err != nil {
		t.Fatalf("StartDeviceAuthorization: %v", err)
	}
	if gotBody != `{"name":"my-laptop"}` {
		t.Errorf("request body = %q", gotBody)
	}
	if start.DeviceCode != "dc" || start.UserCode != "WDJB-MJHT" || start.Interval != 5 {
		t.Errorf("start = %+v", start)
	}
}

func TestPollDeviceTokenErrorMapping(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusBadRequest, `{"error":"authorization_pending"}`, ErrAuthorizationPending},
		{http.StatusTooManyRequests, `{"error":"slow_down"}`, ErrSlowDown},
		{http.StatusBadRequest, `{"error":"expired_token"}`, ErrExpiredToken},
		{http.StatusForbidden, `{"error":"access_denied"}`, ErrAccessDenied},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		}))
		c := &Client{BaseURL: srv.URL}
		_, err := c.PollDeviceToken(context.Background(), "dc")
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.body, err, tt.want)
		}
		srv.Close()
	}
}

func TestMeSendsBearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer rcnt_tok" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-1","github_login":"octocat"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "rcnt_tok"}
	user, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if user.GitHubLogin != "octocat" {
		t.Errorf("user = %+v", user)
	}
}

func TestListGitHubInstallationsFlattensRepositoriesAndSkipsArchived(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/github/installations" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"installations":[
			{"repositories":[{"id":"repo-1","full_name":"acme/one","archived":false},{"id":"old","full_name":"acme/old","archived":true}]},
			{"repositories":[{"id":"repo-2","full_name":"acme/two","archived":false}]}
		]}`))
	}))
	defer srv.Close()

	repositories, err := (&Client{BaseURL: srv.URL, Token: "rcnt_tok"}).ListGitHubInstallations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0].FullName != "acme/one" || repositories[1].ID != "repo-2" {
		t.Fatalf("repositories = %+v", repositories)
	}
}

func TestUnauthenticatedMapsTo401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthenticated"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "rcnt_revoked"}
	if _, err := c.Me(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestGateRefusalOfABearerTokenMapsToUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(GateRequiredHeader, GateRequiredHeaderValue)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"gate required"}`))
	}))
	defer srv.Close()

	// A valid token always passes the gate, so a refusal of a request that
	// carried one means the token is no good.
	c := &Client{BaseURL: srv.URL, Token: "rcnt_stale"}
	if _, err := c.Me(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Me: err = %v, want ErrUnauthenticated", err)
	}
	if err := c.UploadRepositoryTraces(context.Background(), "repo-1", strings.NewReader("x"), 1); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("UploadRepositoryTraces: err = %v, want ErrUnauthenticated", err)
	}

	// Without a token there was nothing to refuse, so the gate answer stands.
	anonymous := &Client{BaseURL: srv.URL}
	_, err := anonymous.StartDeviceAuthorization(context.Background(), "device")
	if err == nil || errors.Is(err, ErrUnauthenticated) || err.Error() != "server returned 403 Forbidden: gate required" {
		t.Errorf("anonymous: err = %v", err)
	}
}

func TestPlainForbiddenIsNotUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "rcnt_reader"}
	err := c.UploadRepositoryTraces(context.Background(), "repo-1", strings.NewReader("x"), 1)
	if err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want a plain forbidden error", err)
	}
}

func TestRevokeCurrentToken(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "rcnt_tok"}
	if err := c.RevokeCurrentToken(context.Background()); err != nil {
		t.Fatalf("RevokeCurrentToken: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/tokens/current" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
}

func TestFindGitHubRepository(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/github/repositories/find" || r.URL.Query().Get("full_name") != "acme/demo" {
			t.Errorf("URL = %q", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"id":"repo-1","full_name":"acme/demo"}`))
	}))
	defer srv.Close()
	repository, err := (&Client{BaseURL: srv.URL, Token: "rcnt_tok"}).FindGitHubRepository(context.Background(), "acme/demo")
	if err != nil || repository.ID != "repo-1" || repository.FullName != "acme/demo" {
		t.Fatalf("FindGitHubRepository = %+v, %v", repository, err)
	}
}

func TestRequestGitHubRepositorySync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/github/repositories/repo-1/sync" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer rcnt_tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Token: "rcnt_tok"}
	if err := client.RequestGitHubRepositorySync(context.Background(), "repo-1"); err != nil {
		t.Fatalf("RequestGitHubRepositorySync: %v", err)
	}
}

func TestAuthenticatedMethodsRequireTokenWithoutSendingRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	client := &Client{BaseURL: srv.URL}

	_, meErr := client.Me(context.Background())
	_, findErr := client.FindGitHubRepository(context.Background(), "acme/demo")
	syncErr := client.RequestGitHubRepositorySync(context.Background(), "repo-1")
	uploadErr := client.UploadRepositoryTraces(context.Background(), "repo-1", bytes.NewReader(nil), 0)
	revokeErr := client.RevokeCurrentToken(context.Background())
	for name, err := range map[string]error{
		"me": meErr, "find": findErr, "sync": syncErr, "upload": uploadErr, "revoke": revokeErr,
	} {
		if !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s error = %v, want ErrUnauthenticated", name, err)
		}
	}
	if requests != 0 {
		t.Errorf("sent %d requests without a token", requests)
	}
}

func TestUploadRepositoryTraces(t *testing.T) {
	payload := []byte("archive bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/github/repositories/repo-1/trace-uploads" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer rcnt_tok" || r.Header.Get("Content-Type") != "application/gzip" {
			t.Errorf("headers = %+v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, payload) || r.ContentLength != int64(len(payload)) {
			t.Errorf("body=%q length=%d", body, r.ContentLength)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "rcnt_tok"}
	if err := client.UploadRepositoryTraces(context.Background(), "repo-1", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("UploadRepositoryTraces: %v", err)
	}
}

func TestUnknownErrorIncludesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing token name"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	_, err := c.StartDeviceAuthorization(context.Background(), "")
	if err == nil || err.Error() != "server returned 400 Bad Request: missing token name" {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultHTTPClientTimesOutOnAServerThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	original := defaultHTTPClient
	defaultHTTPClient = newHTTPClient(50 * time.Millisecond)
	t.Cleanup(func() { defaultHTTPClient = original })

	c := &Client{BaseURL: srv.URL, Token: "t"}
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("Me succeeded against a server that never answers")
	}
}
