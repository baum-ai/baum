package uiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"path": r.URL.Path}) })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s
}
func request(t *testing.T, s *Server, method, path, host, origin, kind, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, s.Origin()+path, strings.NewReader(body))
	if host != "" {
		r.Host = host
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if kind != "" {
		r.Header.Set("Content-Type", kind)
	}
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSecurityBoundary(t *testing.T) {
	s := testServer(t)
	for _, tt := range []struct {
		name, method, path, host, origin, kind, auth, body string
		status                                             int
	}{
		{name: "bootstrap", method: "GET", path: "/", status: 200},
		{name: "script", method: "GET", path: "/launch.js", status: 200},
		{name: "no token", method: "GET", path: "/api/repos", status: 401},
		{name: "health private", method: "GET", path: "/api/health", status: 401},
		{name: "wrong token", method: "GET", path: "/api/repos", auth: "Bearer wrong", status: 401},
		{name: "bearer", method: "GET", path: "/api/repos", auth: "Bearer " + s.launch, status: 200},
		{name: "wrong host", method: "GET", path: "/", host: "attacker.example", auth: "Bearer " + s.launch, status: 403},
		{name: "localhost alias rejected", method: "GET", path: "/api/repos", host: strings.Replace(strings.TrimPrefix(s.origin, "http://"), "127.0.0.1", "localhost", 1), auth: "Bearer " + s.launch, status: 403},
		{name: "rebinding", method: "POST", path: "/api/repos", host: "127.0.0.1.attacker.example", origin: s.origin, kind: "application/json", auth: "Bearer " + s.launch, status: 403},
		{name: "cross origin", method: "POST", path: "/api/repos", origin: "https://attacker.example", kind: "application/json", auth: "Bearer " + s.launch, status: 403},
		{name: "missing origin", method: "POST", path: "/api/repos", kind: "application/json", auth: "Bearer " + s.launch, status: 403},
		{name: "simple form", method: "POST", path: "/api/repos", origin: s.origin, kind: "text/plain", auth: "Bearer " + s.launch, status: 403},
		{name: "origin null", method: "POST", path: "/api/repos", origin: "null", kind: "application/json", auth: "Bearer " + s.launch, status: 403},
		{name: "valid mutation", method: "POST", path: "/api/repos", origin: s.origin, kind: "application/json; charset=utf-8", auth: "Bearer " + s.launch, status: 200},
		{name: "cross origin exchange", method: "POST", path: "/api/auth/exchange", origin: "https://evil.example", kind: "application/json", body: fmt.Sprintf(`{"token":%q}`, s.launch), status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := request(t, s, tt.method, tt.path, tt.host, tt.origin, tt.kind, tt.auth, tt.body)
			if w.Code != tt.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("CORS enabled")
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing nosniff")
			}
			if strings.Contains(w.Body.String(), s.launch) || strings.Contains(w.Body.String(), s.session) || strings.Contains(w.Body.String(), s.namespace) {
				t.Fatal("capability leaked")
			}
		})
	}
}
func TestFragmentExchangeAndCookie(t *testing.T) {
	s := testServer(t)
	u, err := url.Parse(s.LaunchURL())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.RawQuery, s.launch) || u.Fragment != "token="+s.launch {
		t.Fatal("token must remain in fragment")
	}
	w := request(t, s, "POST", "/api/auth/exchange", "", s.origin, "application/json", "", fmt.Sprintf(`{"token":%q}`, s.launch))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var exchanged struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &exchanged); err != nil || exchanged.Path != "/s/"+s.namespace+"/" {
		t.Fatalf("exchange path %q: %v", exchanged.Path, err)
	}
	if s.namespace == s.session || s.namespace == s.launch || len(s.namespace) != 43 {
		t.Fatal("namespace must be an independent capability")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != exchanged.Path {
		t.Fatalf("insecure cookie %+v", cookies)
	}
	get := func(path string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", s.origin+path, nil)
		if c != nil {
			r.AddCookie(c)
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, r)
		return response
	}
	if response := get(exchanged.Path+"api/repos", cookies[0]); response.Code != 200 || !strings.Contains(response.Body.String(), `"path":"/api/repos"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := get(exchanged.Path+"api/health", cookies[0]); response.Code != 200 {
		t.Fatal(response.Code)
	}
	for _, path := range []string{"", "app.js"} {
		if response := get(exchanged.Path+path, cookies[0]); response.Code != 200 {
			t.Fatal(path, response.Code)
		}
	}
	// The cookie is sent to every port on the host, so it alone must not
	// authorize anything outside the namespace.
	for path, status := range map[string]int{
		"/api/repos":                    401,
		"/api/health":                   401,
		"/api/events":                   401,
		"/s/wrong/api/repos":            404,
		"/s/" + s.namespace + "x/":      404,
		"/s/" + s.namespace:             404,
		"/s/" + s.launch + "/api/repos": 404,
		"/s/" + s.session + "/":         404,
		exchanged.Path + "missing":      404,
	} {
		if response := get(path, cookies[0]); response.Code != status {
			t.Fatal(path, response.Code)
		}
	}
	if response := get(exchanged.Path+"api/repos", nil); response.Code != 401 {
		t.Fatal(response.Code)
	}
	if response := get(exchanged.Path+"api/repos", &http.Cookie{Name: "baum_ui", Value: "wrong"}); response.Code != 401 {
		t.Fatal(response.Code)
	}
	r := httptest.NewRequest("GET", s.origin+exchanged.Path+"api/repos", nil)
	r.AddCookie(&http.Cookie{Name: "baum_ui", Value: "tossed"})
	r.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, r)
	if response.Code != 200 {
		t.Fatal("shadowing cookie blocked the session", response.Code)
	}
	if w := request(t, s, "POST", exchanged.Path+"api/repos", "", "https://evil.example", "application/json", "Bearer "+s.launch, "{}"); w.Code != 403 {
		t.Fatal("namespaced mutation skipped origin check", w.Code)
	}
	for _, body := range []string{`{"token":"bad"}`, fmt.Sprintf(`{"token":%q} {}`, s.launch), fmt.Sprintf(`{"token":%q,"extra":true}`, s.launch), strings.Repeat("x", 2048)} {
		if w := request(t, s, "POST", "/api/auth/exchange", "", s.origin, "application/json", "", body); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
func TestBrowserCookieStaysInNamespace(t *testing.T) {
	s := testServer(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	r, _ := http.NewRequest("POST", s.origin+"/api/auth/exchange", strings.NewReader(fmt.Sprintf(`{"token":%q}`, s.launch)))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var exchanged struct {
		Path string `json:"path"`
	}
	err = json.NewDecoder(resp.Body).Decode(&exchanged)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, err)
	}
	// Another loopback port on the same host must not receive the cookie.
	other, _ := url.Parse("http://127.0.0.1:1/")
	if len(jar.Cookies(other)) != 0 || len(jar.Cookies(&url.URL{Scheme: "http", Host: strings.TrimPrefix(s.origin, "http://"), Path: "/api/repos"})) != 0 {
		t.Fatal("session cookie escapes the namespace")
	}
	for path, status := range map[string]int{exchanged.Path: 200, exchanged.Path + "api/repos": 200, "/api/repos": 401} {
		resp, err := client.Get(s.origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatal(path, resp.StatusCode)
		}
	}
}
func TestAppHandlerUnderNamespace(t *testing.T) {
	s, err := New(Options{App: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "app "+r.URL.Path) })})
	if err != nil {
		t.Fatal(err)
	}
	s.origin = "http://127.0.0.1:1"
	cookie := "baum_ui=" + s.session
	for _, tt := range []struct {
		method, path, cookie, body string
		status                     int
	}{
		{method: "GET", path: s.appPath() + "assets/index.js", cookie: cookie, body: "app /assets/index.js", status: 200},
		{method: "GET", path: s.appPath(), cookie: cookie, body: "app /", status: 200},
		{method: "GET", path: s.appPath() + "assets/index.js", status: 401},
		{method: "GET", path: "/assets/index.js", cookie: cookie, status: 401},
		{method: "GET", path: s.appPath() + "api/unknown", cookie: cookie, status: 404},
	} {
		r := httptest.NewRequest(tt.method, s.origin+tt.path, nil)
		if tt.cookie != "" {
			r.Header.Set("Cookie", tt.cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tt.status || (tt.body != "" && w.Body.String() != tt.body) {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
}
func TestEventsReplayAndReset(t *testing.T) {
	s := testServer(t)
	for i := range 3 {
		if err := s.Publish("index", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Browsers stream under the namespace with the session cookie.
	r, _ := http.NewRequestWithContext(ctx, "GET", s.origin+s.appPath()+"api/events", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
	r.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	var got strings.Builder
	for !strings.Contains(got.String(), "id: 3\n") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(line)
	}
	if strings.Contains(got.String(), "id: 1\n") || !strings.Contains(got.String(), "id: 2\n") {
		t.Fatal(got.String())
	}
	cancel()
	for range 130 {
		_ = s.Publish("progress", struct{}{})
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	r, _ = http.NewRequestWithContext(ctx2, "GET", s.origin+"/api/events", nil)
	r.Header.Set("Authorization", "Bearer "+s.launch)
	r.Header.Set("Last-Event-ID", "1")
	resp, err = http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader = bufio.NewReader(resp.Body)
	got.Reset()
	for !strings.Contains(got.String(), "event: reset") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(line)
	}
}
func TestShutdownClosesEventStreams(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	r, _ := http.NewRequest("GET", s.origin+"/api/events", nil)
	r.Header.Set("Authorization", "Bearer "+s.launch)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
