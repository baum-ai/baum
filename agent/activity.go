package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxStallRetries is how many stalled attempts a run retries before it fails
// with ReasonStalled. A stall is a hang, not a property of the input, so one
// immediate retry usually completes the run.
const maxStallRetries = 1

// Activity is what an attempt has been doing, as the stall watchdog sees it:
// when it last made progress, the model requests it has open and the tools it
// is running. Backends report to the Activity of their Request; a nil
// *Activity ignores every report.
type Activity struct {
	mu       sync.Mutex
	last     time.Time
	requests int
	tools    map[string]runningTool
	lastTool runningTool
}

type runningTool struct {
	id, kind string
	started  time.Time
}

func NewActivity() *Activity {
	return &Activity{last: time.Now(), tools: map[string]runningTool{}}
}

// Progress records that the attempt did something, such as an event the
// harness emitted.
func (a *Activity) Progress() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

// ToolStarted records a tool the harness started, by its own item ID and kind
// (for example command_execution). A running tool is never idle, but it is
// bounded by the tool timeout.
func (a *Activity) ToolStarted(id, kind string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	now := time.Now()
	a.last = now
	a.tools[id] = runningTool{id: id, kind: kind, started: now}
	a.lastTool = runningTool{id: id, kind: kind, started: now}
	a.mu.Unlock()
}

// ToolFinished records that the tool with this ID ended; an unknown ID is ignored.
func (a *Activity) ToolFinished(id string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.last = time.Now()
	delete(a.tools, id)
	a.mu.Unlock()
}

// Running is the number of tools the attempt is running.
func (a *Activity) Running() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tools)
}

func (a *Activity) requestStarted() {
	a.mu.Lock()
	a.last = time.Now()
	a.requests++
	a.mu.Unlock()
}

func (a *Activity) requestFinished() {
	a.mu.Lock()
	a.last = time.Now()
	a.requests--
	a.mu.Unlock()
}

// stallError ends an attempt the watchdog found stalled. Its text is the
// stored reason; the cause and tool are for logs and diagnostics.
type stallError struct {
	// cause is "tool_timeout" (a tool ran past the tool timeout) or "idle"
	// (nothing in flight and no progress for the stall timeout).
	cause      string
	tool, item string
	elapsed    time.Duration
}

func (e *stallError) Error() string { return ReasonStalled }

// stalled reports why the attempt has stalled, or nil. A tool running longer
// than toolTimeout stalls it; so does an attempt with no open model request
// and no running tool that made no progress for idle. A zero duration
// disables its check.
func (a *Activity) stalled(now time.Time, idle, toolTimeout time.Duration) *stallError {
	a.mu.Lock()
	defer a.mu.Unlock()
	if toolTimeout > 0 {
		for _, tool := range a.tools {
			if elapsed := now.Sub(tool.started); elapsed > toolTimeout {
				return &stallError{cause: "tool_timeout", tool: tool.kind, item: tool.id, elapsed: elapsed}
			}
		}
	}
	if idle > 0 && a.requests == 0 && len(a.tools) == 0 {
		if elapsed := now.Sub(a.last); elapsed > idle {
			return &stallError{cause: "idle", tool: a.lastTool.kind, item: a.lastTool.id, elapsed: elapsed}
		}
	}
	return nil
}

// watch cancels the attempt with a stallError once it stalls, checking a few
// times per timeout, until ctx ends.
func (a *Activity) watch(ctx context.Context, idle, toolTimeout time.Duration, cancel context.CancelCauseFunc) {
	interval := time.Second
	for _, timeout := range []time.Duration{idle, toolTimeout} {
		if timeout > 0 {
			interval = min(interval, timeout/4)
		}
	}
	ticker := time.NewTicker(max(interval, time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if stall := a.stalled(now, idle, toolTimeout); stall != nil {
				cancel(stall)
				return
			}
		}
	}
}

// runWatched runs one backend attempt under the stall watchdog of the run's
// budgets. With an endpoint, the backend reaches it through a loopback
// forwarder that reports each model request and its streamed response, so a
// long model call is never mistaken for idleness. Without one the loop cannot
// see the backend's model calls, so only the tool timeout applies.
func (g *Generator) runWatched(ctx context.Context, budgets Budgets, request Request) (Result, error) {
	idle, toolTimeout := budgets.StallTimeout, budgets.ToolTimeout
	if request.Endpoint == nil || request.Endpoint.BaseURL == "" {
		idle = 0
	}
	if idle <= 0 && toolTimeout <= 0 {
		return g.Backend.Run(ctx, request)
	}
	activity := NewActivity()
	request.Activity = activity
	if idle > 0 {
		observed, stop, err := observeEndpoint(request.Endpoint, activity)
		if err != nil {
			return Result{}, err
		}
		defer stop()
		request.Endpoint = observed
	}
	attempt, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		activity.watch(attempt, idle, toolTimeout, cancel)
	}()
	result, err := g.Backend.Run(attempt, request)
	cancel(nil)
	<-watching
	stall, ok := context.Cause(attempt).(*stallError)
	// An attempt that finished keeps its result, even if the watchdog fired
	// as it did.
	if err == nil || !ok || ctx.Err() != nil {
		return result, err
	}
	if request.Logger != nil {
		request.Logger.WarnContext(ctx, "review agent stalled", "cause", stall.cause, "tool", stall.tool, "item", stall.item,
			"elapsed", stall.elapsed.Round(time.Second), "stall_timeout", idle, "tool_timeout", toolTimeout)
	}
	result.Events = append(result.Events, Event{Kind: "stalled", Status: stall.cause, Tool: stall.tool, ElapsedMS: stall.elapsed.Milliseconds()})
	return result, stall
}

// observeEndpoint starts a loopback forwarder to endpoint and returns the
// endpoint that reaches the same provider through it, and the function that
// stops it. The forwarder passes requests and responses through unchanged,
// streaming, and reports to activity that a request is open from its arrival
// until its response is copied. It connects directly, as the harness did: no
// proxy from the environment sees the run's key.
func observeEndpoint(endpoint *Endpoint, activity *Activity) (*Endpoint, func(), error) {
	target, err := url.Parse(endpoint.BaseURL)
	if err != nil || target.Host == "" {
		return nil, nil, fmt.Errorf("observing the model endpoint: invalid base URL")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("observing the model endpoint: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host, r.Out.Host = target.Scheme, target.Host, target.Host
		},
		Transport: transport,
		// Server-sent events must reach the harness as they arrive.
		FlushInterval: -1,
		// Never log or relay the transport error: it can name the URL.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) },
		ErrorLog:     slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			activity.requestStarted()
			defer activity.requestFinished()
			proxy.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	}
	go func() { _ = server.Serve(listener) }()
	observed := &Endpoint{BaseURL: "http://" + listener.Addr().String() + strings.TrimSuffix(target.Path, "/"), Key: endpoint.Key}
	return observed, func() {
		_ = server.Close()
		transport.CloseIdleConnections()
	}, nil
}
