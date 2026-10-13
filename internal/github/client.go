// Package github reads one pull request's metadata, commits, checks and
// conversation from GitHub's GraphQL API with the user's own token, and maps
// them onto the generation bundle the way every baum host does. It is the
// only part of baum that calls GitHub's API.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	graphql "github.com/hasura/go-graphql-client"
	"golang.org/x/oauth2"

	"github.com/lastpersonlabs/baum/githubapi"
	"github.com/lastpersonlabs/baum/internal/config"
)

// DefaultEndpoint is GitHub's GraphQL API.
const DefaultEndpoint = "https://api.github.com/graphql"

// Options configure a Client. The zero value talks to GitHub.
type Options struct {
	// Endpoint is the GraphQL URL; empty uses DefaultEndpoint.
	Endpoint string
	// Transport carries the requests; nil uses http.DefaultTransport.
	Transport http.RoundTripper
}

// Client is a GitHub GraphQL client authenticated with one token.
type Client struct {
	gql *graphql.Client

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

// New returns a client that sends token as a bearer token with every
// request.
func New(token string, opts Options) *Client {
	base := opts.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	transport := &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		Base:   githubapi.RateLimitTransport(base),
	}
	return &Client{
		gql:              graphql.NewClient(endpoint, &http.Client{Transport: transport, CheckRedirect: refuseRedirect}),
		maxRetries:       4,
		maxWait:          2 * time.Minute,
		rateLimitBackoff: time.Minute,
		retryBackoff:     time.Second,
		now:              time.Now,
		sleep:            sleep,
	}
}

// errRedirect reports a redirect the client refused to follow.
var errRedirect = errors.New("github answered with a redirect, which baum does not follow")

// refuseRedirect follows no redirect. The token transport sets the
// Authorization header on every request it sends, including one to another
// host, so a followed redirect could hand the token to it; GitHub's GraphQL
// endpoint never redirects.
func refuseRedirect(*http.Request, []*http.Request) error { return errRedirect }

// NewFromConfig returns a client for the token cfg resolves: the configured
// one (GITHUB_TOKEN, then GH_TOKEN, then the config file), or `gh auth token`
// when none is set.
func NewFromConfig(ctx context.Context, cfg config.Config, opts Options) (*Client, error) {
	token, err := cfg.ResolveGitHubToken(ctx)
	if err != nil {
		return nil, err
	}
	return New(token, opts), nil
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

// query runs one GraphQL operation into q, retrying rate limits and an
// unavailable GitHub with backoff. q is reset before each attempt.
func (c *Client) query(ctx context.Context, operation string, q any, variables map[string]any) error {
	for attempt := 0; ; attempt++ {
		reflect.ValueOf(q).Elem().SetZero()
		err := c.gql.Query(ctx, q, variables, graphql.OperationName(operation))
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
			if graphQLErrorMentions(err, "could not resolve to a") {
				return fmt.Errorf("%w: %s", ErrNotFound, graphQLMessages(err))
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
	if errors.As(err, &rateLimit) || graphQLErrorMentions(err, "rate limit") {
		if rateLimit != nil && !rateLimit.ResumeAt.IsZero() {
			return max(rateLimit.ResumeAt.Sub(c.now()), 0), rateLimit.ResumeAt, true, true
		}
		return c.rateLimitBackoff << attempt, time.Time{}, true, true
	}
	var network graphql.NetworkError
	if errors.As(err, &network) {
		switch network.StatusCode() {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return c.retryBackoff << attempt, time.Time{}, false, true
		}
	}
	return 0, time.Time{}, false, false
}

// graphQLErrorMentions reports a GraphQL application error whose message
// contains text. The client does not keep GitHub's top-level error type, so
// the message is the only signal left.
func graphQLErrorMentions(err error, text string) bool {
	var graphErrors graphql.Errors
	if !errors.As(err, &graphErrors) {
		return false
	}
	for _, graphError := range graphErrors {
		if strings.Contains(strings.ToLower(graphError.Message), strings.ToLower(text)) {
			return true
		}
	}
	return false
}

func graphQLMessages(err error) string {
	var graphErrors graphql.Errors
	if !errors.As(err, &graphErrors) {
		return err.Error()
	}
	messages := make([]string, 0, len(graphErrors))
	for _, graphError := range graphErrors {
		messages = append(messages, graphError.Message)
	}
	return strings.Join(messages, "; ")
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
