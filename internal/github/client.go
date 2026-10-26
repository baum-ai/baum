// Package github reads one pull request's metadata, commits, checks and
// conversation from GitHub's GraphQL API with the user's own token, and maps
// them onto the generation bundle the way every baum host does. It is the
// only part of baum that calls GitHub's API. Repositories and SSH
// aliases are parsed with go-gh; baum owns host recognition and credentials; the GraphQL requests are baum's own, over shurcooL-graphql.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	graphql "github.com/cli/shurcooL-graphql"

	"github.com/baum-ai/baum/githubapi"
	"github.com/baum-ai/baum/internal/config"
)

// TokenSource returns the token for one GitHub host. The client asks it only
// for the host of the repository it is about to read, and sends the token
// only there.
type TokenSource func(ctx context.Context, host string) (string, error)

// Options configure a Client. The zero value talks to GitHub.
type Options struct {
	// Transport carries the requests; nil uses http.DefaultTransport.
	Transport       http.RoundTripper
	AuthResult      func(host, token string, status int, headers http.Header)
	AccountIdentity func(host, token string) string
}

// Client reads GitHub GraphQL evidence and read-only REST discovery metadata
// from any recognized GitHub host with that host's token. Its writes are
// merging a pull request the user confirmed (merge.go), through
// RESTReader.Write, updating its branch and re-running its workflow runs
// (internal/prupdate), and, after a confirmed fix's push, replying on and
// resolving the review threads the fix addressed (threads.go).
type Client struct {
	tokens          TokenSource
	transport       http.RoundTripper
	restTransport   http.RoundTripper
	authResult      func(string, string, int, http.Header)
	accountIdentity func(string, string) string

	mu    sync.Mutex
	hosts map[string]*graphql.Client

	// maxRetries bounds the retries of one request; maxWait is the longest
	// single wait for a rate limit, beyond which the request fails.
	maxRetries int
	maxWait    time.Duration
	// rateLimitBackoff is the first wait after a rate limit GitHub gave no
	// reset time for (GitHub asks for at least a minute), retryBackoff after
	// GitHub was unavailable; both double with each retry.
	rateLimitBackoff time.Duration
	retryBackoff     time.Duration
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
}

// New returns a client that reads each host with the token tokens returns
// for it.
func New(tokens TokenSource, opts Options) *Client {
	base := opts.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &Client{
		tokens:           tokens,
		authResult:       opts.AuthResult,
		accountIdentity:  opts.AccountIdentity,
		restTransport:    base,
		transport:        githubapi.RateLimitTransport(unavailableTransport{base: base}),
		hosts:            map[string]*graphql.Client{},
		maxRetries:       4,
		maxWait:          2 * time.Minute,
		rateLimitBackoff: time.Minute,
		retryBackoff:     time.Second,
		now:              time.Now,
		sleep:            sleep,
	}
}

// NewFromConfig returns a client whose tokens cfg resolves per host (see
// config.Config.ResolveGitHubToken).
func NewFromConfig(cfg config.Config, opts Options) *Client {
	if opts.AuthResult == nil {
		opts.AuthResult = func(host, token string, status int, headers http.Header) {
			if host == "github.com" {
				config.GitHubAuth.ObserveResponse(token, status, headers)
			}
		}
	}
	if opts.AccountIdentity == nil {
		opts.AccountIdentity = func(host, token string) string {
			if _, source := cfg.ExplicitGitHubTokenSource(host); source != "" || host != "github.com" {
				return ""
			}
			if id := config.GitHubAuth.AccountForToken(token); id > 0 {
				return "baum-account:" + strconv.FormatInt(id, 10)
			}
			return ""
		}
	}
	return New(cfg.ResolveGitHubToken, opts)
}

// requestTimeout bounds one GraphQL request, however slowly GitHub answers.
const requestTimeout = 2 * time.Minute

// ErrUnknownHost reports a host baum does not recognize as GitHub. No token is
// looked up for it and no request is sent to it.
var ErrUnknownHost = errors.New("unrecognized GitHub host: use github.com, a *.ghe.com tenant, or explicitly set GH_HOST")

// graphQL returns the GraphQL client for host, created on first use with the
// endpoint. The token is resolved for every request to the host's GraphQL
// endpoint, the client follows no redirect, and responses reach the mapping
// byte for byte. The token lookup, which may wait on a keyring, runs without
// the client's lock held.
func (c *Client) graphQL(ctx context.Context, host string) (*graphql.Client, error) {
	host = auth.NormalizeHostname(host)
	if !gitHubHost(host) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHost, host)
	}
	c.mu.Lock()
	client, ok := c.hosts[host]
	c.mu.Unlock()
	if ok {
		return client, nil
	}
	endpoint := graphQLEndpoint(host)
	httpClient := &http.Client{
		Transport:     authTransport{host: endpoint.Host, tokens: c.tokens, tokenHost: host, base: c.transport, result: c.authResult},
		CheckRedirect: refuseRedirect,
		Timeout:       requestTimeout,
	}
	client = graphql.NewClient(endpoint.String(), httpClient)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.hosts[host]; ok {
		return existing, nil
	}
	c.hosts[host] = client
	return client, nil
}

// graphQLEndpoint is a host's GraphQL API, derived as gh derives it:
// api.github.com for github.com, api.<host> for a *.ghe.com tenant, and
// /api/graphql on the host itself for an Enterprise Server. gh's per-host
// api_host override is not read: go-gh does not expose it.
func graphQLEndpoint(host string) *url.URL {
	if host == config.GitHubHost || auth.IsTenancy(host) {
		return &url.URL{Scheme: "https", Host: "api." + host, Path: "/graphql"}
	}
	return &url.URL{Scheme: "https", Host: host, Path: "/api/graphql"}
}

// authTransport adds the host's token, and identifies baum, on requests for
// the host's GraphQL endpoint only.
type authTransport struct {
	result    func(string, string, int, http.Header)
	tokens    TokenSource
	tokenHost string
	host      string
	token     string
	base      http.RoundTripper
}

func (t authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("User-Agent", "baum")
	usedToken := ""
	if request.URL.Scheme == "https" && strings.EqualFold(request.URL.Host, t.host) {
		token := t.token
		if t.tokens != nil {
			var err error
			token, err = t.tokens(request.Context(), t.tokenHost)
			if err != nil {
				return nil, err
			}
		}
		request.Header.Set("Authorization", "token "+token)
		usedToken = token
	}
	response, err := t.base.RoundTrip(request)
	if err == nil && response != nil && usedToken != "" && t.result != nil {
		t.result(t.tokenHost, usedToken, response.StatusCode, response.Header)
	}
	return response, err
}

// errRedirect reports a redirect the client refused to follow.
var errRedirect = errors.New("github answered with a redirect, which baum does not follow")

// refuseRedirect follows no redirect. The token is already withheld from
// other hosts, but GitHub's GraphQL endpoint never redirects, so a redirect
// is a misconfiguration better reported than followed.
func refuseRedirect(*http.Request, []*http.Request) error { return errRedirect }

// unavailableError is GitHub answering 502, 503 or 504. The GraphQL
// transport reduces any other status to text, so this transport keeps
// these, the ones worth retrying, typed.
type unavailableError struct{ status int }

func (e *unavailableError) Error() string {
	return fmt.Sprintf("github is unavailable (HTTP %d)", e.status)
}

type unavailableTransport struct{ base http.RoundTripper }

func (t unavailableTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	switch response.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		_ = response.Body.Close()
		return nil, &unavailableError{status: response.StatusCode}
	}
	return response, nil
}

// RateLimitedError reports a GitHub rate limit the client did not wait out:
// its reset was further away than the client waits, or it persisted through
// every retry. ResumeAt is when GitHub said to try again, zero when unknown.
type RateLimitedError struct {
	ResumeAt time.Time
	Err      error
}

func (e *RateLimitedError) Error() string {
	if e.ResumeAt.IsZero() {
		return "GitHub rate limit exceeded; try again later"
	}
	return "GitHub rate limit exceeded; try again after " + e.ResumeAt.Local().Format(time.Kitchen)
}

func (e *RateLimitedError) Unwrap() error { return e.Err }

// ErrNotFound reports a repository or pull request GitHub does not show to
// this token.
var ErrNotFound = errors.New("not found on GitHub")

// query runs one GraphQL operation on host into q, retrying rate limits and
// an unavailable GitHub with backoff. q is reset before each attempt.
func (c *Client) query(ctx context.Context, host, operation string, q any, variables map[string]any) error {
	gql, err := c.graphQL(ctx, host)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		reflect.ValueOf(q).Elem().SetZero()
		err := gql.QueryNamed(ctx, operation, q, variables)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		wait, resumeAt, limited, retry := c.retryWait(err, attempt)
		switch {
		case limited && (!retry || attempt >= c.maxRetries || wait > c.maxWait):
			return &RateLimitedError{ResumeAt: resumeAt, Err: err}
		case !retry || attempt >= c.maxRetries:
			if graphQLErrorType(err, "NOT_FOUND") {
				return fmt.Errorf("%w: %w", ErrNotFound, err)
			}
			return fmt.Errorf("github %s query: %w", operation, err)
		}
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// retryWait says how long to wait before retrying err, when GitHub said to
// resume (zero when it did not say), whether err is a rate limit, and whether
// it is worth retrying at all.
func (c *Client) retryWait(err error, attempt int) (wait time.Duration, resumeAt time.Time, limited, retry bool) {
	var rateLimit *githubapi.RateLimitError
	if errors.As(err, &rateLimit) || graphQLErrorType(err, "RATE_LIMITED") {
		if rateLimit != nil && !rateLimit.ResumeAt.IsZero() {
			return max(rateLimit.ResumeAt.Sub(c.now()), 0), rateLimit.ResumeAt, true, true
		}
		return c.rateLimitBackoff << attempt, time.Time{}, true, true
	}
	var unavailable *unavailableError
	if errors.As(err, &unavailable) {
		return c.retryBackoff << attempt, time.Time{}, false, true
	}
	return 0, time.Time{}, false, false
}

// graphQLErrorType reports a GraphQL error of GitHub's type kind, such as
// NOT_FOUND or RATE_LIMITED.
func graphQLErrorType(err error, kind string) bool {
	var graphErr graphql.Errors
	if !errors.As(err, &graphErr) {
		return false
	}
	for _, item := range graphErr {
		if item.Type == kind {
			return true
		}
	}
	return false
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
