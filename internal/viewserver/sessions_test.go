package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baumai/baum/internal/locallink"
	"github.com/baumai/baum/schema"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	base0 = "0000000000000000000000000000000000000000"
)

func reviewDocument(head, title string) string {
	return fmt.Sprintf(`{"schema":"baum.review/1","title":%q,"review":{"repository":"acme/widgets","pull_request":7,"head":%q,"base":%q}}`+"\n", title, head, base0)
}

// linker is a SessionsFunc that answers with one linked session and records
// what it was asked.
type linker struct {
	calls atomic.Int32
	mu    sync.Mutex
	seen  []Review
	// block, when set, holds every call until it is closed.
	block chan struct{}
	// reply overrides the answer for call n (1-based).
	reply func(n int) (locallink.Result, error)
}

func (l *linker) fn(_ context.Context, review Review) (locallink.Result, error) {
	n := int(l.calls.Add(1))
	l.mu.Lock()
	l.seen = append(l.seen, review)
	l.mu.Unlock()
	if l.block != nil {
		<-l.block
	}
	if l.reply != nil {
		return l.reply(n)
	}
	return locallink.Result{
		Available: true,
		Sessions:  []locallink.Session{{ID: "s-" + review.Head[:1], Source: "claude_code", Vendor: "claude_code", Outcome: "unknown"}},
		Commits:   []locallink.Commit{{OID: review.Head, Subject: "A change", Links: []locallink.Link{{Session: "s-" + review.Head[:1], Method: "commit_ref", Confidence: 0.95}}}},
	}, nil
}

func fetchSessions(t *testing.T, base string) (*http.Response, map[string]any) {
	t.Helper()
	resp, body := get(t, base+"/api/sessions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/sessions: status %d: %s", resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("/api/sessions: %v\n%s", err, body)
	}
	return resp, decoded
}

func TestSessionsAreComputedLazilyAndKept(t *testing.T) {
	l := &linker{}
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Sessions: l.fn})
	fetchDocument(t, base)
	get(t, base+"/api/health")
	if n := l.calls.Load(); n != 0 {
		t.Fatalf("sessions computed %d times before anyone asked", n)
	}

	resp, first := fetchSessions(t, base)
	if first["available"] != true || len(first["sessions"].([]any)) != 1 || len(first["commits"].([]any)) != 1 {
		t.Errorf("response = %v", first)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	_, second := fetchSessions(t, base)
	if fmt.Sprint(first) != fmt.Sprint(second) || l.calls.Load() != 1 {
		t.Errorf("calls = %d; second response = %v", l.calls.Load(), second)
	}
	want := Review{Repository: "acme/widgets", PullRequest: 7, Head: headA, Base: base0}
	if l.seen[0] != want {
		t.Errorf("asked about %+v, want %+v", l.seen[0], want)
	}

	head, err := http.Head(base + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = head.Body.Close()
	if head.StatusCode != http.StatusOK || head.Header.Get("Content-Type") != "application/json" || l.calls.Load() != 1 {
		t.Errorf("HEAD: status %d, calls %d", head.StatusCode, l.calls.Load())
	}
}

func TestSessionsAreRecomputedWhenTheHeadChanges(t *testing.T) {
	path := fixtureFile(t)
	writeFile(t, path, reviewDocument(headA, "one"))
	l := &linker{}
	_, base := start(t, Options{Path: path, Watch: true, Sessions: l.fn})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	_, first := fetchSessions(t, base)
	if l.calls.Load() != 1 || first["sessions"].([]any)[0].(map[string]any)["id"] != "s-a" {
		t.Fatalf("calls = %d, first = %v", l.calls.Load(), first)
	}

	// A rewrite that keeps the head keeps the answer.
	writeFile(t, path, reviewDocument(headA, "retitled"))
	stream.next(t, "event: document")
	fetchSessions(t, base)
	if l.calls.Load() != 1 {
		t.Fatalf("calls = %d after a rewrite that kept the head", l.calls.Load())
	}

	temp := path + ".tmp"
	writeFile(t, temp, reviewDocument(headB, "retitled"))
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	stream.next(t, "event: document")
	if l.calls.Load() != 1 {
		t.Fatalf("calls = %d: a change must not compute until someone asks", l.calls.Load())
	}
	_, next := fetchSessions(t, base)
	if l.calls.Load() != 2 || next["sessions"].([]any)[0].(map[string]any)["id"] != "s-b" {
		t.Errorf("calls = %d, response = %v", l.calls.Load(), next)
	}
	if l.seen[1].Head != headB {
		t.Errorf("second call asked about %+v", l.seen[1])
	}
}

func TestConcurrentFirstRequestsComputeOnce(t *testing.T) {
	l := &linker{block: make(chan struct{})}
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Sessions: l.fn})

	const requests = 12
	bodies := make(chan string, requests)
	var started sync.WaitGroup
	for range requests {
		started.Add(1)
		go func() {
			started.Done()
			resp, err := http.Get(base + "/api/sessions")
			if err != nil {
				bodies <- "error: " + err.Error()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			bodies <- string(body)
		}()
	}
	started.Wait()
	for deadline := time.Now().Add(wait); l.calls.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the computation never started")
		}
	}
	// Let the other requests reach the server while the first is computing.
	time.Sleep(200 * time.Millisecond)
	close(l.block)
	for range requests {
		if body := <-bodies; !strings.Contains(body, `"available":true`) {
			t.Errorf("body = %q", body)
		}
	}
	if n := l.calls.Load(); n != 1 {
		t.Errorf("computed %d times for %d concurrent requests", n, requests)
	}
}

func TestSampleSessionsAreUnavailable(t *testing.T) {
	l := &linker{}
	_, base := start(t, Options{Document: schema.Example, Sample: true, Sessions: l.fn})
	_, got := fetchSessions(t, base)
	if got["available"] != false || got["code"] != "sample" || got["reason"] == "" || got["sessions"] != nil {
		t.Errorf("response = %v", got)
	}
	if l.calls.Load() != 0 {
		t.Error("the sample must not be linked")
	}
}

func TestSessionsWithoutAFunctionOrAHeadAreUnavailable(t *testing.T) {
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one"))})
	if _, got := fetchSessions(t, base); got["available"] != false || got["code"] != "error" {
		t.Errorf("no function: %v", got)
	}
	l := &linker{}
	_, base = start(t, Options{Document: []byte(fixture), Sessions: l.fn})
	if _, got := fetchSessions(t, base); got["available"] != false || got["code"] != "no_head" || l.calls.Load() != 0 {
		t.Errorf("no head: %v (calls %d)", got, l.calls.Load())
	}
}

func TestOnlyAvailableAnswersAreKept(t *testing.T) {
	var warnings syncBuffer
	l := &linker{reply: func(n int) (locallink.Result, error) {
		switch n {
		case 1:
			return locallink.Unavailable(locallink.CodeCommitsMissing, "fetch them"), nil
		case 2:
			return locallink.Result{}, errors.New("disk on fire")
		}
		return locallink.Result{Available: true}, nil
	}}
	failureTTL = time.Millisecond
	t.Cleanup(func() { failureTTL = 30 * time.Second })
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Sessions: l.fn, Warn: &warnings})
	if _, got := fetchSessions(t, base); got["code"] != "commits_missing" || got["reason"] != "fetch them" {
		t.Errorf("first = %v", got)
	}
	_, got := fetchSessions(t, base)
	if got["available"] != false || got["code"] != "error" || strings.Contains(fmt.Sprint(got), "disk") {
		t.Errorf("an error must be a generic unavailable answer, got %v", got)
	}
	time.Sleep(20 * time.Millisecond) // the failure is kept only briefly
	if !strings.Contains(warnings.String(), "disk on fire") {
		t.Errorf("warnings = %q", warnings.String())
	}
	if _, got := fetchSessions(t, base); got["available"] != true || len(got["sessions"].([]any)) != 0 {
		t.Errorf("third = %v", got)
	}
	fetchSessions(t, base)
	if n := l.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3: the available answer is kept", n)
	}
}

func TestSessionsRouteIsBehindTheHostCheckAndHeaders(t *testing.T) {
	l := &linker{}
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Sessions: l.fn})
	port := base[strings.LastIndex(base, ":")+1:]
	for _, host := range []string{"evil.example", "evil.example:" + port, "127.0.0.1", "[::1]:" + port} {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/sessions", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "available") {
			t.Errorf("Host %q: status %d, body %q", host, resp.StatusCode, body)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("Host %q: security headers missing", host)
		}
	}
	if l.calls.Load() != 0 {
		t.Error("a rejected host must not trigger the computation")
	}

	resp, _ := get(t, base+"/api/sessions")
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("security headers missing: %v", resp.Header)
	}
	for name := range resp.Header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Errorf("unexpected CORS header %s", name)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, base+"/api/sessions", strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(resp.Header.Get("Allow"), "GET") {
			t.Errorf("%s: status %d, Allow %q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}
