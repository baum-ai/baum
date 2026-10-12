package viewserver

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lastpersonlabs/baum/internal/viewer"
	"github.com/lastpersonlabs/baum/schema"
)

const (
	fixture = "{\"schema\":\"baum.review/1\",\"title\":\"one\"}\n"
	updated = "{\"schema\":\"baum.review/1\",\"title\":\"two, rewritten\"}\n"
	wait    = 5 * time.Second
)

// syncBuffer collects warnings written by the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs a server on a free port until the test ends and returns its URL
// without the trailing slash.
func start(t *testing.T, opts Options) (*Server, string) {
	t.Helper()
	if opts.PollInterval == 0 {
		opts.PollInterval = 10 * time.Millisecond
	}
	server, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	url, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(wait):
			t.Error("Serve did not return after the context was cancelled")
		}
	})
	return server, strings.TrimSuffix(url, "/")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, fixture)
	return path
}

// noRedirects keeps a redirect visible instead of following it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := noRedirects.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func fetchDocument(t *testing.T, base string) string {
	t.Helper()
	resp, body := get(t, base+"/api/document")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/document: status %d", resp.StatusCode)
	}
	return body
}

// events reads an event stream line by line.
type events struct {
	lines chan string
	body  io.Closer
}

func openEvents(t *testing.T, base string) *events {
	t.Helper()
	resp, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("events Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("events Cache-Control = %q", got)
	}
	stream := &events{lines: make(chan string, 64), body: resp.Body}
	go func() {
		defer close(stream.lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			stream.lines <- scanner.Text()
		}
	}()
	t.Cleanup(func() { _ = resp.Body.Close() })
	return stream
}

// next waits for a line that satisfies match.
func (e *events) next(t *testing.T, match string) {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case line, ok := <-e.lines:
			if !ok {
				t.Fatalf("event stream ended before %q", match)
			}
			if line == match {
				return
			}
		case <-timeout:
			t.Fatalf("no %q on the event stream", match)
		}
	}
}

// nextEvent waits for the next event line, ignoring comments, data and the
// blank lines between events, and fails if that line is not want.
func (e *events) nextEvent(t *testing.T, want string) {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case line, ok := <-e.lines:
			if !ok {
				t.Fatalf("event stream ended before %q", want)
			}
			if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "data:") {
				continue
			}
			if line != want {
				t.Fatalf("event line %q, want %q", line, want)
			}
			return
		case <-timeout:
			t.Fatalf("no %q on the event stream", want)
		}
	}
}

// eventually polls /api/document until it equals want.
func eventually(t *testing.T, base, want string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		got := fetchDocument(t, base)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("document = %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServesDocumentBytesUnchanged(t *testing.T) {
	// Odd spacing and key order must survive: the bytes are served as read.
	odd := "{ \"title\" :  \"x\",\n\t\"schema\":\"baum.review/1\" }"
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, odd)
	_, base := start(t, Options{Path: path})

	resp, body := get(t, base+"/api/document")
	if body != odd {
		t.Errorf("body = %q, want %q", body, odd)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestSampleServesExampleAndIndex(t *testing.T) {
	_, base := start(t, Options{Document: schema.Example})
	if got := fetchDocument(t, base); got != string(schema.Example) {
		t.Error("/api/document is not schema.Example")
	}

	index, err := fs.ReadFile(viewer.Assets(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, base+"/")
	if resp.StatusCode != http.StatusOK || body != string(index) {
		t.Errorf("/ : status %d, index.html served = %v", resp.StatusCode, body == string(index))
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("/ Content-Type = %q", got)
	}
}

func TestDeepLinkFallsBackToIndexAndAssetsKeepTheirType(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	index, _ := fs.ReadFile(viewer.Assets(), "index.html")

	resp, body := get(t, base+"/foo/bar?pane=files")
	if resp.StatusCode != http.StatusOK || body != string(index) {
		t.Errorf("deep link: status %d, served index = %v", resp.StatusCode, body == string(index))
	}

	refs := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(string(index), -1)
	if len(refs) < 2 {
		t.Fatalf("index.html references %d assets", len(refs))
	}
	for _, ref := range refs {
		resp, body := get(t, base+ref[1])
		wantFile, err := fs.ReadFile(viewer.Assets(), strings.TrimPrefix(ref[1], "/"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || body != string(wantFile) {
			t.Errorf("%s: status %d, content matches = %v", ref[1], resp.StatusCode, body == string(wantFile))
		}
		wantType := "text/javascript; charset=utf-8"
		if strings.HasSuffix(ref[1], ".css") {
			wantType = "text/css; charset=utf-8"
		}
		if got := resp.Header.Get("Content-Type"); got != wantType {
			t.Errorf("%s Content-Type = %q, want %q", ref[1], got, wantType)
		}
	}

	if resp, _ := get(t, base+"/assets/missing-0000.js"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset: status %d, want 404", resp.StatusCode)
	}
}

func TestFontContentType(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	entries, err := fs.Glob(viewer.Assets(), "assets/*.woff2")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no embedded font: %v", err)
	}
	resp, _ := get(t, base+"/"+entries[0])
	if got := resp.Header.Get("Content-Type"); got != "font/woff2" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestHealth(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	resp, body := get(t, base+"/api/health")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("health: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestUnknownAPIIsNotFound(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	for _, p := range []string{"/api/nope", "/api/", "/api/document/extra"} {
		if resp, _ := get(t, base+p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestOnlyGetAndHeadAreAllowed(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	for _, p := range []string{"/api/document", "/api/events", "/api/health", "/api/sessions", "/", "/foo", "/api/nope"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			req, _ := http.NewRequest(method, base+p, strings.NewReader("{}"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", method, p, resp.StatusCode)
			}
			if !strings.Contains(resp.Header.Get("Allow"), "GET") {
				t.Errorf("%s %s: Allow = %q", method, p, resp.Header.Get("Allow"))
			}
		}
	}
	resp, err := http.Head(base + "/api/document")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD /api/document: status %d", resp.StatusCode)
	}
}

func TestBadHostIsRejected(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	port := base[strings.LastIndex(base, ":")+1:]
	for _, host := range []string{"evil.example", "evil.example:" + port, "127.0.0.1", "127.0.0.1:1", "localhost", "[::1]:" + port, "127.0.0.1.evil.example:" + port} {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/document", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "baum.review") {
			t.Errorf("Host %q: status %d, body %q", host, resp.StatusCode, body)
		}
	}
	for _, host := range []string{"localhost:" + port, "LOCALHOST:" + port, "127.0.0.1:" + port} {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/health", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, resp.StatusCode)
		}
	}
}

func TestTraversalNeverReachesDisk(t *testing.T) {
	// With a file system holding nothing but an index, and with the real
	// embedded app, whatever a request path says, the only content that can
	// come back is the index or a 404. The paths include files that exist
	// relative to this test's working directory, which a handler serving from
	// disk would return.
	index, err := fs.ReadFile(viewer.Assets(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		assets fs.FS
		index  string
	}{
		{"map", fstest.MapFS{
			"index.html":    {Data: []byte("<!doctype html>INDEX")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		}, "<!doctype html>INDEX"},
		{"embedded", nil, string(index)},
	}
	paths := []string{
		"/../../etc/passwd",
		"/%2e%2e/%2e%2e/etc/passwd",
		"/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/api/../go.mod",
		"/api/%2e%2e/go.mod",
		"/assets/../../go.mod",
		"/assets/..%2f..%2fgo.mod",
		"/%252e%252e/etc/passwd",
		"/..\\..\\etc\\passwd",
		"//etc/passwd",
		"/etc/passwd",
		"/go.mod",
		"/review.json",
		"/server.go",
		"/watch.go",
		"/server_test.go",
		"/./server.go",
		"/../viewserver/server.go",
		"/%2e%2e/viewserver/server.go",
		"/assets/../server.go",
		"/assets/..%2fserver.go",
		"/%2e/server.go",
		"/server.go%00",
		"/server.go/",
		"/api/../server.go",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, base := start(t, Options{Document: []byte(fixture), Assets: tt.assets})
			port := base[strings.LastIndex(base, ":")+1:]
			for _, p := range paths {
				// Send the path as written, without the client normalising it.
				conn, err := net.Dial("tcp", "127.0.0.1:"+port)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.WriteString(conn, "GET "+p+" HTTP/1.1\r\nHost: 127.0.0.1:"+port+"\r\nConnection: close\r\n\r\n")
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					_ = conn.Close()
					t.Fatalf("%s: %v", p, err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = conn.Close()
				text := string(body)
				for _, leak := range []string{"root:", "module github.com", "baum.review", "package viewserver"} {
					if strings.Contains(text, leak) {
						t.Errorf("%s: response leaks %q", p, leak)
					}
				}
				switch {
				case resp.StatusCode >= 300 && resp.StatusCode < 400:
				case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
				case resp.StatusCode == http.StatusOK && text == tt.index:
				default:
					t.Errorf("%s: status %d, body %.60q", p, resp.StatusCode, text)
				}
			}
		})
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	for _, p := range []string{"/", "/foo", "/api/document", "/api/health", "/api/sessions", "/api/nope", "/assets/missing.js"} {
		resp, _ := get(t, base+p)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe") || strings.Contains(csp, "*") {
			t.Errorf("%s: CSP = %q", p, csp)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", p)
		}
		if resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: missing Referrer-Policy", p)
		}
		for name := range resp.Header {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Errorf("%s: unexpected CORS header %s", p, name)
			}
		}
	}
	// The policy is sent with a rejected host too.
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a rejected host response has no security headers")
	}
}

func TestEventsStreamWithoutWatch(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture), PingInterval: 10 * time.Millisecond})
	stream := openEvents(t, base)
	stream.next(t, ": ping")
}

func TestWatchReloadsOnRewrite(t *testing.T) {
	path := fixtureFile(t)
	_, base := start(t, Options{Path: path, Watch: true})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	writeFile(t, path, updated)
	stream.next(t, "event: document")
	if got := fetchDocument(t, base); got != updated {
		t.Errorf("document = %q after the event, want %q", got, updated)
	}
}

func TestWatchSurvivesAtomicRename(t *testing.T) {
	path := fixtureFile(t)
	var warnings syncBuffer
	_, base := start(t, Options{Path: path, Watch: true, Warn: &warnings})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	for i, content := range []string{updated, fixture + " ", updated + "  "} {
		temp := path + ".tmp"
		writeFile(t, temp, content)
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
		stream.next(t, "event: document")
		if got := fetchDocument(t, base); got != content {
			t.Fatalf("rename %d: document = %q, want %q", i, got, content)
		}
	}
}

func TestWatchKeepsPreviousDocumentWhenInvalid(t *testing.T) {
	path := fixtureFile(t)
	var warnings syncBuffer
	_, base := start(t, Options{Path: path, Watch: true, Warn: &warnings})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	writeFile(t, path, "{\"schema\": ")
	deadline := time.Now().Add(wait)
	for !strings.Contains(warnings.String(), "not valid JSON") {
		if time.Now().After(deadline) {
			t.Fatalf("no warning for invalid JSON, got %q", warnings.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fetchDocument(t, base); got != fixture {
		t.Errorf("document = %q, want the previous one", got)
	}

	// The bad write sent no event: the first event line after it is the one for
	// the good write.
	writeFile(t, path, updated)
	stream.nextEvent(t, "event: document")
	eventually(t, base, updated)
}

func TestWatchRejectsOversizedDocument(t *testing.T) {
	path := fixtureFile(t)
	var warnings syncBuffer
	_, base := start(t, Options{Path: path, Watch: true, Warn: &warnings})

	large := "[" + strings.Repeat("0,", MaxDocumentBytes/2) + "0]"
	writeFile(t, path, large)
	deadline := time.Now().Add(wait)
	for !strings.Contains(warnings.String(), "limit") {
		if time.Now().After(deadline) {
			t.Fatalf("no warning for an oversized document, got %q", warnings.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fetchDocument(t, base); got != fixture {
		t.Error("the oversized document replaced the previous one")
	}
}

func TestWatchToleratesMissingFile(t *testing.T) {
	path := fixtureFile(t)
	_, base := start(t, Options{Path: path, Watch: true})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fetchDocument(t, base); got != fixture {
		t.Errorf("document = %q while the file is missing", got)
	}
	writeFile(t, path, updated)
	stream.next(t, "event: document")
	eventually(t, base, updated)
}

func TestWatchWarnsAgainAboutFindings(t *testing.T) {
	path := fixtureFile(t)
	var warnings syncBuffer
	validate := func(document []byte) []string {
		if bytes.Contains(document, []byte("rewritten")) {
			return []string{"title: is rewritten"}
		}
		return nil
	}
	_, base := start(t, Options{Path: path, Watch: true, Warn: &warnings, Validate: validate})
	stream := openEvents(t, base)
	stream.next(t, ": connected")
	writeFile(t, path, updated)
	stream.next(t, "event: document")
	if !strings.Contains(warnings.String(), "title: is rewritten") {
		t.Errorf("warnings = %q", warnings.String())
	}
}

func TestInvalidDocumentIsServedWithWarnings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, "{}")
	var warnings syncBuffer
	validate := func([]byte) []string { return []string{"summary: missing"} }
	_, base := start(t, Options{Path: path, Warn: &warnings, Validate: validate})
	if got := fetchDocument(t, base); got != "{}" {
		t.Errorf("document = %q", got)
	}
	if !strings.Contains(warnings.String(), "summary: missing") {
		t.Errorf("warnings = %q", warnings.String())
	}
}

func TestNewRejectsBadDocuments(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "not json")
	large := filepath.Join(dir, "large.json")
	writeFile(t, large, "["+strings.Repeat("0,", MaxDocumentBytes/2)+"0]")

	tests := map[string]Options{
		"invalid JSON":     {Path: bad},
		"too large":        {Path: large},
		"missing":          {Path: filepath.Join(dir, "missing.json")},
		"neither":          {},
		"both":             {Path: bad, Document: []byte("{}")},
		"watch fixed":      {Document: []byte("{}"), Watch: true},
		"invalid fixed":    {Document: []byte("nope")},
		"empty (not JSON)": {Document: []byte("")},
	}
	for name, opts := range tests {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func TestListenReportsPortInUse(t *testing.T) {
	first, err := New(Options{Document: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	url, err := first.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.listener.Close() })
	port := url[strings.LastIndex(url, ":")+1 : len(url)-1]

	second, err := New(Options{Document: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Listen(number); err == nil {
		t.Fatal("Listen on a taken port succeeded")
	}
}

func TestShutdownEndsEventStreams(t *testing.T) {
	path := fixtureFile(t)
	server, err := New(Options{Path: path, Watch: true})
	if err != nil {
		t.Fatal(err)
	}
	url, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()

	stream := openEvents(t, strings.TrimSuffix(url, "/"))
	stream.next(t, ": connected")
	cancel()
	// The client's stream reaches EOF well inside the grace period because the
	// handler returns at once; a handler that waited for the client would hold
	// the shutdown until the grace period ran out.
	eof := time.After(shutdownGrace / 2)
	for open := true; open; {
		select {
		case _, open = <-stream.lines:
		case <-eof:
			t.Fatal("the event stream did not end after shutdown began")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(shutdownGrace / 2):
		t.Fatal("Serve did not return after the stream ended")
	}
}

func TestServeReturnsOnlyAfterTheWatcherStops(t *testing.T) {
	path := fixtureFile(t)
	server, err := New(Options{Path: path, Watch: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	// The watcher is done, so nothing reads or swaps the document any more.
	writeFile(t, path, updated)
	time.Sleep(20 * time.Millisecond)
	if string(server.current()) != fixture {
		t.Error("the watcher kept running after Serve returned")
	}
}

func TestHeadEventsReturnsHeadersOnly(t *testing.T) {
	_, base := start(t, Options{Document: []byte(fixture)})
	client := &http.Client{Timeout: wait}
	resp, err := client.Head(base + "/api/events")
	if err != nil {
		t.Fatalf("HEAD /api/events did not finish: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("HEAD /api/events: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestWatchSeesASaveBetweenNewAndServe(t *testing.T) {
	path := fixtureFile(t)
	server, err := New(Options{Path: path, Watch: true, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// The file changes after the document was read and before the watcher runs.
	writeFile(t, path, updated)
	url, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	eventually(t, strings.TrimSuffix(url, "/"), updated)
}

func TestUseAfterServePanics(t *testing.T) {
	server, _ := start(t, Options{Document: []byte(fixture)})
	defer func() {
		if recover() == nil {
			t.Error("Use after Serve did not panic")
		}
	}()
	// Serve runs in a goroutine; wait until it has fixed the chain.
	deadline := time.Now().Add(wait)
	for !server.serving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	server.Use(func(next http.Handler) http.Handler { return next })
}

func TestExtensionPoints(t *testing.T) {
	server, err := New(Options{Document: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	server.Mux().HandleFunc("GET /api/extra", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "extra")
	})
	server.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != "secret" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	url, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()

	req, _ := http.NewRequest(http.MethodGet, url+"api/extra", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: status %d", resp.StatusCode)
	}
	req.Header.Set("X-Token", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "extra" {
		t.Errorf("with a token: %d %q", resp.StatusCode, body)
	}
}
