package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// gqlRequest is one GraphQL request the fake received.
type gqlRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"-"`
	Authorization string         `json:"-"`
	Header        http.Header    `json:"-"`
	// URL is where the client sent the request, before the fake's transport
	// redirected it to the fake.
	URL string `json:"-"`
}

// operationName is the name the GraphQL library writes into the query
// itself: "query PullRequest($owner:String!...){...}".
var operationName = regexp.MustCompile(`^query (\w+)`)

// cursor is the request's "cursor" variable, "" for the first page.
func (r gqlRequest) cursor() string {
	cursor, _ := r.Variables["cursor"].(string)
	return cursor
}

type fakeResponse struct {
	status int
	header http.Header
	body   any
}

// route answers one operation; call counts that operation's requests from 1.
type route func(request gqlRequest, call int) fakeResponse

// fakeGitHub is an httptest GraphQL endpoint answering by operation name.
type fakeGitHub struct {
	t          *testing.T
	server     *httptest.Server
	mu         sync.Mutex
	routes     map[string]route
	calls      map[string]int
	requests   []gqlRequest
	tokenHosts []string
}

func newFakeGitHub(t *testing.T, routes map[string]route) *fakeGitHub {
	t.Helper()
	fake := &fakeGitHub{t: t, routes: routes, calls: map[string]int{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	var request gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		f.t.Errorf("decoding request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	request.Authorization = r.Header.Get("Authorization")
	request.Header = r.Header.Clone()
	request.URL = r.Header.Get(originalURLHeader)
	if match := operationName.FindStringSubmatch(request.Query); match != nil {
		request.OperationName = match[1]
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.calls[request.OperationName]++
	call := f.calls[request.OperationName]
	handler, ok := f.routes[request.OperationName]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected operation %q", request.OperationName)
		http.Error(w, "unexpected operation", http.StatusBadRequest)
		return
	}
	response := handler(request, call)
	for key, values := range response.header {
		w.Header()[key] = values
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.status)
	if err := json.NewEncoder(w).Encode(response.body); err != nil {
		f.t.Errorf("encoding response: %v", err)
	}
}

func (f *fakeGitHub) recorded() []gqlRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gqlRequest(nil), f.requests...)
}

func (f *fakeGitHub) callCount(operation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[operation]
}

// originalURLHeader carries a request's own URL to the fake.
const originalURLHeader = "X-Test-Original-URL"

// toFake sends every request for a GitHub host to the fake, keeping its
// headers. It stands in for the network below the client's transports.
type toFake struct{ target *url.URL }

func (t toFake) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() == "127.0.0.1" {
		// Another test server, such as a redirect's target.
		return http.DefaultTransport.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	clone.Header.Set(originalURLHeader, request.URL.String())
	clone.URL.Scheme, clone.URL.Host, clone.Host = t.target.Scheme, t.target.Host, ""
	return http.DefaultTransport.RoundTrip(clone)
}

func (f *fakeGitHub) transport() http.RoundTripper {
	target, err := url.Parse(f.server.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	return toFake{target: target}
}

// testTokens hands out "test-token" for every host and records the hosts
// asked for.
func (f *fakeGitHub) testTokens(_ context.Context, host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenHosts = append(f.tokenHosts, host)
	return "test-token", nil
}

// client returns a client for the fake that records its waits instead of
// sleeping. gh's configuration is isolated from the developer's.
func (f *fakeGitHub) client(waits *[]time.Duration) *Client {
	f.t.Setenv("GH_CONFIG_DIR", f.t.TempDir())
	c := New(f.testTokens, Options{Transport: f.transport()})
	c.rateLimitBackoff, c.retryBackoff = 10*time.Second, time.Second
	c.sleep = func(_ context.Context, d time.Duration) error {
		if waits != nil {
			*waits = append(*waits, d)
		}
		return nil
	}
	return c
}

func data(value any) fakeResponse {
	return fakeResponse{status: http.StatusOK, body: map[string]any{"data": value}}
}

// graphQLError is GitHub answering with an application error and, as it
// does, whatever data it could still resolve.
func graphQLError(value any, kind, message string) fakeResponse {
	return fakeResponse{status: http.StatusOK, body: map[string]any{
		"data":   value,
		"errors": []map[string]any{{"type": kind, "message": message}},
	}}
}

// page is a connection page; next is the end cursor, "" for the last page.
func page(nodes []any, next string) map[string]any {
	info := map[string]any{"hasNextPage": next != "", "endCursor": nil}
	if next != "" {
		info["endCursor"] = next
	}
	return map[string]any{"nodes": nodes, "pageInfo": info}
}

// pages routes a paginated connection: cursors maps each request cursor
// ("" for the first page) to the response.
func pages(t *testing.T, cursors map[string]func() fakeResponse) route {
	return func(request gqlRequest, _ int) fakeResponse {
		respond, ok := cursors[request.cursor()]
		if !ok {
			t.Errorf("%s: unexpected cursor %q", request.OperationName, request.cursor())
			return fakeResponse{status: http.StatusBadRequest, body: map[string]any{"message": "bad cursor"}}
		}
		return respond()
	}
}

func joinQueries(requests []gqlRequest) string {
	queries := make([]string, 0, len(requests))
	for _, request := range requests {
		queries = append(queries, request.Query)
	}
	return strings.Join(queries, "\n")
}
