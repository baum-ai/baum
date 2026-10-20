// Package uiserver hosts the authenticated local application. Providers own
// their routes; every provider runs inside the same Host, auth and CSRF checks.
//
// Browsers reach the application only under a secret path namespace,
// /s/<namespace>/, and the session cookie is scoped to that path. Cookies are
// shared by every port on 127.0.0.1, so a host-wide cookie would also reach
// other loopback services; a path they cannot guess keeps it from them.
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
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	// API serves application routes after authentication and CSRF validation.
	API http.Handler
	// App serves the browser application's files (GET and HEAD outside /api/)
	// under the session namespace, after authentication, with the namespace
	// prefix stripped. Nil serves a minimal status page.
	App http.Handler
}

type Server struct {
	opts     Options
	listener net.Listener
	// launch is the private launch capability, session the cookie value and
	// namespace the secret segment of /s/<namespace>/. All three are random and
	// independent, and none may be logged.
	origin, launch, session, namespace string
	mu                                 sync.Mutex
	sequence                           uint64
	events                             []event
	subscribers                        map[chan struct{}]struct{}
	done                               chan struct{}
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
	namespace, err := secret()
	if err != nil {
		return nil, err
	}
	return &Server{opts: opts, launch: launch, session: session, namespace: namespace, subscribers: map[chan struct{}]struct{}{}, done: make(chan struct{})}, nil
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

const sessionCookie = "baum_ui"

func (s *Server) bearer(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && same(token, s.launch)
}
func (s *Server) cookie(r *http.Request) bool {
	// Another loopback service can set a cookie of the same name on a broader
	// path; any matching value authorizes, so it cannot shadow the session.
	for _, c := range r.CookiesNamed(sessionCookie) {
		if same(c.Value, s.session) {
			return true
		}
	}
	return false
}

// appPath is the secret namespace the browser application lives under.
func (s *Server) appPath() string { return "/s/" + s.namespace + "/" }

// scoped strips a correct /s/<namespace> prefix from path.
func (s *Server) scoped(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/s/")
	if !ok {
		return "", false
	}
	namespace, rest, ok := strings.Cut(rest, "/")
	if !ok || !same(namespace, s.namespace) {
		return "", false
	}
	return "/" + rest, true
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
		if r.URL.Path == "/" && (r.Method == "GET" || r.Method == "HEAD") {
			h.Set("Content-Type", "text/html; charset=utf-8")
			if r.Method != "HEAD" {
				_, _ = io.WriteString(w, bootstrapHTML)
			}
			return
		}
		if r.URL.Path == "/launch.js" && (r.Method == "GET" || r.Method == "HEAD") {
			h.Set("Content-Type", "text/javascript; charset=utf-8")
			if r.Method != "HEAD" {
				_, _ = io.WriteString(w, bootstrapJS)
			}
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
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.session, Path: s.appPath(), HttpOnly: true, SameSite: http.SameSiteStrictMode})
			writeJSON(w, map[string]string{"path": s.appPath()})
			return
		}
		path, inApp := s.scoped(r.URL.Path)
		switch {
		case inApp:
			if !s.cookie(r) && !s.bearer(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		case strings.HasPrefix(r.URL.Path, "/s/"):
			http.NotFound(w, r)
			return
		case strings.HasPrefix(r.URL.Path, "/api/") && s.bearer(r):
			// Browsers never send the launch capability ambiently, so API
			// clients holding it need no namespace. Cookies are ignored here.
			path = r.URL.Path
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r = r.Clone(r.Context())
		// Providers never see the namespace, so they cannot echo or log it.
		r.URL.Path, r.URL.RawPath = path, ""
		r.RequestURI = r.URL.RequestURI()
		switch path {
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
		if strings.HasPrefix(path, "/api/") {
			if s.opts.API == nil {
				http.NotFound(w, r)
				return
			}
			s.opts.API.ServeHTTP(w, r)
			return
		}
		if !inApp || (r.Method != "GET" && r.Method != "HEAD") {
			http.NotFound(w, r)
			return
		}
		if s.opts.App != nil {
			s.opts.App.ServeHTTP(w, r)
			return
		}
		s.defaultApp(w, r)
	})
}

// defaultApp is the status page served when no application is installed.
func (s *Server) defaultApp(w http.ResponseWriter, r *http.Request) {
	var body, kind string
	switch r.URL.Path {
	case "/":
		body, kind = appHTML, "text/html; charset=utf-8"
	case "/app.js":
		body, kind = appJS, "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	if r.Method != "HEAD" {
		_, _ = io.WriteString(w, body)
	}
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Publish records a small notification, not sensitive documents. Clients that
// miss the bounded replay window receive a reset event and rebuild their index.
func (s *Server) Publish(kind string, data any) error {
	if kind != "index" && kind != "progress" && kind != "automatic" && kind != "live" {
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

const bootstrapHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>baum local</title><script src="/launch.js" defer></script></head><body><h1>baum local</h1><p id="status">Connecting to the local application…</p></body></html>`

// The bootstrap exchanges the fragment capability, then replaces itself with
// the application at the namespace path the exchange returns. A `page` of
// "diagnostics" beside the token (the desktop app's first run) opens that
// page; no other value is carried over.
const bootstrapJS = `(async()=>{const params=new URLSearchParams(location.hash.slice(1));const token=params.get('token');const page=params.get('page')==='diagnostics'?'?page=diagnostics':'';history.replaceState(null,'',location.pathname);const status=document.getElementById('status');try{if(!token)throw Error('Open this page from the baum desktop app.');const response=await fetch('/api/auth/exchange',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token})});if(!response.ok)throw Error('Launch authorization failed.');const {path}=await response.json();if(typeof path!=='string'||!path.startsWith('/s/'))throw Error('Launch authorization failed.');location.replace(path+page);}catch(error){status.textContent=error.message;}})();`

// The default application page resolves API paths relative to its namespace.
const appHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>baum local</title><script src="app.js" defer></script></head><body><h1>baum local</h1><p id="status">Connecting to the local application…</p></body></html>`
const appJS = `(async()=>{const status=document.getElementById('status');try{const response=await fetch('api/repos');if(!response.ok)throw Error('Open this page from the baum desktop app.');const repos=await response.json();status.textContent='Local API ready. '+repos.length+' registered clone(s).';}catch(error){status.textContent=error.message;}})();`
