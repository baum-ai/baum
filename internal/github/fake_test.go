package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// gqlRequest is one GraphQL request the fake received.
type gqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
	Authorization string         `json:"-"`
}

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
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	routes   map[string]route
	calls    map[string]int
	requests []gqlRequest
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

// client returns a client for the fake that records its waits instead of
// sleeping.
func (f *fakeGitHub) client(waits *[]time.Duration) *Client {
	c := New("test-token", Options{Endpoint: f.server.URL})
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
