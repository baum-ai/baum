package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/githubauth"
)

type runtimeTransport func(*http.Request) (*http.Response, error)

func (f runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCachedGraphQLClientResolvesEveryRequest(t *testing.T) {
	token := "first"
	var headers []string
	transport := runtimeTransport(func(r *http.Request) (*http.Response, error) {
		headers = append(headers, r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{}}`)), Request: r}, nil
	})
	c := New(func(context.Context, string) (string, error) { return token, nil }, Options{Transport: transport})
	client, err := c.graphQL(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	var query struct{ Viewer struct{ Login string } }
	if err = client.Query(context.Background(), &query, nil); err != nil {
		t.Fatal(err)
	}
	token = "second"
	cached, err := c.graphQL(context.Background(), "github.com")
	if err != nil || cached != client {
		t.Fatal("client not cached", err)
	}
	if err = cached.Query(context.Background(), &query, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(headers, ",") != "token first,token second" {
		t.Fatal(headers)
	}
}

func TestRESTReaderRefusesChangedPartition(t *testing.T) {
	token := "first"
	calls := 0
	c := New(func(context.Context, string) (string, error) { return token, nil }, Options{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})})
	reader, err := c.REST(context.Background(), Repository{Host: "github.com", Owner: "org", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	token = "second"
	if _, _, _, err = reader.Read(context.Background(), "/user", ""); err == nil || calls != 0 {
		t.Fatalf("old reader sent new token: %v calls %d", err, calls)
	}
	newReader, err := c.REST(context.Background(), Repository{Host: "github.com", Owner: "org", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if reader.CacheIdentity() == newReader.CacheIdentity() {
		t.Fatal("identity partition reused")
	}
}

type hostHealthStore struct{}

func (hostHealthStore) Load(context.Context) (githubauth.Credential, error) {
	return githubauth.Credential{AccessToken: "shared", Login: "alice", ID: 1}, nil
}
func (hostHealthStore) Save(context.Context, githubauth.Credential) error { return nil }
func (hostHealthStore) Delete(context.Context) error                      { return nil }

func TestEnterpriseFailureCannotRejectPublicGitHubConnection(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_HOST", "enterprise.example.com")
	t.Setenv("GH_ENTERPRISE_TOKEN", "shared")
	original := config.GitHubAuth
	t.Cleanup(func() { config.GitHubAuth = original })
	config.GitHubAuth = githubauth.New(hostHealthStore{})
	if _, err := config.GitHubAuth.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := NewFromConfig(config.Config{}, Options{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})})
	enterprise, err := c.REST(context.Background(), Repository{Host: "enterprise.example.com", Owner: "org", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = enterprise.Read(context.Background(), "/user", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.GitHubAuth.Health(); err != nil {
		t.Fatal("Enterprise response poisoned public GitHub health", err)
	}
	public, err := c.REST(context.Background(), Repository{Host: "github.com", Owner: "org", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = public.Read(context.Background(), "/user", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.GitHubAuth.Health(); err != githubauth.ErrReconnect {
		t.Fatal("public revocation was not recorded", err)
	}
}

func TestUnauthorizedRereadsTheTokenOnceAndRetriesOnlyWithANewOne(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reread       string
		wantRequests int
		wantStatus   int
	}{
		{"replaced credential is retried once", "second", 2, 200},
		{"same credential is not retried", "first", 1, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolutions, requests := 0, 0
			tokens := func(context.Context, string) (string, error) {
				resolutions++
				if resolutions == 1 {
					return "first", nil
				}
				return tc.reread, nil
			}
			var observed []string
			c := New(tokens, Options{
				AuthResult: func(_, token string, status int, _ http.Header) {
					observed = append(observed, token+":"+http.StatusText(status))
				},
				Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
					requests++
					status := 200
					if r.Header.Get("Authorization") == "token first" {
						status = 401
					}
					body, _ := io.ReadAll(r.Body)
					if string(body) != "payload" {
						t.Errorf("request %d body %q", requests, body)
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
				}),
			})
			request, _ := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", strings.NewReader("payload"))
			response, err := authTransport{host: "api.github.com", tokens: c.tokens, tokenHost: "github.com", base: c.transport, result: c.authResult}.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			if requests != tc.wantRequests || response.StatusCode != tc.wantStatus || resolutions != 2 {
				t.Fatalf("requests %d status %d resolutions %d (observed %v)", requests, response.StatusCode, resolutions, observed)
			}
		})
	}
}
