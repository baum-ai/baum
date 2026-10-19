// Package apiclient is the Go client for the hosted baum HTTP API, used by the
// hosted commands of the baum CLI.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GateRequiredHeader marks a response a deployment's shared-password gate
// refused, rather than ordinary authentication. Clients key off the header
// because 403 alone cannot be told apart from a real authorization failure. The
// server's gate sets these values, so they live here, where both sides read
// them.
const (
	GateRequiredHeader      = "X-Baum-Gate"
	GateRequiredHeaderValue = "required"
)

// Device-flow poll answers, matching the RFC 8628 error strings the server
// uses as client-safe messages.
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrExpiredToken         = errors.New("expired_token")
	ErrAccessDenied         = errors.New("access_denied")

	// ErrUnauthenticated is any 401 answer: a missing, revoked, or expired
	// API token. A gated deployment refuses an unusable bearer token with 403
	// and X-Baum-Gate: required instead, because it never learns
	// whether the token was bad or merely absent; since a valid token always
	// passes the gate, that answer means the same thing to a client that sent
	// one.
	ErrUnauthenticated = errors.New("unauthenticated")
)

// pollErrors maps server error messages to poll sentinels.
var pollErrors = map[string]error{
	ErrAuthorizationPending.Error(): ErrAuthorizationPending,
	ErrSlowDown.Error():             ErrSlowDown,
	ErrExpiredToken.Error():         ErrExpiredToken,
	ErrAccessDenied.Error():         ErrAccessDenied,
}

// defaultHTTPClient serves clients without their own HTTP client. Unlike
// http.DefaultClient it gives up on a stalled proxy or half-open connection.
// The response-header timeout starts once the request body is written, so a
// large upload is not cut short.
var defaultHTTPClient = newHTTPClient(2 * time.Minute)

func newHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: transport}
}

// Client calls the baum API. Token, when set, is sent as a bearer
// credential.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// DeviceAuthorizationStart mirrors the POST /auth/device response.
type DeviceAuthorizationStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceToken mirrors the POST /auth/device/token success response.
type DeviceToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

// User mirrors the GET /api/me response.
type User struct {
	ID          string  `json:"id"`
	GitHubLogin string  `json:"github_login"`
	Name        *string `json:"name"`
	Email       *string `json:"email"`
}

// GitHubRepository is a repository connected to the authenticated user.
type GitHubRepository struct {
	ID       string `json:"id"`
	FullName string `json:"full_name"`
	// Enabled is baum's own switch; the server works on a repository only while
	// it is on.
	Enabled bool `json:"enabled"`
}

// ListGitHubInstallations returns every non-archived repository connected to
// the authenticated user, flattened across GitHub App installations.
func (c *Client) ListGitHubInstallations(ctx context.Context) ([]GitHubRepository, error) {
	if c.Token == "" {
		return nil, ErrUnauthenticated
	}
	var response struct {
		Installations []struct {
			Repositories []struct {
				ID       string `json:"id"`
				FullName string `json:"full_name"`
				Archived bool   `json:"archived"`
				Enabled  bool   `json:"enabled"`
			} `json:"repositories"`
		} `json:"installations"`
	}
	if err := c.do(ctx, http.MethodGet, "/github/installations", nil, &response); err != nil {
		return nil, err
	}
	var repositories []GitHubRepository
	for _, installation := range response.Installations {
		for _, repository := range installation.Repositories {
			if !repository.Archived {
				repositories = append(repositories, GitHubRepository{ID: repository.ID, FullName: repository.FullName, Enabled: repository.Enabled})
			}
		}
	}
	return repositories, nil
}

// StartDeviceAuthorization opens a device-flow login named after the device.
func (c *Client) StartDeviceAuthorization(ctx context.Context, name string) (DeviceAuthorizationStart, error) {
	var out DeviceAuthorizationStart
	err := c.do(ctx, http.MethodPost, "/auth/device", map[string]string{"name": name}, &out)
	return out, err
}

// PollDeviceToken asks whether the device login was approved. Pending,
// throttled, denied, and expired answers surface as the package sentinels.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (DeviceToken, error) {
	var out DeviceToken
	err := c.do(ctx, http.MethodPost, "/auth/device/token", map[string]string{"device_code": deviceCode}, &out)
	return out, err
}

// Me returns the user the client's token authenticates as.
func (c *Client) Me(ctx context.Context) (User, error) {
	if c.Token == "" {
		return User{}, ErrUnauthenticated
	}
	var out User
	err := c.do(ctx, http.MethodGet, "/me", nil, &out)
	return out, err
}

// FindGitHubRepository returns one connected repository by its GitHub full name.
func (c *Client) FindGitHubRepository(ctx context.Context, fullName string) (GitHubRepository, error) {
	if c.Token == "" {
		return GitHubRepository{}, ErrUnauthenticated
	}
	var out GitHubRepository
	query := url.Values{"full_name": []string{fullName}}
	err := c.do(ctx, http.MethodGet, "/github/repositories/find?"+query.Encode(), nil, &out)
	return out, err
}

// RequestGitHubRepositorySync queues synchronization for a repository.
func (c *Client) RequestGitHubRepositorySync(ctx context.Context, repositoryID string) error {
	if c.Token == "" {
		return ErrUnauthenticated
	}
	path := "/github/repositories/" + url.PathEscape(repositoryID) + "/sync"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// UploadRepositoryTraces streams a tar.gz trace bundle to a repository.
func (c *Client) UploadRepositoryTraces(ctx context.Context, repositoryID string, archive io.Reader, size int64) error {
	if c.Token == "" {
		return ErrUnauthenticated
	}
	path := "/github/repositories/" + url.PathEscape(repositoryID) + "/trace-uploads"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+path, archive)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Content-Disposition", `attachment; filename="baum-traces.tar.gz"`)
	req.ContentLength = size
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = defaultHTTPClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return apiError(resp, c.Token != "")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RevokeCurrentToken revokes the token this client authenticates with.
func (c *Client) RevokeCurrentToken(ctx context.Context) error {
	if c.Token == "" {
		return ErrUnauthenticated
	}
	return c.do(ctx, http.MethodDelete, "/tokens/current", nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = defaultHTTPClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return apiError(resp, c.Token != "")
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// apiError maps an error response to a sentinel where one exists, and to a
// descriptive error otherwise. sentBearer reports whether the request carried
// a bearer token, which decides what a gate refusal means.
func apiError(resp *http.Response, sentBearer bool) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)

	if err, ok := pollErrors[body.Error]; ok {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthenticated
	}
	if sentBearer && resp.StatusCode == http.StatusForbidden && resp.Header.Get(GateRequiredHeader) == GateRequiredHeaderValue {
		return ErrUnauthenticated
	}
	if body.Error != "" {
		return fmt.Errorf("server returned %s: %s", resp.Status, body.Error)
	}
	return fmt.Errorf("server returned %s", resp.Status)
}
