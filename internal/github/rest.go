package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	digest := sha256.Sum256([]byte(host + "\x00" + token))
	return &RESTReader{client: &http.Client{Transport: authTransport{host: endpoint.Host, token: token, base: c.restTransport}, CheckRedirect: refuseRedirect, Timeout: requestTimeout}, endpoint: endpoint, identity: hex.EncodeToString(digest[:]), graphqlEndpoint: graphQLEndpoint(host)}, nil
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
	defer response.Body.Close()
	body, err = io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		return 0, nil, nil, fmt.Errorf("GitHub discovery response unreadable or too large")
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}
