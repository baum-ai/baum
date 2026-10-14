package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// patchDocument cites a hunk in each of paths.
func patchDocument(head string, paths ...string) string {
	var hunks []string
	for i, p := range paths {
		hunks = append(hunks, fmt.Sprintf(`{"id":"h%d","path":%q}`, i, p))
	}
	return fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":"acme/widgets","pull_request":7,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[%s]}}`+"\n",
		head, base0, headB, strings.Join(hunks, ","))
}

// patcher is a PatchFunc that records what it was asked and answers from a
// table.
type patcher struct {
	calls atomic.Int32
	mu    sync.Mutex
	seen  []string
	reply func(n int, path string) (locallink.PatchResult, error)
	last  Review
}

func (p *patcher) fn(_ context.Context, review Review, path string) (locallink.PatchResult, error) {
	n := int(p.calls.Add(1))
	p.mu.Lock()
	p.seen = append(p.seen, path)
	p.last = review
	p.mu.Unlock()
	if p.reply != nil {
		return p.reply(n, path)
	}
	return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-" + path + "\n+new\n"}, nil
}

func fetchPatch(t *testing.T, base, path string) (*http.Response, *string) {
	t.Helper()
	resp, body := get(t, base+"/api/patch?path="+url.QueryEscape(path))
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	var decoded struct {
		Patch *string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("/api/patch: %v\n%s", err, body)
	}
	return resp, decoded.Patch
}

func TestPatchIsServedForACitedPathAndKept(t *testing.T) {
	p := &patcher{}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "src/a.go", "src/b.go")), Patch: p.fn})
	if p.calls.Load() != 0 {
		t.Fatal("a patch was computed before anyone asked")
	}
	resp, patch := fetchPatch(t, base, "src/a.go")
	if patch == nil || *patch != "@@ -1 +1 @@\n-src/a.go\n+new\n" {
		t.Fatalf("patch = %v", patch)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", resp.Header)
	}
	fetchPatch(t, base, "src/a.go")
	if n := p.calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1: a patch is kept", n)
	}
	fetchPatch(t, base, "src/b.go")
	if n := p.calls.Load(); n != 2 {
		t.Errorf("calls = %d, want one per path", n)
	}
	want := Review{Repository: "acme/widgets", PullRequest: 7, Head: headA, Base: base0, MergeBase: headB}
	if p.last != want {
		t.Errorf("asked about %+v, want %+v", p.last, want)
	}
	head, err := http.Head(base + "/api/patch?path=src/a.go")
	if err != nil {
		t.Fatal(err)
	}
	_ = head.Body.Close()
	if head.StatusCode != http.StatusOK || p.calls.Load() != 2 {
		t.Errorf("HEAD: status %d, calls %d", head.StatusCode, p.calls.Load())
	}
}

func TestOnlyPathsTheDocumentCitesHavePatches(t *testing.T) {
	p := &patcher{}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "src/a.go")), Patch: p.fn})
	for _, path := range []string{"src/secret.go", "../a.go", "src/../src/a.go", "/src/a.go", "--output=/tmp/x", "-p", "src", ""} {
		if resp, patch := fetchPatch(t, base, path); resp.StatusCode != http.StatusNotFound || patch != nil {
			t.Errorf("path %q: status %d", path, resp.StatusCode)
		}
	}
	if resp, _ := get(t, base+"/api/patch"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("no path: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, base+"/api/patch?path=src/a.go&path=src/b.go"); resp.StatusCode != http.StatusOK {
		t.Errorf("the first path is the one asked for: status %d", resp.StatusCode)
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("calls = %d, want only the cited path read", n)
	}
	if got := p.seen[0]; got != "src/a.go" {
		t.Errorf("read %q", got)
	}
}

func TestUnavailablePatchesAreNullAndOnlyStableOnesKept(t *testing.T) {
	p := &patcher{reply: func(_ int, path string) (locallink.PatchResult, error) {
		switch path {
		case "binary.bin":
			return locallink.PatchResult{Code: locallink.CodeNoTextPatch}, nil
		default:
			return locallink.PatchResult{Code: locallink.CodeCommitsMissing}, nil
		}
	}}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "binary.bin", "missing.go")), Patch: p.fn})
	for range 2 {
		for _, path := range []string{"binary.bin", "missing.go"} {
			resp, patch := fetchPatch(t, base, path)
			if resp.StatusCode != http.StatusOK || patch != nil {
				t.Errorf("%s: status %d, patch %v; want 200 and null", path, resp.StatusCode, patch)
			}
		}
	}
	if n := p.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3: a file with no text patch is kept, missing commits are asked again", n)
	}
	// The body is exactly {"patch":null}: nothing about the machine leaks.
	_, body := get(t, base+"/api/patch?path=missing.go")
	if strings.TrimSpace(body) != `{"patch":null}` {
		t.Errorf("body = %q", body)
	}
}

func TestPatchWithoutAFunctionOrForTheSampleIsNull(t *testing.T) {
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "src/a.go"))})
	if resp, patch := fetchPatch(t, base, "src/a.go"); resp.StatusCode != http.StatusOK || patch != nil {
		t.Errorf("no function: status %d, patch %v", resp.StatusCode, patch)
	}
	p := &patcher{}
	_, base = start(t, Options{Document: []byte(patchDocument(headA, "src/a.go")), Sample: true, Patch: p.fn})
	if resp, patch := fetchPatch(t, base, "src/a.go"); resp.StatusCode != http.StatusOK || patch != nil || p.calls.Load() != 0 {
		t.Errorf("sample: status %d, patch %v, calls %d", resp.StatusCode, patch, p.calls.Load())
	}
}

func TestAFailedPatchIsAServerErrorAndTriedAgain(t *testing.T) {
	p := &patcher{reply: func(n int, _ string) (locallink.PatchResult, error) {
		if n == 1 {
			return locallink.PatchResult{}, errors.New("git broke: /srv/private-clone/secret")
		}
		return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-a\n+b\n"}, nil
	}}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "src/a.go")), Patch: p.fn})
	resp, body := get(t, base+"/api/patch?path=src/a.go")
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(body, "secret") {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	if _, patch := fetchPatch(t, base, "src/a.go"); patch == nil {
		t.Error("the failure was kept")
	}
}

func TestPatchRouteIsBehindTheHostCheckAndReadOnly(t *testing.T) {
	p := &patcher{}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, "src/a.go")), Patch: p.fn})
	port := base[strings.LastIndex(base, ":")+1:]
	for _, host := range []string{"evil.example", "evil.example:" + port, "127.0.0.1", "[::1]:" + port} {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/patch?path=src/a.go", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "patch") {
			t.Errorf("Host %q: status %d, body %q", host, resp.StatusCode, body)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("Host %q: security headers missing", host)
		}
	}
	if p.calls.Load() != 0 {
		t.Error("a rejected host must not read a patch")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, base+"/api/patch?path=src/a.go", strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(resp.Header.Get("Allow"), "GET") {
			t.Errorf("%s: status %d, Allow %q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	if p.calls.Load() != 0 {
		t.Error("a method other than GET must not read a patch")
	}
}

func TestPatchesAreDroppedWhenTheDocumentChanges(t *testing.T) {
	path := fixtureFile(t)
	writeFile(t, path, patchDocument(headA, "src/a.go"))
	p := &patcher{}
	_, base := start(t, Options{Path: path, Watch: true, Patch: p.fn})
	stream := openEvents(t, base)
	stream.next(t, ": connected")

	fetchPatch(t, base, "src/a.go")
	writeFile(t, path, patchDocument(headB, "src/other.go"))
	stream.next(t, "event: document")

	if resp, _ := fetchPatch(t, base, "src/a.go"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a path the new document does not cite: status %d", resp.StatusCode)
	}
	if _, patch := fetchPatch(t, base, "src/other.go"); patch == nil {
		t.Error("the new document's path has no patch")
	}
	if p.last.Head != headB {
		t.Errorf("read for head %q, want the new document's", p.last.Head)
	}
	writeFile(t, path, patchDocument(headA, "src/a.go", "src/other.go"))
	stream.next(t, "event: document")
	fetchPatch(t, base, "src/other.go")
	if n := p.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3: an answer is not kept across a reload", n)
	}
}

// The whole path, with the real reader: a clone the document's pull request
// belongs to, and a file it changed.
func TestPatchEndToEndFromAClone(t *testing.T) {
	f := linktest.New(t)
	document := fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":%q,"pull_request":7,"head":%q,"base":%q},"index":{"hunks":[{"id":"h0","path":"b.txt"},{"id":"h1","path":"a.txt"}]}}`+"\n",
		linktest.Repository, f.Head, f.Base)
	_, base := start(t, Options{Document: []byte(document), Patch: func(ctx context.Context, review Review, path string) (locallink.PatchResult, error) {
		return locallink.Patch(ctx, locallink.PatchRequest{
			Dir: f.Repo, Repository: review.Repository, Head: review.Head, Base: review.Base, MergeBase: review.MergeBase, Path: path,
		})
	}})
	if _, patch := fetchPatch(t, base, "b.txt"); patch == nil || *patch != "@@ -0,0 +1 @@\n+b\n" {
		t.Errorf("b.txt: %v", patch)
	}
	if resp, patch := fetchPatch(t, base, "a.txt"); resp.StatusCode != http.StatusOK || patch != nil {
		t.Errorf("a.txt, which the range does not change: status %d, patch %v", resp.StatusCode, patch)
	}
	if resp, _ := fetchPatch(t, base, "c.txt"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("c.txt, which the document does not cite: status %d", resp.StatusCode)
	}
}
