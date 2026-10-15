// Package uiserver hosts the authenticated local application. Providers own
// their routes; every provider runs inside the same Host, auth and CSRF checks.
package uiserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/localapp"
)

type Options struct {
	// API serves application routes after authentication and CSRF validation.
	API http.Handler
}

type Server struct {
	opts                    Options
	listener                net.Listener
	origin, launch, session string
	mu                      sync.Mutex
	sequence                uint64
	events                  []event
	subscribers             map[chan struct{}]struct{}
	done                    chan struct{}
}
type event struct {
	ID   uint64
	Kind string
	Data json.RawMessage
}

func secret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func New(opts Options) (*Server, error) {
	launch, err := secret()
	if err != nil {
		return nil, err
	}
	session, err := secret()
	if err != nil {
		return nil, err
	}
	return &Server{opts: opts, launch: launch, session: session, subscribers: map[chan struct{}]struct{}{}, done: make(chan struct{})}, nil
}
func (s *Server) Listen(port int) (string, error) {
	if port < 0 || port > 65535 {
		return "", errors.New("port must be between 0 and 65535")
	}
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	s.listener = l
	s.origin = "http://" + l.Addr().String()
	return s.LaunchURL(), nil
}

// LaunchURL is a private capability, suitable for the browser fragment or a
// mode-0600 instance file. It must never be logged in diagnostics.
func (s *Server) LaunchURL() string { return s.origin + "/#token=" + s.launch }
func (s *Server) Origin() string    { return s.origin }
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("ui server: Listen required")
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		close(s.done)
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if srv.Shutdown(shutdown) != nil {
			_ = srv.Close()
		}
	}()
	err := srv.Serve(s.listener)
	cancel()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func same(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func (s *Server) authorized(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && same(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.launch) {
		return true
	}
	c, err := r.Cookie("baum_ui")
	return err == nil && same(c.Value, s.session)
}
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if s.origin == "" || r.Host != strings.TrimPrefix(s.origin, "http://") {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.Header.Get("Origin") != s.origin || err != nil || kind != "application/json" {
				http.Error(w, "same-origin JSON request required", http.StatusForbidden)
				return
			}
		}
		if (r.Method == "GET" || r.Method == "HEAD") && !strings.HasPrefix(r.URL.Path, "/api/") {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			if !fs.ValidPath(path) {
				http.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(localapp.Assets(), path); err != nil {
				http.NotFound(w, r)
				return
			}
			http.FileServer(http.FS(localapp.Assets())).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/auth/exchange" && r.Method == "POST" {
			r.Body = http.MaxBytesReader(w, r.Body, 1024)
			var input struct {
				Token string `json:"token"`
			}
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !same(input.Token, s.launch) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "baum_ui", Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/health":
			if r.Method == "GET" || r.Method == "HEAD" {
				writeJSON(w, map[string]string{"status": "ok"})
				return
			}
		case "/api/events":
			if r.Method == "GET" {
				s.stream(w, r)
				return
			}
		}
		if s.opts.API == nil {
			http.NotFound(w, r)
			return
		}
		s.opts.API.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Publish records a small notification, not sensitive documents. Clients that
// miss the bounded replay window receive a reset event and rebuild their index.
func (s *Server) Publish(kind string, data any) error {
	if kind != "index" && kind != "progress" {
		return errors.New("unknown event kind")
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(b) > 16<<10 {
		return errors.New("event too large")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	s.events = append(s.events, event{s.sequence, kind, b})
	if len(s.events) > 128 {
		s.events = append([]event(nil), s.events[len(s.events)-128:]...)
	}
	for ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	var after uint64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		var err error
		after, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid event cursor", http.StatusBadRequest)
			return
		}
	}
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(w, ": connected\n\n")
	f.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		s.mu.Lock()
		events := append([]event(nil), s.events...)
		latest := s.sequence
		s.mu.Unlock()
		if after > latest || (len(events) > 0 && after+1 < events[0].ID) {
			_, _ = fmt.Fprintf(w, "id: %d\nevent: reset\ndata: {}\n\n", latest)
			after = latest
		}
		for _, e := range events {
			if e.ID > after {
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, e.Data); err != nil {
					return
				}
				after = e.ID
			}
		}
		f.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-ch:
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			f.Flush()
		}
	}
}
