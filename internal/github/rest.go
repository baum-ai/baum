package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
)

// RESTReader reads only relative API paths on one validated GitHub host.
// Its identity partitions private caches without persisting a credential.
type RESTReader struct {
	client          *http.Client
	endpoint        *url.URL
	identity        string
	graphqlEndpoint *url.URL
}

var ErrIdentityChanged = errors.New("GitHub identity changed; retry the operation")

func (c *Client) REST(ctx context.Context, repository Repository) (*RESTReader, error) {
	host := auth.NormalizeHostname(repository.Host)
	if _, ok := checkedRepository(host, repository.Owner, repository.Name); !ok {
		return nil, ErrInvalidRepository
	}
	if !gitHubHost(host) {
		return nil, ErrUnknownHost
	}
	token, err := c.tokens(ctx, host)
	if err != nil {
		return nil, err
	}
	endpoint := graphQLEndpoint(host)
	endpoint.Path = ""
	if endpoint.Host == host {
		endpoint.Path = "/api/v3"
	}
	identity := "token:" + token
	if c.accountIdentity != nil {
		if account := c.accountIdentity(host, token); account != "" {
			identity = account
		}
	}
	digest := sha256.Sum256([]byte(host + "\x00" + identity))
	// A discovery transaction must never combine accounts in one cache. Resolve
	// credentials on every request, but abort if this reader's partition changed.
	tokens := func(ctx context.Context, host string) (string, error) {
		current, err := c.tokens(ctx, host)
		if err != nil {
			return "", err
		}
		if current != token {
			return "", ErrIdentityChanged
		}
		return current, nil
	}
	return &RESTReader{client: &http.Client{Transport: authTransport{host: endpoint.Host, tokens: tokens, tokenHost: host, base: c.restTransport, result: c.authResult}, CheckRedirect: refuseRedirect, Timeout: requestTimeout}, endpoint: endpoint, identity: hex.EncodeToString(digest[:]), graphqlEndpoint: graphQLEndpoint(host)}, nil
}

func (r *RESTReader) CacheIdentity() string { return r.identity }

// Read returns bounded raw JSON and headers, including rate and conditional
// response headers. Only GET is available; redirects are refused.
func (r *RESTReader) Read(ctx context.Context, path, etag string) (int, http.Header, []byte, error) {
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.Host != "" || relative.Scheme != "" || relative.Fragment != "" {
		return 0, nil, nil, fmt.Errorf("invalid GitHub API path")
	}
	endpoint := *r.endpoint
	endpoint.Path += relative.Path
	endpoint.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitHub discovery request failed")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		return 0, nil, nil, fmt.Errorf("GitHub discovery response unreadable or too large")
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

// maxTailDownload bounds how much of a download Tail reads, however much of it
// it keeps.
const maxTailDownload = 64 << 20

// Tail reads path, which GitHub may answer with a redirect to a signed
// download URL as it does for job logs, and returns the status and the last
// limit bytes of the body. One redirect is followed, only to an https URL,
// and the token is never sent there: it goes to the API host alone. At most
// 64 MiB are read.
func (r *RESTReader) Tail(ctx context.Context, path string, limit int) (int, []byte, error) {
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.Host != "" || relative.Scheme != "" || relative.Fragment != "" || limit <= 0 {
		return 0, nil, fmt.Errorf("invalid GitHub API path")
	}
	endpoint := *r.endpoint
	endpoint.Path += relative.Path
	endpoint.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("GitHub request failed")
	}
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		location, err := response.Location()
		_ = response.Body.Close()
		if err != nil || location.Scheme != "https" || location.Host == "" {
			return 0, nil, fmt.Errorf("GitHub redirected to an unusable download URL")
		}
		next, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return 0, nil, err
		}
		if response, err = client.Do(next); err != nil {
			return 0, nil, fmt.Errorf("GitHub download failed")
		}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, nil, nil
	}
	tail := &tailBuffer{limit: limit}
	if _, err := io.Copy(tail, io.LimitReader(response.Body, maxTailDownload)); err != nil {
		return 0, nil, fmt.Errorf("GitHub download unreadable")
	}
	return response.StatusCode, tail.bytes(), nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	if len(t.buf) > t.limit {
		return t.buf[len(t.buf)-t.limit:]
	}
	return t.buf
}

// GraphQL reads one bounded operation without retries or waiting. Discovery owns
// scheduling and budgets; this uses the same authenticated, redirect-safe client.
func (r *RESTReader) GraphQL(ctx context.Context, query string, variables map[string]any) (int, http.Header, []byte, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return 0, nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.graphqlEndpoint.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitHub discovery request failed")
	}
	defer func() { _ = response.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		return 0, nil, nil, fmt.Errorf("GitHub discovery response unreadable or too large")
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}
