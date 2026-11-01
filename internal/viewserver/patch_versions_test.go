package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// versionDocument is version n of acme/widgets#7, comparing mergeBase to
// head, with a hunk in each of paths; a path written "role:path" gives its
// hunk that role.
func versionDocument(n int, head, mergeBase string, paths ...string) string {
	var hunks []string
	for i, p := range paths {
		role := ""
		if r, rest, ok := strings.Cut(p, ":"); ok {
			role, p = r, rest
		}
		hunks = append(hunks, fmt.Sprintf(`{"id":"h%d","path":%q,"role":%q}`, i, p, role))
	}
	return fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":"acme/widgets","pull_request":7,"version":%d,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[%s]}}`+"\n",
		n, head, base0, mergeBase, strings.Join(hunks, ","))
}

// savedVersions answers Options.Versions from a table.
func savedVersions(documents map[int]string) VersionsFunc {
	return func(_ context.Context, n int) ([]byte, error) {
		document, ok := documents[n]
		if !ok {
			return nil, ErrNoVersion
		}
		return []byte(document), nil
	}
}

func fetchVersionPatch(t *testing.T, base, version, path string) (*http.Response, *string) {
	t.Helper()
	query := url.Values{"path": {path}}
	if version != "" {
		query.Set("version", version)
	}
	resp, body := get(t, base+"/api/patch?"+query.Encode())
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

// A saved version's patch is read between that version's own commits, for a
// path that version cites; the served document stays the answer without a
// version or with its own.
func TestPatchOfASavedVersionUsesItsCommitsAndPaths(t *testing.T) {
	headC := strings.Repeat("c", 40)
	p := &patcher{}
	_, base := start(t, Options{
		Document: []byte(versionDocument(2, headB, headC, "src/a.go")),
		Patch:    p.fn,
		Versions: savedVersions(map[int]string{
			1: versionDocument(1, headA, headC, "src/old.go"),
			4: strings.Replace(versionDocument(4, headA, headC, "src/a.go"), `"pull_request":7`, `"pull_request":8`, 1),
			5: strings.Replace(versionDocument(5, headA, headC, "src/a.go"), `"acme/widgets"`, `"acme/gadgets"`, 1),
			6: versionDocument(3, headA, headC, "src/a.go"),
		}),
	})
	for _, version := range []string{"", "2"} {
		if resp, patch := fetchVersionPatch(t, base, version, "src/a.go"); resp.StatusCode != http.StatusOK || patch == nil || p.last.Head != headB {
			t.Errorf("version %q: status %d, patch %v, head %s", version, resp.StatusCode, patch, p.last.Head)
		}
	}
	if resp, patch := fetchVersionPatch(t, base, "1", "src/old.go"); resp.StatusCode != http.StatusOK || patch == nil {
		t.Fatalf("version 1: status %d, patch %v", resp.StatusCode, patch)
	}
	if p.last.Head != headA || p.last.MergeBase != headC || p.last.Version != 1 {
		t.Errorf("version 1 read %+v", p.last)
	}
	for _, c := range []struct{ version, path, why string }{
		{"1", "src/a.go", "a path only the served document cites"},
		{"", "src/old.go", "a path only version 1 cites"},
		{"3", "src/a.go", "a version that does not exist"},
		{"4", "src/a.go", "a version of another pull request"},
		{"5", "src/a.go", "a version of another repository"},
		{"6", "src/a.go", "a document that is another version"},
		{"0", "src/a.go", "version 0"},
		{"-1", "src/a.go", "a negative version"},
		{"01", "src/a.go", "a version not written plainly"},
		{"one", "src/a.go", "a version that is not a number"},
	} {
		if resp, _ := fetchVersionPatch(t, base, c.version, c.path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", c.why, resp.StatusCode)
		}
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("read %d patches, want 2: the served document's once, kept, and version 1's", n)
	}
}

// Without Options.Versions only the served document's version has patches;
// a version that cannot be read is a failure, not a missing one.
func TestPatchOfAVersionThatCannotBeRead(t *testing.T) {
	p := &patcher{}
	_, base := start(t, Options{Document: []byte(versionDocument(2, headB, headA, "src/a.go")), Patch: p.fn})
	if resp, _ := fetchVersionPatch(t, base, "1", "src/a.go"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("no versions: status %d", resp.StatusCode)
	}
	var reads atomic.Int32
	_, base = start(t, Options{Document: []byte(versionDocument(2, headB, headA, "src/a.go")), Patch: p.fn, Versions: func(context.Context, int) ([]byte, error) {
		if reads.Add(1) == 1 {
			return nil, errors.New("the store is locked")
		}
		return []byte(versionDocument(1, headA, headA, "src/a.go")), nil
	}})
	if resp, _ := fetchVersionPatch(t, base, "1", "src/a.go"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a failed read: status %d", resp.StatusCode)
	}
	if resp, patch := fetchVersionPatch(t, base, "1", "src/a.go"); resp.StatusCode != http.StatusOK || patch == nil {
		t.Errorf("read again: status %d, patch %v", resp.StatusCode, patch)
	}
}

// Versions that compare the same commits share one read of a file, whether
// their requests arrive together or one after the other.
func TestVersionsWithTheSameRangeShareOneRead(t *testing.T) {
	hold := make(chan struct{})
	p := &patcher{reply: func(int, string) (locallink.PatchResult, error) {
		<-hold
		return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-a\n+b\n"}, nil
	}}
	_, base := start(t, Options{
		Document: []byte(versionDocument(2, headB, headA, "src/a.go")),
		Patch:    p.fn,
		Versions: savedVersions(map[int]string{1: versionDocument(1, headB, headA, "src/a.go", "src/b.go")}),
	})
	var done sync.WaitGroup
	for i := range 6 {
		version := []string{"", "1", "2"}[i%3]
		done.Go(func() {
			if resp, patch := fetchVersionPatch(t, base, version, "src/a.go"); resp.StatusCode != http.StatusOK || patch == nil {
				t.Errorf("version %q: status %d, patch %v", version, resp.StatusCode, patch)
			}
		})
	}
	time.Sleep(100 * time.Millisecond) // time for every request to reach the wait
	close(hold)
	done.Wait()
	fetchVersionPatch(t, base, "1", "src/a.go")
	if n := p.calls.Load(); n != 1 {
		t.Errorf("read %d times, want 1", n)
	}
}

// A generated or vendored file is read like any other: the bundle cuts its
// text only over budget, and the patch is masked as stored hunks are.
func TestGeneratedAndVendoredFilesArePatchedLikeAnyOther(t *testing.T) {
	p := &patcher{}
	_, base := start(t, Options{
		Document: []byte(versionDocument(1, headB, headA, "generated:api/client.go", "vendor/lib/x.go")),
		Patch:    p.fn,
	})
	for _, path := range []string{"api/client.go", "vendor/lib/x.go"} {
		if resp, patch := fetchVersionPatch(t, base, "", path); resp.StatusCode != http.StatusOK || patch == nil {
			t.Errorf("%s: status %d, patch %v", path, resp.StatusCode, patch)
		}
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("read %d patches, want 2", n)
	}
}

// The whole path from a clone: two saved versions of a pull request whose
// head moved, each answered between its own commits, with a key the later
// head adds masked.
func TestSavedVersionsPatchesFromAClone(t *testing.T) {
	f := linktest.New(t)
	secret := "sk_" + "live_" + "oHBvRPOIvGrv5iFlbCBFNOgmBjMtpsia"
	linktest.Git(t, f.Repo, "checkout", "-q", "feature")
	if err := os.WriteFile(filepath.Join(f.Repo, "b.txt"), []byte("b\nkey = \""+secret+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.Repo, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Repo, "vendor", "x.go"), []byte("key = \""+secret+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Key")
	moved := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")

	document := func(n int, head string) string {
		return fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":%q,"pull_request":7,"version":%d,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[{"id":"h0","path":"b.txt","role":"docs"},{"id":"h1","path":"c.txt","role":"docs"},{"id":"h2","path":"vendor/x.go","role":"vendored"}]}}`+"\n",
			linktest.Repository, n, head, f.Base, f.Base)
	}
	patcher := new(locallink.Patcher)
	_, base := start(t, Options{
		Document: []byte(document(2, moved)),
		Patch: func(ctx context.Context, review Review, path string) (locallink.PatchResult, error) {
			return patcher.Patch(ctx, locallink.PatchRequest{Dir: f.Repo, Repository: review.Repository, Head: review.Head, Base: review.Base, MergeBase: review.MergeBase, Path: path})
		},
		Versions: savedVersions(map[int]string{1: document(1, f.Commits[0])}),
	})
	if _, patch := fetchVersionPatch(t, base, "1", "b.txt"); patch == nil || *patch != "@@ -0,0 +1 @@\n+b\n" {
		t.Errorf("version 1, b.txt: %v", patch)
	}
	if resp, patch := fetchVersionPatch(t, base, "1", "c.txt"); resp.StatusCode != http.StatusOK || patch != nil {
		t.Errorf("version 1, c.txt, which its range does not change: status %d, patch %v", resp.StatusCode, patch)
	}
	if _, patch := fetchVersionPatch(t, base, "2", "c.txt"); patch == nil || *patch != "@@ -0,0 +1 @@\n+c\n" {
		t.Errorf("version 2, c.txt: %v", patch)
	}
	_, patch := fetchVersionPatch(t, base, "", "b.txt")
	if patch == nil || strings.Contains(*patch, secret) || !strings.HasPrefix(*patch, "@@ -0,0 +1,2 @@\n+b\n+key = \"") {
		t.Errorf("version 2, b.txt: %q", deref(patch))
	}
	_, patch = fetchVersionPatch(t, base, "", "vendor/x.go")
	if patch == nil || strings.Contains(*patch, secret) || !strings.HasPrefix(*patch, "@@ -0,0 +1 @@\n+key = \"") {
		t.Errorf("version 2, the vendored vendor/x.go: %q", deref(patch))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}
