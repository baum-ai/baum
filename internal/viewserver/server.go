// Package viewserver serves the embedded review viewer and one review document
// on a loopback address.
//
// The server only ever serves the embedded app and the single document it was
// given; request paths never reach the file system. It never fetches anything.
// Host checking, security headers and routing are fixed here; a host such as a
// later "baum ui" extends the server through Mux and Use.
package viewserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lastpersonlabs/baum/internal/viewer"
)

// MaxDocumentBytes is the largest document the server accepts, at start-up and
// when a watched file changes. Reviews are far smaller; the cap keeps a wrong
// file from filling memory.
const MaxDocumentBytes = 32 << 20

const (
	defaultPollInterval = 500 * time.Millisecond
	defaultPingInterval = 20 * time.Second
	shutdownGrace       = 3 * time.Second
)

// contentSecurityPolicy lets the page load only same-origin resources and
// blocks framing, plugins and form posts. The built app has no inline code.
const contentSecurityPolicy = "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

// Options configures a Server.
type Options struct {
	// Path is the document file. It is read at New; with Watch it is polled
	// for changes. Exactly one of Path and Document must be set.
	Path string
	// Document is a fixed document, such as the sample. It cannot be watched.
	Document []byte
	// Watch reloads the viewer when the file at Path changes.
	Watch bool
	// Validate returns the findings for a document. They are printed to Warn
	// each time a document is loaded; the document is served either way.
	Validate func(document []byte) []string
	// Warn receives warnings. It defaults to discarding them.
	Warn io.Writer
	// Assets is the app to serve, with index.html at its root. It defaults to
	// the embedded viewer.
	Assets fs.FS
	// PollInterval is how often a watched file is checked; it defaults to
	// 500ms. PingInterval is how often an idle event stream sends a comment
	// line; it defaults to 20s.
	PollInterval time.Duration
	PingInterval time.Duration
}

// Server serves the viewer and a document. Create it with New, call Listen,
// then Serve.
type Server struct {
	opts   Options
	mux    *http.ServeMux
	chain  []func(http.Handler) http.Handler
	assets fs.FS

	mu       sync.RWMutex
	document []byte
	hub      hub

	listener net.Listener
	allowed  map[string]bool
	done     chan struct{}
	stop     sync.Once
}

// New loads the document and returns a server that is not yet listening.
func New(opts Options) (*Server, error) {
	if (opts.Path == "") == (opts.Document == nil) {
		return nil, errors.New("viewserver: set exactly one of Path and Document")
	}
	if opts.Watch && opts.Path == "" {
		return nil, errors.New("viewserver: only a document file can be watched")
	}
	if opts.Warn == nil {
		opts.Warn = io.Discard
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInterval
	}
	s := &Server{opts: opts, assets: opts.Assets, done: make(chan struct{})}
	if s.assets == nil {
		s.assets = viewer.Assets()
	}
	s.hub.subscribers = map[chan struct{}]struct{}{}

	document := opts.Document
	if opts.Path != "" {
		loaded, err := LoadDocument(opts.Path)
		if err != nil {
			return nil, err
		}
		document = loaded
	} else if !isJSON(document) {
		return nil, errors.New("viewserver: the document is not valid JSON")
	}
	s.document = document
	s.warnFindings(document)

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /api/document", s.handleDocument)
	s.mux.HandleFunc("GET /api/health", handleHealth)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	s.mux.HandleFunc("GET /", s.handleAsset)
	return s, nil
}

// Mux returns the router, so a host can add routes before Serve. Its routes sit
// behind the host check and the security headers.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Use adds middleware that wraps the router, inside the host check, for
// example authentication. The first one added is the outermost.
func (s *Server) Use(middleware func(http.Handler) http.Handler) {
	s.chain = append(s.chain, middleware)
}

// Listen binds 127.0.0.1 on port, or on a free port when port is 0, and returns
// the viewer's URL.
func (s *Server) Listen(port int) (string, error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	s.listener = listener
	actual := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	s.allowed = map[string]bool{"127.0.0.1:" + actual: true, "localhost:" + actual: true}
	return "http://127.0.0.1:" + actual + "/", nil
}

// Serve serves until ctx is cancelled, then shuts down gracefully: event
// streams end at once and requests in flight get a short grace period. It
// returns nil after a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("viewserver: Serve before Listen")
	}
	var handler http.Handler = s.mux
	for i := len(s.chain) - 1; i >= 0; i-- {
		handler = s.chain[i](handler)
	}
	srv := &http.Server{
		Handler:           s.secure(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if s.opts.Watch {
		go s.watch(ctx)
	}
	go func() {
		<-ctx.Done()
		s.stop.Do(func() { close(s.done) })
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
		}
	}()
	err := srv.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// secure rejects requests whose Host is not the bound loopback address, which
// stops DNS rebinding, and adds the security headers to every response. No CORS
// headers are ever sent.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !s.allowed[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) current() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.document
}

func (s *Server) handleDocument(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	document := s.current()
	h.Set("Content-Length", strconv.Itoa(len(document)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(document)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
}

// handleEvents is always a valid event stream, so the app's EventSource stays
// connected without --watch; it then only carries pings.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	changed, cancel := s.hub.subscribe()
	defer cancel()
	ping := time.NewTicker(s.opts.PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-changed:
			_, _ = io.WriteString(w, "event: document\ndata: {}\n\n")
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
		}
		flusher.Flush()
	}
}

// handleAsset serves an embedded file, or index.html for a path that names no
// file, so the app's own routes work as deep links. The request path is only
// ever looked up in the embedded file system.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if info, err := fs.Stat(s.assets, name); err != nil || info.IsDir() {
		// A missing hashed asset is an error, not an app route.
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	content, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	if name == "index.html" {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, bytes.NewReader(content))
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	}
	if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
		return kind
	}
	return "application/octet-stream"
}

// setDocument swaps the served bytes and tells every open event stream.
func (s *Server) setDocument(document []byte) {
	s.mu.Lock()
	s.document = document
	s.mu.Unlock()
	s.hub.broadcast()
}

func (s *Server) warnFindings(document []byte) {
	if s.opts.Validate == nil {
		return
	}
	findings := s.opts.Validate(document)
	if len(findings) == 0 {
		return
	}
	_, _ = fmt.Fprintf(s.opts.Warn, "warning: the document has %d finding(s); serving it anyway\n", len(findings))
	for _, finding := range findings {
		_, _ = fmt.Fprintln(s.opts.Warn, finding)
	}
}

// hub fans a "document changed" signal out to the open event streams. A
// subscriber that has not yet seen the last signal is not signalled twice, so a
// slow stream never blocks the watcher.
type hub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func (h *hub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers, ch)
		h.mu.Unlock()
	}
}

func (h *hub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
