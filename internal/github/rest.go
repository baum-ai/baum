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

// RESTReader reads, and for the confirmed writes of the app (Write) sends, only relative API paths on one validated GitHub host.
// Its identity partitions private caches without persisting a credential.
type RESTReader struct {
	client          *http.Client
	endpoint        *url.URL
	identity        string
	graphqlEndpoint *url.URL
}

var ErrIdentityChanged = errors.New("GitHub identity changed; retry the operation")

// ErrResponseTooLarge is a response over Read's 8 MiB bound.
var ErrResponseTooLarge = errors.New("GitHub discovery response unreadable or too large")

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
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitHub discovery response unreadable or too large")
	}
	if len(body) > 8<<20 {
		return 0, nil, nil, ErrResponseTooLarge
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

// maxDownload bounds how much of a download Download reads.
const maxDownload = 64 << 20

// Download reads path, which GitHub may answer with a redirect to a signed
// download URL as it does for job logs, and returns the status, writing the
// body to w when it is 200. One redirect is followed, only to an https URL,
// and the token is never sent there: it goes to the API host alone. At most
// 64 MiB are read; w keeps what it needs of them.
func (r *RESTReader) Download(ctx context.Context, path string, w io.Writer) (int, error) {
	status, _, err := r.download(ctx, path, w, 0)
	return status, err
}

// ErrDownloadTooLarge is a download whose end DownloadTail could not reach:
// it is over the bound and its host does not honour a range request.
var ErrDownloadTooLarge = errors.New("the download is too large to read the end of")

// DownloadTail is Download for the end of a long file, as a job log's last
// lines: after the redirect it asks the download host for the last tail
// bytes only (a suffix range request). It writes what it got to w and reports
// partial when that is only the end, whose first line may be cut; the file
// whole, when the host ignores the range and it is under the bound, is not
// partial. A host that ignores the range for a file over the 64 MiB bound
// answers ErrDownloadTooLarge rather than the wrong part of the file.
func (r *RESTReader) DownloadTail(ctx context.Context, path string, w io.Writer, tail int64) (status int, partial bool, err error) {
	return r.download(ctx, path, w, max(tail, 1))
}

func (r *RESTReader) download(ctx context.Context, path string, w io.Writer, tail int64) (int, bool, error) {
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.Host != "" || relative.Scheme != "" || relative.Fragment != "" {
		return 0, false, fmt.Errorf("invalid GitHub API path")
	}
	endpoint := *r.endpoint
	endpoint.Path += relative.Path
	endpoint.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, false, err
	}
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return 0, false, fmt.Errorf("GitHub request failed")
	}
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		location, err := response.Location()
		_ = response.Body.Close()
		if err != nil || location.Scheme != "https" || location.Host == "" {
			return 0, false, fmt.Errorf("GitHub redirected to an unusable download URL")
		}
		next, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return 0, false, err
		}
		if tail > 0 {
			next.Header.Set("Range", fmt.Sprintf("bytes=-%d", tail))
		}
		if response, err = client.Do(next); err != nil {
			return 0, false, fmt.Errorf("GitHub download failed")
		}
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case tail > 0 && response.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// A range over an empty file.
		return http.StatusOK, false, nil
	case tail > 0 && response.StatusCode == http.StatusPartialContent:
		if _, err := io.Copy(w, io.LimitReader(response.Body, tail)); err != nil {
			return 0, false, fmt.Errorf("GitHub download unreadable")
		}
		// A file no longer than the range comes whole, from its first byte.
		return http.StatusOK, !fromFirstByte(response.Header.Get("Content-Range")), nil
	case response.StatusCode != http.StatusOK:
		return response.StatusCode, false, nil
	}
	if tail == 0 {
		if _, err := io.Copy(w, io.LimitReader(response.Body, maxDownload)); err != nil {
			return 0, false, fmt.Errorf("GitHub download unreadable")
		}
		return response.StatusCode, false, nil
	}
	// The host ignored the range: the file whole, or too long to read.
	n, err := io.Copy(w, io.LimitReader(response.Body, maxDownload+1))
	switch {
	case err != nil:
		return 0, false, fmt.Errorf("GitHub download unreadable")
	case n > maxDownload:
		return 0, false, ErrDownloadTooLarge
	}
	return response.StatusCode, false, nil
}

// fromFirstByte reports whether a 206 answer's Content-Range ("bytes
// first-last/size") starts at the file's first byte. A missing or unreadable
// header counts as not, so the part is treated as cut.
func fromFirstByte(contentRange string) bool {
	unit, rest, ok := strings.Cut(contentRange, " ")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return false
	}
	first, _, ok := strings.Cut(rest, "-")
	return ok && first == "0"
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

// Write sends one PUT or POST with a JSON body to a relative API path and
// returns the status and the bounded answer. It is never retried: the callers
// are the user's confirmed writes (update a branch, re-run workflow runs).
func (r *RESTReader) Write(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	relative, err := url.Parse(path)
	if (method != http.MethodPut && method != http.MethodPost) || err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.Host != "" || relative.Scheme != "" || relative.Fragment != "" {
		return 0, nil, fmt.Errorf("invalid GitHub API request")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *r.endpoint
	endpoint.Path += relative.Path
	endpoint.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("GitHub request failed")
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("GitHub response unreadable")
	}
	return response.StatusCode, answer, nil
}
