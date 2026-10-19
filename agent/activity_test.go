package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log destination the watchdog and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// hang is a backend attempt that blocks until the run cancels it, having
// started a tool when tool is set.
func hang(tool bool) func(context.Context, Request, int) (Result, error) {
	return func(ctx context.Context, request Request, n int) (Result, error) {
		if tool {
			request.Activity.ToolStarted(fmt.Sprintf("item_%d", n), "command_execution")
		}
		<-ctx.Done()
		return Result{Calls: 1, Usage: Usage{Unreported: true}}, ctx.Err()
	}
}

func TestGenerateEndsAToolThatRunsPastTheToolTimeoutAndRetriesOnce(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	logs := &syncBuffer{}
	generator.Logger = slog.New(slog.NewTextHandler(logs, nil))
	run.Budgets.ToolTimeout = 100 * time.Millisecond
	run.Budgets.MaxProviderAttempts = 1
	backend.run = hang(true)
	started := time.Now()
	outcome, document, err := generator.Generate(context.Background(), run)
	if err == nil || err.Error() != ReasonStalled || document != nil {
		t.Fatalf("err = %v, want %q", err, ReasonStalled)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("two stalls took %s", elapsed)
	}
	// One retry, which spends no provider attempt; both attempts' calls count.
	if len(backend.calls) != 2 || outcome.ProviderAttempts != 2 || outcome.ErroredAttempts != 2 || outcome.Calls != 2 || !outcome.Usage.Unreported {
		t.Fatalf("outcome = %+v, backend calls = %d", outcome, len(backend.calls))
	}
	stalls := 0
	for _, event := range outcome.Diagnostics {
		if event.Kind == "stalled" {
			stalls++
			if event.Status != "tool_timeout" || event.Tool != "command_execution" || event.ElapsedMS < 100 {
				t.Fatalf("stall diagnostic = %+v", event)
			}
		}
	}
	if stalls != 2 {
		t.Fatalf("diagnostics = %+v, want two stalls", outcome.Diagnostics)
	}
	if text := logs.String(); strings.Count(text, `msg="review agent stalled" cause=tool_timeout tool=command_execution item=item_`) != 2 || strings.Contains(text, "review agent failed") {
		t.Fatalf("logs:\n%s", text)
	}
}

func TestGenerateRetriesAStalledAttemptAndSucceeds(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.ToolTimeout = 100 * time.Millisecond
	run.Budgets.MaxProviderAttempts = 1
	backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
		if n == 0 {
			return hang(true)(ctx, request, n)
		}
		return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
	}
	outcome, document, err := generator.Generate(context.Background(), run)
	if err != nil || len(document) == 0 || len(backend.calls) != 2 || outcome.Calls != 2 || outcome.ErroredAttempts != 1 {
		t.Fatalf("outcome = %+v, err = %v, backend calls = %d", outcome, err, len(backend.calls))
	}
}

// Stalls count over the run: a repair that stalls after the first pass
// already stalled once fails the run.
func TestGenerateCountsStallsOverTheRunRepairsIncluded(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.ToolTimeout = 100 * time.Millisecond
	backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
		switch n {
		case 0, 2:
			return hang(true)(ctx, request, n)
		default:
			return Result{Document: []byte("not JSON"), Calls: 1, Tokens: 10}, nil
		}
	}
	outcome, _, err := generator.Generate(context.Background(), run)
	if err == nil || err.Error() != ReasonStalled || len(backend.calls) != 3 || outcome.ValidationRepairs != 1 {
		t.Fatalf("err = %v, outcome = %+v, backend calls = %d", err, outcome, len(backend.calls))
	}
}

// An attempt that finished keeps its result even when the watchdog finds it
// stalled as it returns.
func TestGenerateKeepsAFinishedAttemptTheWatchdogCaught(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.ToolTimeout = 50 * time.Millisecond
	backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
		request.Activity.ToolStarted("item_0", "command_execution")
		<-ctx.Done()
		return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
	}
	if _, document, err := generator.Generate(context.Background(), run); err != nil || len(document) == 0 || len(backend.calls) != 1 {
		t.Fatalf("err = %v, backend calls = %d", err, len(backend.calls))
	}
}

// modelServer answers each request after holding it silent for silence, then
// streams chunks every interval.
func modelServer(t *testing.T, silence time.Duration, chunks int, interval time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(silence)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Path", r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("X-Authorization", r.Header.Get("Authorization"))
		for i := range chunks {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(interval)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// callModel is a backend attempt that makes one request to its endpoint, then
// returns the fixture document.
func callModel(t *testing.T) func(context.Context, Request, int) (Result, error) {
	return func(ctx context.Context, request Request, _ int) (Result, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, request.Endpoint.BaseURL+"/responses", strings.NewReader("{}"))
		if err != nil {
			return Result{}, err
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = response.Body.Close() }()
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			return Result{}, err
		}
		return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
	}
}

func TestGenerateIdleStallNeedsNothingInFlight(t *testing.T) {
	t.Run("an idle attempt stalls", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		run.Endpoint = &Endpoint{BaseURL: modelServer(t, 0, 1, 0).URL + "/openai", Key: "token"}
		run.Budgets.StallTimeout = 150 * time.Millisecond
		backend.run = hang(false)
		outcome, _, err := generator.Generate(context.Background(), run)
		if err == nil || err.Error() != ReasonStalled || len(backend.calls) != 2 {
			t.Fatalf("err = %v, backend calls = %d", err, len(backend.calls))
		}
		if event := outcome.Diagnostics[0]; event.Kind != "stalled" || event.Status != "idle" {
			t.Fatalf("diagnostics = %+v", outcome.Diagnostics)
		}
	})
	t.Run("a long model request does not", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		// Silent for twice the stall timeout, then streaming for longer
		// than it: an open model request is never idle.
		run.Endpoint = &Endpoint{BaseURL: modelServer(t, 300*time.Millisecond, 6, 60*time.Millisecond).URL + "/openai", Key: "token"}
		run.Budgets.StallTimeout = 150 * time.Millisecond
		run.Budgets.ToolTimeout = 150 * time.Millisecond
		backend.run = callModel(t)
		outcome, document, err := generator.Generate(context.Background(), run)
		if err != nil || len(document) == 0 || len(backend.calls) != 1 || outcome.ErroredAttempts != 0 {
			t.Fatalf("outcome = %+v, err = %v, backend calls = %d", outcome, err, len(backend.calls))
		}
	})
	for name, endpoint := range map[string]*Endpoint{"without an endpoint": nil, "with an endpoint without a base URL": {Key: "token"}} {
		t.Run(name+" only the tool timeout applies", func(t *testing.T) {
			generator, run, backend := loopFixture(t, nil)
			run.Endpoint = endpoint
			run.Budgets.StallTimeout = 50 * time.Millisecond
			run.Budgets.ToolTimeout = 100 * time.Millisecond
			backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
				if n == 0 {
					// Idle for four stall timeouts: not a stall.
					time.Sleep(200 * time.Millisecond)
					return Result{Document: []byte("not JSON"), Calls: 1, Tokens: 10}, nil
				}
				return hang(true)(ctx, request, n)
			}
			outcome, _, err := generator.Generate(context.Background(), run)
			if err == nil || err.Error() != ReasonStalled || len(backend.calls) != 3 {
				t.Fatalf("err = %v, backend calls = %d", err, len(backend.calls))
			}
			for _, event := range outcome.Diagnostics {
				if event.Kind == "stalled" && event.Status != "tool_timeout" {
					t.Fatalf("diagnostics = %+v", outcome.Diagnostics)
				}
			}
		})
	}
	t.Run("zero timeouts disable the watchdog", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		run.Endpoint = &Endpoint{BaseURL: "http://model.example/openai", Key: "token"}
		backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
			if request.Activity != nil || request.Endpoint.BaseURL != "http://model.example/openai" {
				t.Errorf("request = %+v", request)
			}
			return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
		}
		if _, _, err := generator.Generate(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGenerateCancellationIsNotAStall(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.ToolTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	backend.run = func(attempt context.Context, request Request, n int) (Result, error) {
		cancel()
		return hang(true)(attempt, request, n)
	}
	if _, _, err := generator.Generate(ctx, run); !errors.Is(err, context.Canceled) || len(backend.calls) != 1 {
		t.Fatalf("err = %v, backend calls = %d", err, len(backend.calls))
	}
}

func TestObservedEndpointForwardsStreamsAndReportsRequests(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Path", r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("X-Authorization", r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, "data: 0\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = fmt.Fprint(w, "data: 1\n\n")
	}))
	defer server.Close()
	activity := NewActivity()
	observed, stop, err := observeEndpoint(&Endpoint{BaseURL: server.URL + "/openai/", Key: "token"}, activity)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(observed.BaseURL, "http://127.0.0.1:") || !strings.HasSuffix(observed.BaseURL, "/openai") || observed.Key != "token" {
		t.Fatalf("observed endpoint = %+v", observed)
	}
	request, _ := http.NewRequest(http.MethodPost, observed.BaseURL+"/responses?beta=true", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	// The first event arrives while the provider still holds the rest.
	first := make([]byte, len("data: 0\n\n"))
	_, readErr := io.ReadFull(response.Body, first)
	activity.mu.Lock()
	open := activity.requests
	activity.mu.Unlock()
	stall := activity.stalled(time.Now().Add(time.Hour), time.Millisecond, 0)
	close(release)
	if readErr != nil || string(first) != "data: 0\n\n" {
		t.Fatalf("first event %q, %v: the response is not streamed", first, readErr)
	}
	if open != 1 || stall != nil {
		t.Fatalf("open requests = %d, stall = %+v, want one open request and no stall", open, stall)
	}
	rest, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(rest) != "data: 1\n\n" || response.Header.Get("X-Path") != "/openai/responses?beta=true" || response.Header.Get("X-Authorization") != "Bearer token" {
		t.Fatalf("response %d %q headers %v", response.StatusCode, rest, response.Header)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		activity.mu.Lock()
		open = activity.requests
		activity.mu.Unlock()
		if open == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests still open", open)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestObservedEndpointHidesTransportErrors(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	observed, stop, err := observeEndpoint(&Endpoint{BaseURL: address + "/openai", Key: "token"}, NewActivity())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	response, err := http.Post(observed.BaseURL+"/responses", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || len(body) != 0 {
		t.Fatalf("status %d body %q", response.StatusCode, body)
	}
}

func TestActivityStallChecks(t *testing.T) {
	now := time.Now()
	activity := NewActivity()
	if stall := activity.stalled(now.Add(time.Hour), 0, 0); stall != nil {
		t.Fatalf("zero timeouts stalled: %+v", stall)
	}
	if stall := activity.stalled(now.Add(time.Minute), 30*time.Second, 0); stall == nil || stall.cause != "idle" {
		t.Fatalf("idle attempt: %+v", stall)
	}
	activity.ToolStarted("item_1", "command_execution")
	if stall := activity.stalled(now.Add(time.Minute), 30*time.Second, 0); stall != nil {
		t.Fatalf("a running tool is idle: %+v", stall)
	}
	if stall := activity.stalled(now.Add(time.Minute), 30*time.Second, 45*time.Second); stall == nil || stall.cause != "tool_timeout" || stall.tool != "command_execution" || stall.item != "item_1" {
		t.Fatalf("a tool past its timeout: %+v", stall)
	}
	activity.ToolFinished("item_1")
	activity.ToolFinished("unknown")
	if stall := activity.stalled(time.Now(), 30*time.Second, 45*time.Second); stall != nil {
		t.Fatalf("a finished tool stalled: %+v", stall)
	}
	if stall := activity.stalled(time.Now().Add(time.Minute), 30*time.Second, 0); stall == nil || stall.tool != "command_execution" || stall.item != "item_1" {
		t.Fatalf("an idle stall does not name the last tool: %+v", stall)
	}
	var none *Activity
	none.Progress()
	none.ToolStarted("item_1", "command_execution")
	none.ToolFinished("item_1")
}
